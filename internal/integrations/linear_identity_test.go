package integrations_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
)

func TestLinearIdentityRevokedRefreshShowsReconnect(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	identity, err := f.service.Identity(t.Context(), f.project, "linear")
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, identity.IdentityID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	aad := []byte("provider-identity:" + identity.IdentityID)
	plain, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], aad)
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
	expired := vault.Seal(nonce, nonce, plain, aad)
	if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET credentials=$2 WHERE id=$1`, identity.IdentityID, expired); err != nil {
		t.Fatal(err)
	}
	config := f.config
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/token" {
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_request","error_description":"Refresh token revoked"}`))}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	service, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinearTeams(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrReconnect) {
		t.Fatalf("revoked refresh must request reconnect: %v", err)
	}
	current, err := service.Identity(t.Context(), f.project, "linear")
	if err != nil || current.Status != "reconnect_required" || current.Mode != "app" || current.IdentityID != identity.IdentityID {
		t.Fatalf("identity must expose reconnect without changing its author: %+v, %v", current, err)
	}
	if _, err := service.LinearTeams(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrIdentityUnavailable) {
		t.Fatalf("revoked app must not fall back to connected user: %v", err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, identity.IdentityID).Scan(&stored); err != nil || !bytes.Equal(stored, expired) {
		t.Fatal("revoked credentials were unexpectedly changed", err)
	}
}

func TestLinearIdentityReconnectPreservesAgentGrant(t *testing.T) {
	for _, test := range []struct {
		name          string
		purpose       string
		existingWrite bool
		scopes        string
		want          error
	}{
		{"complete grant", "identity", false, "read comments:create app:mentionable app:assignable", nil},
		{"incomplete grant", "identity", false, "read comments:create", integrations.ErrAccess},
		{"reconnect retains write", "identity", true, "read comments:create app:mentionable app:assignable write", nil},
		{"reconnect rejects write loss", "identity", true, "read comments:create app:mentionable app:assignable", integrations.ErrAccess},
		{"agent authorization retains write", "agent", true, "read comments:create app:mentionable app:assignable write", nil},
		{"agent authorization rejects write loss", "agent", true, "read comments:create app:mentionable app:assignable", integrations.ErrAccess},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, route := agentRequestFixture(t)
			f.connect(t, "linear")
			if test.existingWrite {
				if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET granted_scopes=array_append(granted_scopes,'write') WHERE id=$1`, route.IdentityID); err != nil {
					t.Fatal(err)
				}
			}
			route.Mode, route.Enabled = "approval", false
			saved, err := f.service.SaveRequestRoute(t.Context(), route)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.service.Identity(t.Context(), f.project, "linear")
			if err != nil {
				t.Fatal(err)
			}
			var credentials, humanCredentials []byte
			if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, before.IdentityID).Scan(&credentials); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integrations WHERE project_id=$1 AND provider='linear'`, f.project).Scan(&humanCredentials); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET status='reconnect_required' WHERE id=$1`, before.IdentityID); err != nil {
				t.Fatal(err)
			}
			auth, err := f.service.BeginLinearAppAuthorization(t.Context(), f.project, test.purpose)
			if err != nil {
				t.Fatal(err)
			}
			link, err := url.Parse(auth.URL)
			if err != nil {
				t.Fatal(err)
			}
			wantedScopes := "read,comments:create,app:mentionable,app:assignable"
			if test.existingWrite {
				wantedScopes += ",write"
			}
			if link.Query().Get("scope") != wantedScopes {
				t.Errorf("reconnect scopes = %s; want %s", link.Query().Get("scope"), wantedScopes)
			}
			f.provider.SetLinearScopes(test.scopes)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Get(auth.URL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			callback, err := url.Parse(response.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, callback.Query().Get("code")); !errors.Is(err, test.want) {
				t.Fatalf("reconnect result = %v; want %v", err, test.want)
			}
			after, err := f.service.Identity(t.Context(), f.project, "linear")
			if err != nil || after.IdentityID != before.IdentityID || after.Mode != "app" || strings.Join(after.Capabilities, ",") != strings.Join(before.Capabilities, ",") {
				t.Fatalf("reconnect changed the selected identity or capabilities: %+v, %v", after, err)
			}
			if test.want != nil {
				var retained []byte
				if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, before.IdentityID).Scan(&retained); err != nil || !bytes.Equal(retained, credentials) || after.Status != "reconnect_required" {
					t.Fatal("incomplete grant replaced the existing app authorization", err)
				}
			} else if after.Status != "enabled" {
				t.Fatal("complete grant did not restore the existing app", after.Status)
			}
			var retainedHuman []byte
			var humanEnabled, routeEnabled bool
			var routeMode string
			if err := f.pool.QueryRow(t.Context(), `SELECT credentials,enabled FROM integrations WHERE project_id=$1 AND provider='linear'`, f.project).Scan(&retainedHuman, &humanEnabled); err != nil || !humanEnabled || !bytes.Equal(retainedHuman, humanCredentials) {
				t.Fatal("reconnect changed the human connection", err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT enabled,mode FROM linear_request_routes WHERE id=$1`, saved.ID).Scan(&routeEnabled, &routeMode); err != nil || routeEnabled || routeMode != "approval" {
				t.Fatal("reconnect changed the paused approval route", err)
			}
		})
	}
}

