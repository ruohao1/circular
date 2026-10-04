package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var appSlug = regexp.MustCompile(`^[a-z0-9-]{1,100}$`)
var organizationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
var clientIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}$`)

// registeredApp is serialized only inside the encrypted app store. Never return
// it from an HTTP handler; public metadata contains no client secret.
type registeredApp struct {
	AppID         int64  `json:"app_id,omitempty"`
	PrivateKey    string `json:"pem,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Slug          string `json:"slug"`
}

func (s *Service) environmentApp(provider string) bool {
	app := s.app(provider)
	return app.ClientID != "" || app.ClientSecret != ""
}

// Each operation resolves its app from the shared store. API and worker observe
// console registration without a restart or a process-local credential cache.
func (s *Service) resolve(ctx context.Context, provider string) (*Service, error) {
	return s.resolveWith(ctx, provider, s.pool)
}

func (s *Service) resolveWith(ctx context.Context, provider string, reader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (*Service, error) {
	if s.resolvedProvider == provider || s.environmentApp(provider) || s.vault == nil {
		return s, nil
	}
	var encrypted []byte
	err := reader.QueryRow(ctx, `SELECT credentials FROM integration_apps WHERE provider=$1`, provider).Scan(&encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.open(encrypted, "provider-app:"+provider)
	if err != nil {
		return nil, ErrConfiguration
	}
	var app registeredApp
	if json.Unmarshal(plain, &app) != nil || !clientIdentifier.MatchString(app.ClientID) || (provider == "github" && (!safeToken(app.ClientSecret) || !appSlug.MatchString(app.Slug))) {
		return nil, ErrConfiguration
	}
	resolved := *s
	resolved.resolvedProvider = provider
	if provider == "github" {
		resolved.config.GitHub = OAuthApp{ClientID: app.ClientID, ClientSecret: app.ClientSecret}
		resolved.config.GitHubAppSlug = app.Slug
		resolved.config.githubPrivateKey = app.PrivateKey
	} else {
		resolved.config.Linear = OAuthApp{ClientID: app.ClientID, ClientSecret: app.ClientSecret}
	}
	return &resolved, nil
}

func (s *Service) installationURL() string {
	if !appSlug.MatchString(s.config.GitHubAppSlug) {
		return ""
	}
	return s.config.GitHubURL + "/apps/" + s.config.GitHubAppSlug + "/installations/new"
}

// Updating an app's public URL never changes its OAuth identity or account tokens.
// Verify the URL with GitHub before changing the stored slug.
func (s *Service) SaveGitHubAppURL(ctx context.Context, project, value string) error {
	if s.environmentApp("github") {
		return ErrAppConfigured
	}
	current, err := s.resolve(ctx, "github")
	if err != nil {
		return err
	}
	if !current.Configured("github") {
		return ErrConfiguration
	}
	slug := strings.ToLower(strings.TrimSpace(value))
	if !appSlug.MatchString(slug) {
		u, err := url.Parse(slug)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme+"://"+u.Host != s.config.GitHubURL {
			return ErrAppInput
		}
		path := strings.TrimSuffix(u.Path, "/")
		path = strings.TrimSuffix(path, "/installations/new")
		if !strings.HasPrefix(path, "/apps/") {
			return ErrAppInput
		}
		slug = strings.TrimPrefix(path, "/apps/")
		if !appSlug.MatchString(slug) {
			return ErrAppInput
		}
	}
	var app struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
		Slug     string `json:"slug"`
	}
	lookup := func(token string) error { return current.github(ctx, token, "/apps/"+slug, &app) }
	err = current.withToken(ctx, project, "github", lookup)
	if errors.Is(err, ErrReconnect) {
		err = lookup("")
	}
	if err != nil {
		return err
	}
	if app.ID <= 0 || !appSlug.MatchString(app.Slug) {
		return ErrProvider
	}
	if app.ClientID != current.config.GitHub.ClientID {
		return ErrAppIdentity
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "github"); err != nil {
		return err
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `SELECT credentials FROM integration_apps WHERE provider='github' FOR UPDATE`).Scan(&stored); err != nil {
		return err
	}
	plain, err := s.open(stored, "provider-app:github")
	if err != nil {
		return ErrConfiguration
	}
	var saved registeredApp
	if json.Unmarshal(plain, &saved) != nil || saved.ClientID != app.ClientID {
		return ErrAppIdentity
	}
	saved.Slug = app.Slug
	plain, _ = json.Marshal(saved)
	updated, err := s.seal(plain, "provider-app:github")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE integration_apps SET credentials=$1,updated_at=now() WHERE provider='github'`, updated); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) LinearRegistrationURL() string {
	// Developer URL is optional in Linear's private-app form. Leave it unset:
	// the homepage validator rejects localhost even though OAuth callbacks allow it.
	values := url.Values{
		"distribution": {"private"}, "developer.name": {"Circular"},
		"oauth.client_name":   {"Circular"},
		"oauth.redirect_uris": {s.CallbackURL("linear")}, "oauth.grant_types": {"authorization_code"},
		"display.description": {"Import issues into Circular tasks."}, "webhook.enabled": {"false"},
	}
	return s.config.LinearURL + "/settings/api/applications/new?" + values.Encode()
}

func lockApp(ctx context.Context, tx pgx.Tx, provider string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('circular-app:'||current_schema()||':'||$1,0))`, provider)
	return err
}

