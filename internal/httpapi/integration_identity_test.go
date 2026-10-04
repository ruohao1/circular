package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestIdentityHTTPGuardsAndRedaction(t *testing.T) {
	pool := testsupport.Database(t)
	provider := httptest.NewServer(testsupport.NewProviderFixture())
	defer provider.Close()
	config := integrations.Config{WebURL: "https://circular.test", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, Linear: integrations.OAuthApp{ClientID: "fixture-linear"}, GitHubURL: provider.URL, GitHubAPIURL: provider.URL, LinearURL: provider.URL, LinearAPIURL: provider.URL}
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), Integrations: config})
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'HTTP identity')`, project); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project + "/integrations"
	call := func(method, path, payload, origin, content string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, base+path, strings.NewReader(payload))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", content)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, payload, origin, content string
		status                                 int
	}{
		{"GET", "/github/identity", "", "", "", 200},
		{"POST", "/github/identity/detach", "{}", "https://untrusted.test", "application/json", 403},
		{"POST", "/github/identity/detach", "{}", config.WebURL, "text/plain", 415},
		{"POST", "/github/identity/bind", `{"identity_id":"` + uuid.NewString() + `"}`, config.WebURL, "application/json", 409},
		{"POST", "/github/identity/key", `{"private_key":"` + strings.Repeat("x", 65537) + `"}`, config.WebURL, "application/json", 422},
	} {
		w := call(tc.method, tc.path, tc.payload, tc.origin, tc.content)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("identity response can be cached")
		}
		for _, secret := range []string{"client_secret", "credentials", "fixture-github-secret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("response leaked secret")
			}
		}
	}
	begin := call("POST", "/linear/identity/connect", `{"purpose":"identity"}`, config.WebURL, "application/json")
	if begin.Code != 200 {
		t.Fatal(begin.Code, begin.Body.String())
	}
	var auth struct {
		URL string `json:"authorization_url"`
	}
	if json.Unmarshal(begin.Body.Bytes(), &auth) != nil || !strings.Contains(auth.URL, "actor=app") {
		t.Fatal("wrong authorization intent")
	}
	cookies := begin.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/api/v1/integrations/linear/callback" {
		t.Fatal("missing browser binding")
	}
}