func (f fixture) startLinearApp(t *testing.T) (integrations.Authorization, string) {
	t.Helper()
	auth, err := f.service.BeginLinearAppAuthorization(t.Context(), f.project, "identity")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(auth.URL)
	if err != nil || u.Query().Get("actor") != "app" || u.Query().Get("scope") != "read,comments:create" {
		t.Fatal("wrong app authorization", err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	callback, _ := url.Parse(response.Header.Get("Location"))
	return auth, callback.Query().Get("code")
}

func TestLinearIdentityAuthorizationUsesAppActorAndSharedGrant(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, strings.Repeat("x", 43), code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("accepted foreign browser", err)
	}
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("replayed callback", err)
	}
	status, err := f.service.Identity(t.Context(), f.project, "linear")
	if err != nil || status.ActorID != "20000000-0000-4000-8000-000000000005" || status.Status != "enabled" {
		t.Fatal(status, err)
	}
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Shared Linear')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BindIdentity(t.Context(), other, "linear", status.IdentityID); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, status.IdentityID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("access_token")) {
		t.Fatal("plaintext app grant")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	plain, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte("provider-identity:"+status.IdentityID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte(status.IdentityID)); err == nil {
		t.Fatal("grant not bound to identity namespace")
	}
	var token map[string]any
	if json.Unmarshal(plain, &token) != nil {
		t.Fatal("bad grant")
	}
	token["expires_at"] = time.Now().Add(-time.Minute)
	plain, _ = json.Marshal(token)
	nonce := make([]byte, vault.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET credentials=$2 WHERE id=$1`, status.IdentityID, vault.Seal(nonce, nonce, plain, []byte("provider-identity:"+status.IdentityID))); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			project := f.project
			if i%2 == 0 {
				project = other
			}
			_, err := f.service.LinearTeams(t.Context(), project, "")
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.provider.Refreshes.Load() != 1 {
		t.Fatal("shared rotating token consumed twice")
	}
	if err := f.service.DetachIdentity(t.Context(), f.project, "linear"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.LinearTeams(t.Context(), f.project, ""); !errors.Is(err, integrations.ErrIdentityUnavailable) {
		t.Fatal("detached grant fell back to user", err)
	}
	if _, err := f.service.LinearTeams(t.Context(), other, ""); err != nil {
		t.Fatal("detach affected shared grant", err)
	}
}

func TestLinearIdentityWrongWorkspaceScopeLossAndCancellationPreserveGrant(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.Identity(t.Context(), f.project, "linear")
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range []string{"workspace", "scope", "cancel"} {
		t.Run(problem, func(t *testing.T) {
			auth, code := f.startLinearApp(t)
			expected := integrations.ErrDenied
			switch problem {
			case "workspace":
				f.provider.SetLinearOrganization(uuid.NewString())
				expected = integrations.ErrIdentityConflict
			case "scope":
				f.provider.SetLinearScopes("read")
				expected = integrations.ErrAccess
			case "cancel":
				code = ""
			}
			if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); !errors.Is(err, expected) {
				t.Fatal("bad upgrade accepted", err)
			}
			f.provider.SetLinearOrganization(before.AccountID)
			f.provider.SetLinearScopes("read comments:create")
			after, err := f.service.Identity(t.Context(), f.project, "linear")
			if err != nil || after.IdentityID != before.IdentityID || after.Status != "enabled" {
				t.Fatal("failed upgrade lost old grant", err)
			}
		})
	}
}

func TestLinearIdentityDetachInvalidatesPendingAuthorization(t *testing.T) {
	f := setup(t)
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	auth, code = f.startLinearApp(t)
	if err := f.service.DetachIdentity(t.Context(), f.project, "linear"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("pending callback revived disabled identity", err)
	}
}

func TestLinearIdentityBindingRejectsDifferentConnectedWorkspace(t *testing.T) {
	f := setup(t)
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	identity, err := f.service.Identity(t.Context(), f.project, "linear")
	if err != nil {
		t.Fatal(err)
	}
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Different Linear workspace')`, other); err != nil {
		t.Fatal(err)
	}
	f.provider.SetLinearOrganization(uuid.NewString())
	f.project = other
	f.connect(t, "linear")
	if _, err := f.service.BindIdentity(t.Context(), other, "linear", identity.IdentityID); !errors.Is(err, integrations.ErrIdentityConflict) {
		t.Fatal("bound another workspace", err)
	}
}
