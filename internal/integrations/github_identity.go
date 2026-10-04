package integrations

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PublisherIdentity is persisted before a publication can mutate its provider.
// Reconnects and project settings cannot change an in-flight publication author.
type PublisherIdentity struct {
	Mode        string `json:"mode"`
	IdentityID  string `json:"identity_id,omitempty"`
	Provider    string `json:"provider"`
	AccountID   string `json:"account_id"`
	ActorID     string `json:"actor_id"`
	AppClientID string `json:"app_client_id,omitempty"`
}

type GitHubPurpose string

const (
	GitHubClone       GitHubPurpose = "clone"
	GitHubPublish     GitHubPurpose = "publish"
	GitHubReviewRead  GitHubPurpose = "review_read"
	GitHubReviewWrite GitHubPurpose = "review_write"
)

type githubInstallationToken struct {
	Token     string
	ExpiresAt time.Time
}
type githubTokenCache struct {
	mu     sync.Mutex
	values map[string]githubInstallationToken
}

func parseGitHubKey(value []byte) (*rsa.PrivateKey, error) {
	if len(value) == 0 || len(value) > 64<<10 {
		return nil, ErrAppInput
	}
	block, rest := pem.Decode(value)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrAppInput
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else if block.Type == "PRIVATE KEY" {
		parsed, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, ErrAppInput
	}
	return key, nil
}
func githubJWT(key *rsa.PrivateKey, clientID string, now time.Time) (string, error) {
	claims, _ := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{clientID, now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", ErrConfiguration
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
func (s *Service) githubKey() (*rsa.PrivateKey, string, error) {
	value := []byte(s.config.githubPrivateKey)
	if s.config.GitHubPrivateKeyFile != "" {
		file, err := os.Open(s.config.GitHubPrivateKeyFile)
		if err != nil {
			return nil, "", ErrConfiguration
		}
		defer file.Close()
		value, err = io.ReadAll(io.LimitReader(file, (64<<10)+1))
		if err != nil {
			return nil, "", ErrConfiguration
		}
	}
	key, err := parseGitHubKey(value)
	if err != nil {
		return nil, "", ErrIdentityUnavailable
	}
	sum := sha256.Sum256(value)
	return key, hex.EncodeToString(sum[:]), nil
}

type githubAppInfo struct {
	ID       int64  `json:"id"`
	ClientID string `json:"client_id"`
	Slug     string `json:"slug"`
}
type githubBotInfo struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}
type githubInstallationInfo struct {
	ID          int64   `json:"id"`
	AppID       int64   `json:"app_id"`
	SuspendedAt *string `json:"suspended_at"`
	Account     struct {
		Login string `json:"login"`
	} `json:"account"`
	Permissions map[string]string `json:"permissions"`
}

func (s *Service) SaveGitHubIdentity(ctx context.Context, project string, value []byte) (IdentityStatus, error) {
	current, err := s.resolve(ctx, "github")
	if err != nil {
		return IdentityStatus{}, err
	}
	if !current.Configured("github") {
		return IdentityStatus{}, ErrConfiguration
	}
	if s.environmentApp("github") && len(value) > 0 {
		return IdentityStatus{}, ErrAppConfigured
	}
	var key *rsa.PrivateKey
	if len(value) > 0 {
		key, err = parseGitHubKey(value)
	} else {
		key, _, err = current.githubKey()
	}
	if err != nil {
		return IdentityStatus{}, err
	}
	jwt, err := githubJWT(key, current.app("github").ClientID, time.Now())
	if err != nil {
		return IdentityStatus{}, err
	}
	var app githubAppInfo
	if err := current.github(ctx, jwt, "/app", &app); err != nil {
		return IdentityStatus{}, err
	}
	if app.ID <= 0 || app.ClientID != current.app("github").ClientID || !appSlug.MatchString(app.Slug) {
		return IdentityStatus{}, ErrAppIdentity
	}
	var bot githubBotInfo
	if err := current.github(ctx, "", "/users/"+url.PathEscape(app.Slug+"[bot]"), &bot); err != nil {
		return IdentityStatus{}, err
	}
	if bot.ID <= 0 || bot.Login != app.Slug+"[bot]" {
		return IdentityStatus{}, ErrProvider
	}
	// Prefer the imported installation. Without repositories, the connected
	// user's sole installation is unambiguous; multiple accounts require a choice.
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT external_refs->'github'->>'installation_id' FROM repositories WHERE project_id=$1 AND external_refs->'github'->>'installation_id' IS NOT NULL`, project)
	if err != nil {
		return IdentityStatus{}, err
	}
	installations := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return IdentityStatus{}, err
		}
		installations = append(installations, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return IdentityStatus{}, err
	}
	if len(installations) == 0 {
		page, err := s.GitHubInstallations(ctx, project, 1)
		if err != nil {
			return IdentityStatus{}, err
		}
		if page.NextPage != 0 {
			return IdentityStatus{}, ErrIdentityConflict
		}
		for _, item := range page.Items {
			installations = append(installations, item.ID)
		}
	}
	if len(installations) != 1 || !positiveNumber(installations[0]) {
		return IdentityStatus{}, ErrIdentityConflict
	}
	installationID := installations[0]
	var installation githubInstallationInfo
	if err := current.github(ctx, jwt, "/app/installations/"+installationID, &installation); err != nil {
		return IdentityStatus{}, err
	}
	if strconv.FormatInt(installation.ID, 10) != installationID || installation.AppID != app.ID || installation.SuspendedAt != nil {
		return IdentityStatus{}, ErrAccess
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IdentityStatus{}, err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "github"); err != nil {
		return IdentityStatus{}, err
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT i.account_id FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id WHERE b.project_id=$1 AND b.provider='github' FOR UPDATE OF b`, project).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return IdentityStatus{}, err
	}
	if previous != "" && previous != installationID {
		return IdentityStatus{}, ErrIdentityConflict
	}
	if !s.environmentApp("github") {
		var encrypted []byte
		if err := tx.QueryRow(ctx, `SELECT credentials FROM integration_apps WHERE provider='github' FOR UPDATE`).Scan(&encrypted); err != nil {
			return IdentityStatus{}, err
		}
		plain, err := s.open(encrypted, "provider-app:github")
		if err != nil {
			return IdentityStatus{}, err
		}
		var saved registeredApp
		if json.Unmarshal(plain, &saved) != nil || saved.ClientID != app.ClientID {
			return IdentityStatus{}, ErrAppIdentity
		}
		if len(value) > 0 {
			saved.PrivateKey = string(value)
		}
		saved.AppID = app.ID
		saved.Slug = app.Slug
		plain, _ = json.Marshal(saved)
		encrypted, err = s.seal(plain, "provider-app:github")
		if err != nil {
			return IdentityStatus{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE integration_apps SET credentials=$1,updated_at=now() WHERE provider='github'`, encrypted); err != nil {
			return IdentityStatus{}, err
		}
	}
	var identityID string
	err = tx.QueryRow(ctx, `INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,actor_login,avatar_url,status,granted_scopes)
	 VALUES($1,'github',$2,$3,$4,$5,$6,$7,$8,'available',$9) ON CONFLICT(provider,app_client_id,account_id) DO UPDATE SET account_name=EXCLUDED.account_name,actor_id=EXCLUDED.actor_id,actor_name=EXCLUDED.actor_name,actor_login=EXCLUDED.actor_login,avatar_url=EXCLUDED.avatar_url,status='available',enabled=true,generation=provider_identities.generation+1,updated_at=now() RETURNING id`, uuid.NewString(), app.ClientID, installationID, installation.Account.Login, strconv.FormatInt(bot.ID, 10), bot.Name, bot.Login, bot.AvatarURL, []string{"contents:read", "pull_requests:write"}).Scan(&identityID)
	if err != nil {
		return IdentityStatus{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integration_identity_bindings(project_id,provider,identity_id) VALUES($1,'github',$2) ON CONFLICT(project_id,provider) DO UPDATE SET enabled=true,updated_at=now()`, project, identityID); err != nil {
		return IdentityStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityStatus{}, err
	}
	return s.Identity(ctx, project, "github")
}