// Linear supports PKCE with a public client ID. The first-time console form never
// asks for a client secret. Existing connected accounts prevent app replacement.
func (s *Service) SaveLinearApp(ctx context.Context, clientID string) error {
	if !clientIdentifier.MatchString(clientID) {
		return ErrAppInput
	}
	if s.vault == nil {
		return ErrConfiguration
	}
	if s.environmentApp("linear") {
		return ErrAppConfigured
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "linear"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM integrations WHERE provider='linear' ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integrations WHERE provider='linear' AND enabled=true) OR EXISTS(SELECT 1 FROM provider_identities WHERE provider='linear' AND enabled)`).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrAppInUse
	}
	plain, _ := json.Marshal(registeredApp{ClientID: clientID})
	encrypted, err := s.seal(plain, "provider-app:linear")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integration_apps(provider,credentials) VALUES('linear',$1) ON CONFLICT(provider) DO UPDATE SET credentials=EXCLUDED.credentials,updated_at=now()`, encrypted); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM integration_oauth_states WHERE integration_id IN (SELECT id FROM integrations WHERE provider='linear')`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE integrations SET auth_generation=auth_generation+1 WHERE provider='linear'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type AppRegistration struct {
	URL      string `json:"registration_url"`
	Manifest string `json:"manifest"`
	State    string `json:"-"`
	Browser  string `json:"-"`
}

func (s *Service) RegistrationCallbackURL() string {
	return s.APIURL() + "/api/v1/integrations/github/registration-callback"
}

func (s *Service) BeginGitHubRegistration(ctx context.Context, project, organization string) (AppRegistration, error) {
	if s.vault == nil {
		return AppRegistration{}, ErrConfiguration
	}
	if organization != "" && !organizationName.MatchString(organization) {
		return AppRegistration{}, ErrAppInput
	}
	current, err := s.resolve(ctx, "github")
	if err != nil {
		return AppRegistration{}, err
	}
	if current.environmentApp("github") {
		return AppRegistration{}, ErrAppConfigured
	}
	state, browser := randomValue(), randomValue()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AppRegistration{}, err
	}
	defer rollback(ctx, tx)
	if _, err := tx.Exec(ctx, `DELETE FROM integration_app_states WHERE expires_at<now() OR project_id=$1`, project); err != nil {
		return AppRegistration{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integration_app_states(state_hash,browser_hash,project_id,expires_at) VALUES($1,$2,$3,$4)`, digest(state), digest(browser), project, time.Now().Add(10*time.Minute)); err != nil {
		return AppRegistration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppRegistration{}, err
	}
	// This integration uses OAuth and provider reads, without a webhook receiver.
	// Omit hook_attributes entirely: GitHub rejects a localhost hook URL even
	// when active=false. Browser redirects may still return to localhost.
	manifest, _ := json.Marshal(map[string]any{
		"name": "Circular " + state[:8], "url": s.WebURL(),
		"description":  "Read selected repositories for Circular coding tasks.",
		"redirect_url": s.RegistrationCallbackURL(), "callback_urls": []string{s.CallbackURL("github")},
		"setup_url": s.WebURL() + "/setup?section=integrations",
		"public":    false, "request_oauth_on_install": false, "default_permissions": map[string]string{"contents": "write", "metadata": "read", "administration": "write", "pull_requests": "write"}, "default_events": []string{},
	})
	path := "/settings/apps/new"
	if organization != "" {
		path = "/organizations/" + organization + "/settings/apps/new"
	}
	return AppRegistration{URL: s.config.GitHubURL + path + "?state=" + url.QueryEscape(state), Manifest: string(manifest), State: state, Browser: browser}, nil
}

