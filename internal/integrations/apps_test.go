package integrations_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func setupApps(t *testing.T) (fixture, integrations.Config) {
	t.Helper()
	pool := testsupport.Database(t)
	provider := testsupport.NewProviderFixture()
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	config := integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), GitHubURL: server.URL, GitHubAPIURL: server.URL, LinearURL: server.URL, LinearAPIURL: server.URL}
	service, err := integrations.New(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'App setup')`, project); err != nil {
		t.Fatal(err)
	}
	return fixture{pool: pool, service: service, provider: provider, project: project}, config
}

func TestGitHubManifestRegistrationPersistsAndSupportsIndependentWorker(t *testing.T) {
	f, config := setupApps(t)
	before, err := f.service.Connections(t.Context(), f.project)
	if err != nil || before[0].Configured || !before[0].SetupAvailable {
		t.Fatal("console setup unavailable", err)
	}
	registration, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "circular-test")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(registration.URL)
	if u.Path != "/organizations/circular-test/settings/apps/new" || u.Query().Get("state") != registration.State {
		t.Fatal("wrong app registration destination")
	}
	var manifest struct {
		Redirect    string            `json:"redirect_url"`
		Callbacks   []string          `json:"callback_urls"`
		Permissions map[string]string `json:"default_permissions"`
		Public      bool              `json:"public"`
		Hook        json.RawMessage   `json:"hook_attributes"`
	}
	if json.Unmarshal([]byte(registration.Manifest), &manifest) != nil || manifest.Redirect != f.service.RegistrationCallbackURL() || len(manifest.Callbacks) != 1 || manifest.Callbacks[0] != f.service.CallbackURL("github") || manifest.Public || len(manifest.Hook) != 0 || manifest.Permissions["contents"] != "write" || manifest.Permissions["administration"] != "write" || manifest.Permissions["pull_requests"] != "write" || len(manifest.Permissions) != 4 {
		t.Fatal("incorrect registration defaults")
	}
	code, err := f.provider.ManifestCode(registration.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, strings.Repeat("x", 43), code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("foreign browser accepted", err)
	}
	if f.provider.Conversions.Load() != 0 {
		t.Fatal("unbound callback exchanged a manifest code")
	}
	project, install, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, registration.Browser, code)
	if err != nil || project != f.project || install != config.GitHubURL+"/apps/circular-fixture/installations/new" {
		t.Fatal("registration failed", err)
	}
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, registration.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("manifest callback replay accepted", err)
	}
	if _, err := f.service.BeginGitHubRegistration(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrAppConfigured) {
		t.Fatal("registered app could be overwritten", err)
	}
	var stored []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integration_apps WHERE provider='github'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("fixture-github-secret")) {
		t.Fatal("app secret was stored in plaintext")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	plain, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte("provider-app:github"))
	if err != nil || !bytes.Contains(plain, []byte("fixture-github-secret")) || !bytes.Contains(plain, []byte("pem")) || !bytes.Contains(plain, []byte("webhook_secret")) || !bytes.Contains(plain, []byte("PRIVATE KEY")) {
		t.Fatal("app credential storage did not retain encrypted signing material", err)
	}
	worker, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	connections, err := worker.Connections(t.Context(), f.project)
	if err != nil || !connections[0].Configured || connections[0].InstallationURL != install {
		t.Fatal("fresh process did not observe registered app", err)
	}
	public, _ := json.Marshal(connections)
	if bytes.Contains(public, []byte("secret")) || bytes.Contains(public, []byte("credentials")) {
		t.Fatal("public status exposed app credentials")
	}
	f.connect(t, "github")
	repository, _, err := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := worker.GitCredential(t.Context(), uuid.MustParse(repository), "https://github.com/fixture/private-source.git")
	if err != nil || !strings.HasPrefix(credential, "ghu_") {
		t.Fatal("worker could not use console-registered app", err)
	}
}

func TestGitHubRegistrationRejectsExpiredSupersededAndExpandedPermissions(t *testing.T) {
	f, _ := setupApps(t)
	if _, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "../other"); !errors.Is(err, integrations.ErrAppInput) {
		t.Fatal("invalid organization accepted", err)
	}
	old, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := f.provider.ManifestCode(old.Manifest)
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), old.State, old.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("superseded registration accepted", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE integration_app_states SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), newer.State, newer.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("expired registration accepted", err)
	}
	if f.provider.Conversions.Load() != 0 {
		t.Fatal("invalid registration reached provider")
	}
	registration, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	code, _ = f.provider.ManifestCode(`{"default_permissions":{"contents":"read","issues":"write"}}`)
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, registration.Browser, code); !errors.Is(err, integrations.ErrAccess) {
		t.Fatal("unexpected app permissions accepted", err)
	}
	connections, err := f.service.Connections(t.Context(), f.project)
	if err != nil || connections[0].Configured {
		t.Fatal("invalid app persisted", err)
	}
}

func TestGitHubAppRenamePreservesConnectionsAndPendingSignIn(t *testing.T) {
	f, config := setupApps(t)
	registration, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := f.provider.ManifestCode(registration.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, registration.Browser, code); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SaveLinearApp(t.Context(), "fixture-linear"); err != nil {
		t.Fatal(err)
	}
	f.connect(t, "github")
	f.connect(t, "linear")
	pending, pendingCode := f.start(t, "github")
	state := func() string {
		t.Helper()
		var value string
		if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(i) ORDER BY provider)::text FROM integrations i WHERE project_id=$1`, f.project).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	installed, err := f.service.GitHubInstallations(t.Context(), f.project, 1)
	if err != nil || len(installed.Items) != 1 || installed.Items[0].ManagementURL != config.GitHubURL+"/settings/installations/101" {
		t.Fatal("installation settings link missing", err)
	}
	f.provider.RenameGitHubApp("renamed-circular-fixture")
	for _, invalid := range []string{"", "https://untrusted.test/apps/renamed-circular-fixture", config.GitHubURL + "/apps/renamed-circular-fixture?next=other", "../renamed-circular-fixture"} {
		if err := f.service.SaveGitHubAppURL(t.Context(), f.project, invalid); !errors.Is(err, integrations.ErrAppInput) {
			t.Fatal("invalid app URL accepted", err)
		}
	}
	if err := f.service.SaveGitHubAppURL(t.Context(), f.project, "another-fixture-app"); !errors.Is(err, integrations.ErrAppIdentity) {
		t.Fatal("different app identity accepted", err)
	}
	unchanged, err := f.service.Connections(t.Context(), f.project)
	if err != nil || unchanged[0].InstallationURL != config.GitHubURL+"/apps/circular-fixture/installations/new" {
		t.Fatal("rejected URL changed app", err)
	}
	if err := f.service.SaveGitHubAppURL(t.Context(), f.project, config.GitHubURL+"/apps/renamed-circular-fixture/installations/new/"); err != nil {
		t.Fatal(err)
	}
	if state() != before {
		t.Fatal("app rename changed account credentials or authorization state")
	}
	fresh, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	connections, err := fresh.Connections(t.Context(), f.project)
	if err != nil || connections[0].InstallationURL != config.GitHubURL+"/apps/renamed-circular-fixture/installations/new" || !connections[0].AppEditable {
		t.Fatal("fresh process retained the old app URL", err)
	}
	for _, connection := range connections {
		if connection.Status != "connected" {
			t.Fatal("app rename disconnected an account")
		}
	}
	after, err := fresh.GitHubInstallations(t.Context(), f.project, 1)
	if err != nil || len(after.Items) != 1 || after.Items[0].ManagementURL != installed.Items[0].ManagementURL {
		t.Fatal("rename changed installation management URL", err)
	}
	if _, err := fresh.Complete(t.Context(), "github", pending.State, pending.Browser, pendingCode); err != nil {
		t.Fatal("rename invalidated pending sign-in", err)
	}
	if _, _, err := fresh.ImportGitHub(t.Context(), f.project, "101", "202", 1); err != nil {
		t.Fatal("renamed app could not import repositories", err)
	}
}