func (s *Service) withGitHubRepository(ctx context.Context, project, repositoryID string, purpose GitHubPurpose, use func(string, PublisherIdentity) error) error {
	bound, err := s.hasIdentityBinding(ctx, project, "github")
	if err != nil {
		return err
	}
	if !bound {
		return s.withToken(ctx, project, "github", func(token string) error { return use(token, PublisherIdentity{Mode: "user", Provider: "github"}) })
	}
	identity, err := s.Identity(ctx, project, "github")
	if err != nil {
		return err
	}
	if identity.Status != "enabled" {
		return ErrIdentityUnavailable
	}
	var installationID, providerRepo string
	err = s.pool.QueryRow(ctx, `SELECT external_refs->'github'->>'installation_id',external_refs->'github'->>'repository_id' FROM repositories WHERE id=$1 AND project_id=$2`, repositoryID, project).Scan(&installationID, &providerRepo)
	if err != nil || installationID != identity.AccountID || !positiveNumber(providerRepo) {
		return ErrAccess
	}
	current, err := s.resolve(ctx, "github")
	if err != nil {
		return err
	}
	key, fingerprint, err := current.githubKey()
	if err != nil {
		return err
	}
	permissions := map[string]string{"metadata": "read", "contents": "read"}
	switch purpose {
	case GitHubClone:
	case GitHubPublish:
		permissions["contents"] = "write"
		permissions["pull_requests"] = "write"
	case GitHubReviewRead:
		permissions["pull_requests"] = "read"
	case GitHubReviewWrite:
		permissions["pull_requests"] = "write"
	default:
		return ErrAppInput
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM provider_identities WHERE id=$1 AND enabled AND status='available'`, identity.IdentityID).Scan(&generation); err != nil {
		return ErrIdentityUnavailable
	}
	cacheKey := fingerprint + ":" + strconv.FormatInt(generation, 10) + ":" + installationID + ":" + providerRepo + ":" + string(purpose)
	token, err := current.installationToken(ctx, key, cacheKey, installationID, providerRepo, permissions)
	if err != nil {
		return err
	}
	err = use(token, PublisherIdentity{Mode: "app", IdentityID: identity.IdentityID, Provider: "github", AccountID: installationID, ActorID: identity.ActorID, AppClientID: current.app("github").ClientID})
	if errors.Is(err, ErrAccess) || errors.Is(err, ErrReconnect) {
		s.githubTokens.mu.Lock()
		delete(s.githubTokens.values, cacheKey)
		s.githubTokens.mu.Unlock()
	}
	return err
}

func (s *Service) installationToken(ctx context.Context, key *rsa.PrivateKey, cacheKey, installationID, repositoryID string, permissions map[string]string) (string, error) {
	s.githubTokens.mu.Lock()
	defer s.githubTokens.mu.Unlock()
	if cached, ok := s.githubTokens.values[cacheKey]; ok && time.Until(cached.ExpiresAt) > time.Minute {
		return cached.Token, nil
	}
	jwt, err := githubJWT(key, s.app("github").ClientID, time.Now())
	if err != nil {
		return "", err
	}
	var installation githubInstallationInfo
	if err := s.github(ctx, jwt, "/app/installations/"+installationID, &installation); err != nil {
		return "", err
	}
	if strconv.FormatInt(installation.ID, 10) != installationID || installation.SuspendedAt != nil {
		return "", ErrAccess
	}
	for name, wanted := range permissions {
		actual := installation.Permissions[name]
		if actual != wanted && !(wanted == "read" && actual == "write") {
			return "", ErrAccess
		}
	}
	repo, _ := strconv.ParseInt(repositoryID, 10, 64)
	body, _ := json.Marshal(map[string]any{"repository_ids": []int64{repo}, "permissions": permissions})
	req, err := http.NewRequestWithContext(ctx, "POST", s.config.GitHubAPIURL+"/app/installations/"+installationID+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	var response struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	if err := s.do(req, &response); err != nil {
		return "", err
	}
	if !safeToken(response.Token) || time.Until(response.ExpiresAt) < time.Minute {
		return "", ErrProvider
	}
	if len(response.Permissions) != len(permissions) {
		return "", ErrAccess
	}
	for name, wanted := range permissions {
		if response.Permissions[name] != wanted {
			return "", ErrAccess
		}
	}
	if len(response.Repositories) > 0 && (len(response.Repositories) != 1 || response.Repositories[0].ID != repo) {
		return "", ErrAccess
	}
	// Bound memory even when repositories or signing keys change frequently.
	for k, v := range s.githubTokens.values {
		if time.Until(v.ExpiresAt) < time.Minute {
			delete(s.githubTokens.values, k)
		}
	}
	if len(s.githubTokens.values) > 1024 {
		clear(s.githubTokens.values)
	}
	s.githubTokens.values[cacheKey] = githubInstallationToken{response.Token, response.ExpiresAt}
	return response.Token, nil
}