// The browser-bound, single-use manifest exchange retains app signing material
// in the encrypted store. App identity still requires explicit project enablement.
func (s *Service) CompleteGitHubRegistration(ctx context.Context, state, browser, code string) (string, string, error) {
	if s.vault == nil {
		return "", "", ErrConfiguration
	}
	if len(state) != 43 || len(browser) != 43 || !safeToken(code) || len(code) > 200 {
		return "", "", ErrCallback
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, "github"); err != nil {
		return "", "", err
	}
	var project string
	err = tx.QueryRow(ctx, `SELECT project_id FROM integration_app_states WHERE state_hash=$1 AND browser_hash=$2 AND expires_at>now() FOR UPDATE`, digest(state), digest(browser)).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrCallback
	}
	if err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM integration_app_states WHERE state_hash=$1`, digest(state)); err != nil {
		return project, "", err
	}
	fail := func(problem error) (string, string, error) {
		if err := tx.Commit(ctx); err != nil {
			return project, "", err
		}
		return project, "", problem
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integration_apps WHERE provider='github')`).Scan(&exists); err != nil {
		return project, "", err
	}
	if exists || s.environmentApp("github") {
		return fail(ErrAppConfigured)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", s.config.GitHubAPIURL+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return fail(ErrProvider)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	var response struct {
		PrivateKey    string            `json:"pem"`
		WebhookSecret string            `json:"webhook_secret"`
		ID            int64             `json:"id"`
		ClientID      string            `json:"client_id"`
		ClientSecret  string            `json:"client_secret"`
		Slug          string            `json:"slug"`
		Permissions   map[string]string `json:"permissions"`
	}
	if err := s.do(request, &response); err != nil {
		return fail(err)
	}
	if response.ID <= 0 || !clientIdentifier.MatchString(response.ClientID) || !safeToken(response.ClientSecret) || !appSlug.MatchString(response.Slug) {
		return fail(ErrProvider)
	}
	for name, permission := range response.Permissions {
		if !(name == "metadata" && permission == "read" || (name == "contents" || name == "administration" || name == "pull_requests") && permission == "write") {
			return fail(ErrAccess)
		}
	}
	if response.Permissions["contents"] != "write" || response.Permissions["administration"] != "write" || response.Permissions["pull_requests"] != "write" {
		return fail(ErrAccess)
	}
	if _, err := parseGitHubKey([]byte(response.PrivateKey)); err != nil {
		return fail(ErrProvider)
	}
	plain, _ := json.Marshal(registeredApp{ClientID: response.ClientID, ClientSecret: response.ClientSecret, Slug: response.Slug, AppID: response.ID, PrivateKey: response.PrivateKey, WebhookSecret: response.WebhookSecret})
	encrypted, err := s.seal(plain, "provider-app:github")
	if err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO integration_apps(provider,credentials) VALUES('github',$1)`, encrypted); err != nil {
		return project, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return project, "", err
	}
	return project, s.config.GitHubURL + "/apps/" + response.Slug + "/installations/new", nil
}