func TestGitHubEnvironmentAppURLCannotBeOverwritten(t *testing.T) {
	f := setup(t)
	if err := f.service.SaveGitHubAppURL(t.Context(), f.project, "renamed-circular-fixture"); !errors.Is(err, integrations.ErrAppConfigured) {
		t.Fatal("environment app settings overwritten", err)
	}
}

func TestLinearPublicClientSetupRefreshAndReplacementRules(t *testing.T) {
	f, config := setupApps(t)
	registration, _ := url.Parse(f.service.LinearRegistrationURL())
	if registration.Path != "/settings/api/applications/new" || registration.Query().Get("oauth.redirect_uris") != f.service.CallbackURL("linear") || registration.Query().Get("distribution") != "private" || registration.Query().Get("webhook.enabled") != "false" {
		t.Fatal("incorrect Linear app registration defaults")
	}
	if registration.Query().Has("oauth.client_uri") {
		t.Fatal("private app prefilled Developer URL; Linear rejects the localhost homepage")
	}
	for _, value := range []string{"", "id with spaces", "https://untrusted.test", strings.Repeat("a", 201)} {
		if err := f.service.SaveLinearApp(t.Context(), value); !errors.Is(err, integrations.ErrAppInput) {
			t.Fatal("invalid Client ID accepted", err)
		}
	}
	if err := f.service.SaveLinearApp(t.Context(), "fixture-linear"); err != nil {
		t.Fatal(err)
	}
	pending, code := f.start(t, "linear")
	if err := f.service.SaveLinearApp(t.Context(), "corrected-client-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Complete(t.Context(), "linear", pending.State, pending.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("changed app accepted old callback", err)
	}
	if err := f.service.SaveLinearApp(t.Context(), "fixture-linear"); err != nil {
		t.Fatal(err)
	}
	f.connect(t, "linear")
	if err := f.service.SaveLinearApp(t.Context(), "another-client-id"); !errors.Is(err, integrations.ErrAppInUse) {
		t.Fatal("active app replaced", err)
	}
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Other project')`, other); err != nil {
		t.Fatal(err)
	}
	connections, err := f.service.Connections(t.Context(), other)
	if err != nil || !connections[1].Configured || connections[1].AppEditable || connections[1].AppClientID != "fixture-linear" {
		t.Fatal("app availability ignored another project's connection", err)
	}
	// Force access-token expiry, then use a fresh service to exercise client-only refresh.
	var id string
	var stored []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT id,credentials FROM integrations WHERE project_id=$1 AND provider='linear'`, f.project).Scan(&id, &stored); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	plain, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	var token map[string]any
	if err := json.Unmarshal(plain, &token); err != nil {
		t.Fatal(err)
	}
	token["expires_at"] = time.Now().Add(-time.Minute)
	plain, _ = json.Marshal(token)
	nonce := make([]byte, vault.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET credentials=$2 WHERE id=$1`, id, vault.Seal(nonce, nonce, plain, []byte(id))); err != nil {
		t.Fatal(err)
	}
	fresh, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.LinearTeams(t.Context(), f.project, ""); err != nil || f.provider.Refreshes.Load() != 1 {
		t.Fatal("public client refresh failed", err)
	}
	if revoked, err := fresh.Disconnect(t.Context(), f.project, "linear"); err != nil || !revoked {
		t.Fatal("public client disconnect failed", err)
	}
	if err := fresh.SaveLinearApp(t.Context(), "another-client-id"); err != nil {
		t.Fatal("disconnected app could not be corrected", err)
	}
}

func TestEnvironmentAppsCannotBeReplacedFromConsole(t *testing.T) {
	f := setup(t)
	if err := f.service.SaveLinearApp(t.Context(), "replacement"); !errors.Is(err, integrations.ErrAppConfigured) {
		t.Fatal("environment app overwritten", err)
	}
	if _, err := f.service.BeginGitHubRegistration(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrAppConfigured) {
		t.Fatal("environment app overwritten", err)
	}
}
