package integrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrDenied = errors.New("authorization was cancelled")

type Connection struct {
	IdentityMode    string `json:"identity_mode"`
	Provider        string `json:"provider"`
	Configured      bool   `json:"configured"`
	Status          string `json:"status"`
	AccountName     string `json:"account_name"`
	AccountURL      string `json:"account_url"`
	CallbackURL     string `json:"callback_url"`
	InstallationURL string `json:"installation_url"`
	SetupAvailable  bool   `json:"setup_available"`
	AppEditable     bool   `json:"app_editable"`
	AppClientID     string `json:"app_client_id"`
	AppSlug         string `json:"app_slug"`
	RegistrationURL string `json:"registration_url"`
}

func (s *Service) Connections(ctx context.Context, project string) ([]Connection, error) {
	result := []Connection{}
	for _, provider := range []string{"github", "linear"} {
		current, err := s.resolve(ctx, provider)
		if err != nil {
			return nil, err
		}
		value := Connection{Provider: provider, Configured: current.Configured(provider), Status: "disconnected", CallbackURL: s.CallbackURL(provider), SetupAvailable: s.vault != nil && !s.environmentApp(provider)}
		if provider == "linear" {
			value.RegistrationURL = s.LinearRegistrationURL()
			value.AppClientID = current.app(provider).ClientID
			if value.SetupAvailable {
				var active bool
				if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integrations WHERE provider=$1 AND enabled=true)`, provider).Scan(&active); err != nil {
					return nil, err
				}
				value.AppEditable = !active
			}
		}
		if !value.Configured {
			value.Status = "not_configured"
		}
		var enabled bool
		var config []byte
		if err := s.pool.QueryRow(ctx, `SELECT enabled,config FROM integrations WHERE project_id=$1 AND provider=$2`, project, provider).Scan(&enabled, &config); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		var metadata account
		_ = json.Unmarshal(config, &metadata)
		if enabled {
			value.AccountName, value.AccountURL = metadata.Name, metadata.URL
			if value.Configured {
				value.Status = metadata.Status
				if value.Status != "connected" {
					value.Status = "reconnect_required"
				}
			}
		}
		if provider == "github" {
			value.InstallationURL = current.installationURL()
			value.AppSlug = current.config.GitHubAppSlug
			value.AppEditable = value.SetupAvailable && value.Configured
		}
		identity, err := s.Identity(ctx, project, provider)
		if err != nil {
			return nil, err
		}
		value.IdentityMode = identity.Mode
		if provider == "linear" && identity.Mode == "app" {
			value.AppEditable = false
			value.AccountName = identity.AccountName
			switch identity.Status {
			case "enabled":
				value.Status = "connected"
			case "disabled":
				value.Status = "disconnected"
			default:
				value.Status = "reconnect_required"
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
func randomValue() string {
	var data [32]byte
	_, _ = rand.Read(data[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}
func CookieName(provider, state string) string {
	return "circular_oauth_" + provider + "_" + digest(state)[:16]
}

type Authorization struct {
	URL     string
	State   string
	Browser string
}

func rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Service) Begin(ctx context.Context, project, provider string) (Authorization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Authorization{}, err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, provider); err != nil {
		return Authorization{}, err
	}
	current, err := s.resolveWith(ctx, provider, tx)
	if err != nil {
		return Authorization{}, err
	}
	s = current
	if !s.Configured(provider) {
		return Authorization{}, ErrConfiguration
	}
	state, browser, verifier := randomValue(), randomValue(), randomValue()
	hash := digest(state)
	ciphertext, err := s.seal([]byte(verifier), hash)
	if err != nil {
		return Authorization{}, err
	}
	var id string
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO integrations(id,project_id,provider,config,enabled,auth_generation) VALUES($1,$2,$3,'{}',false,1)
        ON CONFLICT(project_id,provider) DO UPDATE SET auth_generation=integrations.auth_generation+1
        RETURNING id,auth_generation`, uuid.NewString(), project, provider).Scan(&id, &generation)
	if err != nil {
		return Authorization{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM integration_oauth_states WHERE integration_id=$1 OR expires_at<now()`, id); err != nil {
		return Authorization{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO integration_oauth_states(state_hash,browser_hash,integration_id,auth_generation,verifier,expires_at,app_client_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, hash, digest(browser), id, generation, ciphertext, time.Now().Add(10*time.Minute), s.app(provider).ClientID); err != nil {
		return Authorization{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Authorization{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	return Authorization{URL: s.authorizeURL(provider, state, base64.RawURLEncoding.EncodeToString(challenge[:])), State: state, Browser: browser}, nil
}

// Complete binds the authorization code to its provider, Project, browser, and
// generation. A disconnect or newer connection attempt invalidates old callbacks.
func (s *Service) Complete(ctx context.Context, provider, state, browser, code string) (string, error) {
	if provider == "linear" {
		var appState bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity_oauth_states WHERE state_hash=$1)`, digest(state)).Scan(&appState); err != nil {
			return "", err
		}
		if appState {
			return s.completeLinearAppAuthorization(ctx, state, browser, code)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(ctx, tx)
	// App replacement and authorization must observe the same registration.
	if err := lockApp(ctx, tx, provider); err != nil {
		return "", err
	}
	current, err := s.resolveWith(ctx, provider, tx)
	if err != nil {
		return "", err
	}
	s = current
	if !s.Configured(provider) {
		return "", ErrConfiguration
	}
	if len(state) != 43 || len(browser) != 43 || len(code) > 8192 {
		return "", ErrCallback
	}
	var id, project string
	var encryptedVerifier []byte
	err = tx.QueryRow(ctx, `SELECT i.id,i.project_id,s.verifier FROM integration_oauth_states s JOIN integrations i ON i.id=s.integration_id
        WHERE s.state_hash=$1 AND s.browser_hash=$2 AND i.provider=$3 AND s.expires_at>now() AND s.auth_generation=i.auth_generation AND (s.app_client_id='' OR s.app_client_id=$4)
        FOR UPDATE OF i,s`, digest(state), digest(browser), provider, s.app(provider).ClientID).Scan(&id, &project, &encryptedVerifier)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCallback
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM integration_oauth_states WHERE state_hash=$1`, digest(state)); err != nil {
		return "", err
	}
	finishError := func(problem error) (string, error) {
		if err := tx.Commit(ctx); err != nil {
			return project, err
		}
		return project, problem
	}
	if code == "" {
		return finishError(ErrDenied)
	}
	verifier, err := s.open(encryptedVerifier, digest(state))
	if err != nil {
		return finishError(ErrCallback)
	}
	token, err := s.token(ctx, provider, code, string(verifier), "")
	if err != nil {
		return finishError(err)
	}
	metadata, err := s.identity(ctx, provider, token.AccessToken)
	if err != nil {
		_ = s.revoke(ctx, provider, token)
		return finishError(err)
	}
	metadata.Status = "connected"
	encoded, _ := json.Marshal(token)
	ciphertext, err := s.seal(encoded, id)
	if err != nil {
		return finishError(err)
	}
	config, _ := json.Marshal(metadata)
	if _, err = tx.Exec(ctx, `UPDATE integrations SET credentials=$2,config=$3,enabled=true,granted_scopes=$4,updated_at=now() WHERE id=$1`, id, ciphertext, config, nonNilScopes(token.Scopes)); err != nil {
		return "", err
	}
	if provider == "linear" {
		if _, err = tx.Exec(ctx, `UPDATE linear_run_updates SET status='pending',next_attempt_at=now(),last_error='' WHERE project_id=$1 AND status IN ('pending','failed')`, project); err != nil {
			return "", err
		}
	}
	return project, tx.Commit(ctx)
}

func (s *Service) identity(ctx context.Context, provider, token string) (account, error) {
	if provider == "github" {
		var user struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		}
		if err := s.github(ctx, token, "/user", &user); err != nil {
			return account{}, err
		}
		if user.ID <= 0 || user.Login == "" {
			return account{}, ErrProvider
		}
		return account{ID: strconv.FormatInt(user.ID, 10), Name: user.Login, URL: "https://github.com/" + url.PathEscape(user.Login)}, nil
	}
	var result struct {
		Organization struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			URLKey string `json:"urlKey"`
		} `json:"organization"`
	}
	if err := s.linear(ctx, token, `query CircularIdentity { organization { id name urlKey } }`, nil, &result); err != nil {
		return account{}, err
	}
	if _, err := uuid.Parse(result.Organization.ID); err != nil || result.Organization.Name == "" {
		return account{}, ErrProvider
	}
	return account{ID: result.Organization.ID, Name: result.Organization.Name, URL: "https://linear.app/" + url.PathEscape(result.Organization.URLKey)}, nil
}

// Credential refresh is serialized with disconnect and other refreshes across
// API/worker processes. Rotated tokens are committed before being handed off.
func (s *Service) withToken(ctx context.Context, project, provider string, use func(string) error) error {
	if provider == "linear" {
		bound, err := s.hasIdentityBinding(ctx, project, provider)
		if err != nil {
			return err
		}
		if bound {
			return s.withIdentityToken(ctx, project, provider, use)
		}
	}
	return s.withUserToken(ctx, project, provider, use)
}

func (s *Service) withUserToken(ctx context.Context, project, provider string, use func(string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	var id string
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id,credentials FROM integrations WHERE project_id=$1 AND provider=$2 AND enabled=true FOR UPDATE`, project, provider).Scan(&id, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReconnect
	}
	if err != nil {
		return err
	}
	current, err := s.resolveWith(ctx, provider, tx)
	if err != nil {
		return err
	}
	s = current
	if !s.Configured(provider) {
		return ErrConfiguration
	}
	plaintext, err := s.open(encrypted, id)
	if err != nil {
		// Preserve unreadable ciphertext so restoring the configured key can
		// recover access, while reporting the need for attention in Setup.
		if _, updateErr := tx.Exec(ctx, `UPDATE integrations SET config=jsonb_set(config::jsonb,'{status}','"reconnect_required"')::json,updated_at=now() WHERE id=$1`, id); updateErr != nil {
			return updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return err
	}
	var token tokenSet
	if json.Unmarshal(plaintext, &token) != nil || !safeToken(token.AccessToken) {
		return ErrReconnect
	}
	markReconnect := func() error {
		if _, err := tx.Exec(ctx, `UPDATE integrations SET credentials=NULL,config=jsonb_set(config::jsonb,'{status}','"reconnect_required"')::json,updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrReconnect
	}
	if !token.ExpiresAt.IsZero() && time.Until(token.ExpiresAt) < 90*time.Second {
		if token.RefreshToken == "" {
			return markReconnect()
		}
		refreshed, err := s.token(ctx, provider, "", "", token.RefreshToken)
		if errors.Is(err, ErrReconnect) {
			return markReconnect()
		}
		if err != nil {
			return err
		}
		if refreshed.Scopes == nil {
			refreshed.Scopes = token.Scopes
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = token.RefreshToken
		}
		encoded, _ := json.Marshal(refreshed)
		ciphertext, err := s.seal(encoded, id)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE integrations SET credentials=$2,granted_scopes=$3,updated_at=now() WHERE id=$1`, id, ciphertext, nonNilScopes(refreshed.Scopes)); err != nil {
			return err
		}
		encrypted = ciphertext
		token = refreshed
	}
	// Save rotations even if the following provider read fails transiently.
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	err = use(token.AccessToken)
	if errors.Is(err, ErrReconnect) {
		// Do not invalidate a newer connection/refresh that completed in parallel.
		_, _ = s.pool.Exec(ctx, `UPDATE integrations SET credentials=NULL,config=jsonb_set(config::jsonb,'{status}','"reconnect_required"')::json,updated_at=now() WHERE id=$1 AND credentials=$2`, id, encrypted)
	}
	return err
}

// Disconnect always removes local access. The boolean says whether revocation
// at the provider was confirmed, so a network failure is visible to the caller.
func (s *Service) Disconnect(ctx context.Context, project, provider string) (bool, error) {
	if provider == "linear" {
		bound, err := s.hasIdentityBinding(ctx, project, provider)
		if err != nil {
			return false, err
		}
		if bound {
			return true, s.DetachIdentity(ctx, project, provider)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(ctx, tx)
	var id string
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id,credentials FROM integrations WHERE project_id=$1 AND provider=$2 FOR UPDATE`, project, provider).Scan(&id, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if current, err := s.resolveWith(ctx, provider, tx); err == nil {
		s = current
	}
	var token tokenSet
	if plain, err := s.open(encrypted, id); err == nil {
		_ = json.Unmarshal(plain, &token)
	}
	if _, err = tx.Exec(ctx, `UPDATE integrations SET credentials=NULL,enabled=false,config='{}',granted_scopes='{}',auth_generation=auth_generation+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM integration_oauth_states WHERE integration_id=$1`, id); err != nil {
		return false, err
	}
	if provider == "linear" {
		if _, err = tx.Exec(ctx, `UPDATE linear_run_updates SET status='skipped',last_error='Linear was disconnected before delivery' WHERE project_id=$1 AND status='pending'`, project); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if len(encrypted) == 0 {
		return true, nil
	}
	if token.AccessToken == "" {
		return false, nil
	}
	return s.revoke(ctx, provider, token) == nil, nil
}

func nonNilScopes(scopes []string) []string {
	if scopes == nil {
		return []string{}
	}
	return scopes
}
