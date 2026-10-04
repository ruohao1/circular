package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func linearIdentityScopes(purpose string) ([]string, error) {
	if purpose == "agent" {
		return []string{"read", "comments:create", "app:mentionable", "app:assignable"}, nil
	}
	if purpose != "identity" {
		return nil, ErrIdentityUnavailable
	}
	return []string{"read", "comments:create"}, nil
}
func containsScopes(granted, wanted []string) bool {
	for _, scope := range wanted {
		if !slices.Contains(granted, scope) {
			return false
		}
	}
	return true
}

func (s *Service) BeginLinearAppAuthorization(ctx context.Context, project, purpose string) (Authorization, error) {
	scopes, err := linearIdentityScopes(purpose)
	if err != nil {
		return Authorization{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Authorization{}, err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "linear"); err != nil {
		return Authorization{}, err
	}
	current, err := s.resolveWith(ctx, "linear", tx)
	if err != nil {
		return Authorization{}, err
	}
	if !current.Configured("linear") {
		return Authorization{}, ErrConfiguration
	}
	var granted []string
	err = tx.QueryRow(ctx, `SELECT i.granted_scopes FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id WHERE b.project_id=$1 AND b.provider='linear' AND i.provider='linear' AND i.app_client_id=$2`, project, current.app("linear").ClientID).Scan(&granted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Authorization{}, err
	}
	if purpose == "identity" {
		agentScopes, _ := linearIdentityScopes("agent")
		if containsScopes(granted, agentScopes) {
			// Reconnecting restores the existing grant. Keep its purpose in the
			// OAuth state so an incomplete callback cannot replace shared access.
			purpose, scopes = "agent", agentScopes
		}
	}
	// Reauthorization must retain permissions already approved for this app,
	// including write access required by native agent activities.
	for _, scope := range granted {
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO identity_authorization_generations(project_id,provider) VALUES($1,'linear') ON CONFLICT(project_id,provider) DO UPDATE SET generation=identity_authorization_generations.generation+1 RETURNING generation`, project).Scan(&generation)
	if err != nil {
		return Authorization{}, err
	}
	var expected string
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT i.account_id FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id WHERE b.project_id=$1 AND b.provider='linear'),(SELECT config->>'account_id' FROM integrations WHERE project_id=$1 AND provider='linear' AND enabled),'')`, project).Scan(&expected)
	if err != nil {
		return Authorization{}, err
	}
	state, browser, verifier := randomValue(), randomValue(), randomValue()
	hash := digest(state)
	encrypted, err := s.seal([]byte(verifier), "identity-state:"+hash)
	if err != nil {
		return Authorization{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity_oauth_states WHERE expires_at<now() OR project_id=$1`, project); err != nil {
		return Authorization{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity_oauth_states(state_hash,browser_hash,project_id,provider,purpose,generation,app_client_id,expected_workspace,verifier,expires_at) VALUES($1,$2,$3,'linear',$4,$5,$6,$7,$8,$9)`, hash, digest(browser), project, purpose, generation, current.app("linear").ClientID, expected, encrypted, time.Now().Add(10*time.Minute)); err != nil {
		return Authorization{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Authorization{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	link, _ := url.Parse(current.authorizeURL("linear", state, base64.RawURLEncoding.EncodeToString(challenge[:])))
	query := link.Query()
	query.Set("actor", "app")
	query.Set("scope", strings.Join(scopes, ","))
	link.RawQuery = query.Encode()
	return Authorization{URL: link.String(), State: state, Browser: browser}, nil
}

type linearActorIdentity struct {
	Organization struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		URLKey string `json:"urlKey"`
	} `json:"organization"`
	Viewer struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatarUrl"`
	} `json:"viewer"`
}

func (s *Service) linearActor(ctx context.Context, token string) (linearActorIdentity, error) {
	var identity linearActorIdentity
	err := s.linear(ctx, token, `query CircularAppIdentity { organization { id name urlKey } viewer { id name avatarUrl } }`, nil, &identity)
	if err != nil {
		return identity, err
	}
	if _, err := uuid.Parse(identity.Organization.ID); err != nil {
		return identity, ErrProvider
	}
	if _, err := uuid.Parse(identity.Viewer.ID); err != nil {
		return identity, ErrProvider
	}
	if identity.Organization.Name == "" || identity.Viewer.Name == "" {
		return identity, ErrProvider
	}
	return identity, nil
}

func (s *Service) completeLinearAppAuthorization(ctx context.Context, state, browser, code string) (string, error) {
	if len(state) != 43 || len(browser) != 43 || len(code) > 8192 {
		return "", ErrCallback
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "linear"); err != nil {
		return "", err
	}
	var project, purpose, clientID, expected string
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT s.project_id,s.purpose,s.app_client_id,s.expected_workspace,s.verifier FROM identity_oauth_states s JOIN identity_authorization_generations g ON g.project_id=s.project_id AND g.provider=s.provider AND g.generation=s.generation WHERE s.state_hash=$1 AND s.browser_hash=$2 AND s.expires_at>now() FOR UPDATE OF s,g`, digest(state), digest(browser)).Scan(&project, &purpose, &clientID, &expected, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCallback
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity_oauth_states WHERE state_hash=$1`, digest(state)); err != nil {
		return "", err
	}
	fail := func(problem error) (string, error) {
		if err := tx.Commit(ctx); err != nil {
			return project, err
		}
		return project, problem
	}
	current, err := s.resolveWith(ctx, "linear", tx)
	if err != nil {
		return fail(err)
	}
	if clientID != current.app("linear").ClientID {
		return fail(ErrCallback)
	}
	if code == "" {
		return fail(ErrDenied)
	}
	scopes, err := linearIdentityScopes(purpose)
	if err != nil {
		return fail(err)
	}
	verifier, err := s.open(encrypted, "identity-state:"+digest(state))
	if err != nil {
		return fail(ErrCallback)
	}
	token, err := current.token(ctx, "linear", code, string(verifier), "")
	if err != nil {
		return fail(err)
	}
	if !containsScopes(token.Scopes, scopes) {
		return fail(ErrAccess)
	}
	actor, err := current.linearActor(ctx, token.AccessToken)
	if err != nil {
		return fail(err)
	}
	if expected != "" && expected != actor.Organization.ID {
		return fail(ErrIdentityConflict)
	}
	// App lock serializes callbacks; row locking coordinates rotating refreshes.
	id := uuid.NewString()
	var priorActor string
	var priorScopes []string
	err = tx.QueryRow(ctx, `SELECT id,actor_id,granted_scopes FROM provider_identities WHERE provider='linear' AND app_client_id=$1 AND account_id=$2 FOR UPDATE`, clientID, actor.Organization.ID).Scan(&id, &priorActor, &priorScopes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if priorActor != "" && priorActor != actor.Viewer.ID {
		return fail(ErrIdentityConflict)
	}
	if !containsScopes(token.Scopes, priorScopes) {
		return fail(ErrAccess)
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT identity_id FROM integration_identity_bindings WHERE project_id=$1 AND provider='linear' FOR UPDATE`, project).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if previous != "" && previous != id {
		return fail(ErrIdentityConflict)
	}
	plain, _ := json.Marshal(token)
	ciphertext, err := s.seal(plain, "provider-identity:"+id)
	if err != nil {
		return fail(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,avatar_url,status,credentials,granted_scopes) VALUES($1,'linear',$2,$3,$4,$5,$6,$7,'available',$8,$9) ON CONFLICT(provider,app_client_id,account_id) DO UPDATE SET account_name=EXCLUDED.account_name,actor_name=EXCLUDED.actor_name,avatar_url=EXCLUDED.avatar_url,status='available',enabled=true,credentials=EXCLUDED.credentials,granted_scopes=EXCLUDED.granted_scopes,generation=provider_identities.generation+1,updated_at=now()`, id, clientID, actor.Organization.ID, actor.Organization.Name, actor.Viewer.ID, actor.Viewer.Name, actor.Viewer.AvatarURL, ciphertext, token.Scopes)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integration_identity_bindings(project_id,provider,identity_id) VALUES($1,'linear',$2) ON CONFLICT(project_id,provider) DO UPDATE SET enabled=true,updated_at=now()`, project, id); err != nil {
		return "", err
	}
	return project, tx.Commit(ctx)
}

func (s *Service) withLinearIdentity(ctx context.Context, identityID string, use func(string) error) error {
	return s.withSharedIdentityToken(ctx, identityID, "linear", use)
}
