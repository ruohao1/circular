package integrations_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

type fixture struct {
	pool     *pgxpool.Pool
	service  *integrations.Service
	provider *testsupport.ProviderFixture
	project  string
	root     string
	config   integrations.Config
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testsupport.Database(t)
	provider := testsupport.NewProviderFixture()
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	config := integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), GitHub: integrations.OAuthApp{"fixture-github", "fixture-github-secret"}, Linear: integrations.OAuthApp{"fixture-linear", "fixture-linear-secret"}, GitHubURL: server.URL, GitHubAPIURL: server.URL, LinearURL: server.URL, LinearAPIURL: server.URL}
	root := t.TempDir()
	config.ArtifactRoot = root
	service, err := integrations.New(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Connections')`, project); err != nil {
		t.Fatal(err)
	}
	return fixture{pool, service, provider, project, root, config}
}

func (f fixture) start(t *testing.T, provider string) (integrations.Authorization, string) {
	t.Helper()
	auth, err := f.service.Begin(t.Context(), f.project, provider)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil || callback.Query().Get("state") != auth.State {
		t.Fatal("provider did not preserve state")
	}
	return auth, callback.Query().Get("code")
}

func (f fixture) connect(t *testing.T, provider string) {
	t.Helper()
	auth, code := f.start(t, provider)
	project, err := f.service.Complete(t.Context(), provider, auth.State, auth.Browser, code)
	if err != nil || project != f.project {
		t.Fatal("authorization failed", err)
	}
}

func TestCallbackBindingReplayAndDisconnect(t *testing.T) {
	f := setup(t)
	auth, code := f.start(t, "github")
	for _, test := range []struct{ provider, state, browser string }{{"linear", auth.State, auth.Browser}, {"github", auth.State, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))}, {"github", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32)), auth.Browser}} {
		if _, err := f.service.Complete(t.Context(), test.provider, test.state, test.browser, code); !errors.Is(err, integrations.ErrCallback) {
			t.Fatal("unbound callback accepted", err)
		}
	}
	if f.provider.Exchanges.Load() != 0 {
		t.Fatal("invalid callbacks reached the token endpoint")
	}
	if _, err := f.service.Complete(t.Context(), "github", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Complete(t.Context(), "github", auth.State, auth.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("replayed callback accepted")
	}
	old, oldCode := f.start(t, "github")
	if revoked, err := f.service.Disconnect(t.Context(), f.project, "github"); err != nil || !revoked {
		t.Fatal("disconnect failed", err)
	}
	if _, err := f.service.Complete(t.Context(), "github", old.State, old.Browser, oldCode); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("callback revived a disconnected connection")
	}
	connections, err := f.service.Connections(t.Context(), f.project)
	if err != nil || connections[0].Status != "disconnected" {
		t.Fatal("connection remained enabled", err)
	}
}

func TestExpiredAndSupersededAuthorization(t *testing.T) {
	f := setup(t)
	old, code := f.start(t, "linear")
	f.start(t, "linear")
	if _, err := f.service.Complete(t.Context(), "linear", old.State, old.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("superseded callback accepted")
	}
	auth, code := f.start(t, "linear")
	_, err := f.pool.Exec(t.Context(), `UPDATE integration_oauth_states SET expires_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("expired callback accepted")
	}
}

func TestEncryptedCredentialsRefreshOnceAcrossConcurrentRequests(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	var id string
	var stored []byte
	err := f.pool.QueryRow(t.Context(), `SELECT id,credentials FROM integrations WHERE project_id=$1 AND provider='github'`, f.project).Scan(&id, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("access_token")) || bytes.Contains(stored, []byte("ghu_")) {
		t.Fatal("plaintext credential stored")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	plain, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte(uuid.NewString())); err == nil {
		t.Fatal("ciphertext was not bound to its connection")
	}
	var token struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(plain, &token)
	token.ExpiresAt = time.Now().Add(-time.Minute)
	plain, _ = json.Marshal(token)
	nonce := make([]byte, vault.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	expired := vault.Seal(nonce, nonce, plain, []byte(id))
	if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET credentials=$2 WHERE id=$1`, id, expired); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := f.service.GitHubInstallations(t.Context(), f.project, 1); failures <- err })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.provider.Refreshes.Load() != 1 {
		t.Fatal("concurrent refresh consumed the token more than once")
	}
	connections, err := f.service.Connections(t.Context(), f.project)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(connections)
	if bytes.Contains(public, []byte(token.AccessToken)) || bytes.Contains(public, []byte("refresh_token")) {
		t.Fatal("public status exposed credentials")
	}
}

func TestRevokedCredentialsRequireReconnectAndTransientFailureRetainsConnection(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	f.provider.Unavailable.Store(true)
	if _, err := f.service.LinearTeams(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrProvider) {
		t.Fatal(err)
	}
	f.provider.Unavailable.Store(false)
	if _, err := f.service.LinearTeams(t.Context(), f.project, ""); err != nil {
		t.Fatal("temporary failure lost authorization", err)
	}
	request := httptest.NewRequest("POST", "/fixture/revoke", nil)
	f.provider.ServeHTTP(httptest.NewRecorder(), request)
	if _, err := f.service.LinearTeams(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrReconnect) {
		t.Fatal(err)
	}
	connections, err := f.service.Connections(t.Context(), f.project)
	if err != nil || connections[1].Status != "reconnect_required" {
		t.Fatal("revocation status missing", err)
	}
	f.connect(t, "linear")
	if _, err := f.service.LinearTeams(t.Context(), f.project, ""); err != nil {
		t.Fatal("reconnect failed", err)
	}
}

func TestImportsAreScopedIdempotentAndSupplyOnlyVerifiedGitCredentials(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	f.connect(t, "linear")
	repository, created, err := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
	if err != nil || !created {
		t.Fatal("repository import failed", err)
	}
	again, created, err := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
	if err != nil || created || again != repository {
		t.Fatal("repository import duplicated", err)
	}
	if _, _, err := f.service.ImportGitHub(t.Context(), f.project, "999", "202", 1); !errors.Is(err, integrations.ErrAccess) {
		t.Fatal("foreign installation accepted", err)
	}
	token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repository), "https://github.com/fixture/private-source.git")
	if err != nil || !strings.HasPrefix(token, "ghu_") {
		t.Fatal("credential handoff failed", err)
	}
	for _, remote := range []string{"https://other.example/fixture/private-source.git", "https://github.com/other/repository.git", "https://github.com/fixture/private-source.git?token=ignored"} {
		if token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repository), remote); token != "" || !errors.Is(err, integrations.ErrAccess) {
			t.Fatal("credential supplied to unverified remote", err)
		}
	}
	task, created, err := f.service.ImportLinear(t.Context(), f.project, repository, testsupport.ProviderIssueID)
	if err != nil || !created {
		t.Fatal("issue import failed", err)
	}
	again, created, err = f.service.ImportLinear(t.Context(), f.project, repository, testsupport.ProviderIssueID)
	if err != nil || created || again != task {
		t.Fatal("issue import duplicated", err)
	}
	var title, description string
	var refs []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT title,description,external_refs FROM tasks WHERE id=$1`, task).Scan(&title, &description, &refs); err != nil {
		t.Fatal(err)
	}
	if title != "Imported integration task" || description == "" || !bytes.Contains(refs, []byte("TST-1")) {
		t.Fatal("issue details lost")
	}
	foreign := uuid.NewString()
	_, err = f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Foreign')`, foreign)
	if err != nil {
		t.Fatal(err)
	}
	other := fixture{pool: f.pool, service: f.service, provider: f.provider, project: foreign}
	other.connect(t, "linear")
	if _, _, err := f.service.ImportLinear(t.Context(), foreign, repository, testsupport.ProviderIssueID); !errors.Is(err, integrations.ErrAccess) {
		t.Fatal("foreign repository accepted", err)
	}
}
