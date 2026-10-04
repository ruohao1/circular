package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestIntegrationHTTPBrowserBindingAndPublicResponses(t *testing.T) {
	pool := testsupport.Database(t)
	provider := httptest.NewServer(testsupport.NewProviderFixture())
	defer provider.Close()
	config := integrations.Config{APIURL: "https://api.circular.test", WebURL: "https://circular.test", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, GitHubURL: provider.URL, GitHubAPIURL: provider.URL}
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), CORSOrigins: []string{config.WebURL}, Integrations: config})
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Integration HTTP')`, project); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project + "/integrations"
	call := func(method, path, body, origin, contentType string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", contentType)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, test := range []struct {
		origin, content string
		status          int
	}{{"https://untrusted.test", "application/json", 403}, {config.WebURL, "text/plain", 415}} {
		if got := call("POST", base+"/github/connect", "{}", test.origin, test.content, nil); got.Code != test.status {
			t.Fatalf("unsafe connect returned %d", got.Code)
		}
	}
	begin := call("POST", base+"/github/connect", "{}", config.WebURL, "application/json", nil)
	if begin.Code != 200 || begin.Header().Get("Access-Control-Allow-Credentials") != "true" || begin.Header().Get("Access-Control-Allow-Origin") != config.WebURL {
		t.Fatal("credentialed connection initiation failed", begin.Code)
	}
	cookies := begin.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("binding cookie missing")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/v1/integrations/github/callback" || cookie.MaxAge != 600 {
		t.Fatal("incorrect cookie protections")
	}
	var auth struct {
		URL string `json:"authorization_url"`
	}
	if json.Unmarshal(begin.Body.Bytes(), &auth) != nil {
		t.Fatal("invalid authorization response")
	}
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
	path := callback.RequestURI()
	// A stolen code/state pair in a different browser cannot connect the project.
	unbound := call("GET", path, "", "", "", nil)
	if unbound.Code != 303 || !strings.Contains(unbound.Header().Get("Location"), "connection_result=failed") {
		t.Fatal("unbound callback accepted")
	}
	success := call("GET", path, "", "", "", cookie)
	target, _ := url.Parse(success.Header().Get("Location"))
	if success.Code != 303 || target.Scheme+"://"+target.Host != config.WebURL || target.Query().Get("connection_result") != "connected" || target.Query().Get("project_id") != project {
		t.Fatal("callback did not return to the bound project")
	}
	if success.Header().Get("Referrer-Policy") != "no-referrer" || success.Header().Get("Cache-Control") != "no-store" || success.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("callback exposed or retained authorization state")
	}
	for _, path := range []string{base, base + "/github/installations", base + "/github/repositories?installation_id=101"} {
		public := call("GET", path, "", "", "", nil)
		if public.Code != 200 {
			t.Fatal("provider browse failed", public.Code, public.Body.String())
		}
		for _, secret := range []string{"ghu_", "refresh_token", "client_secret", "credentials"} {
			if strings.Contains(public.Body.String(), secret) {
				t.Fatal("public API leaked credentials")
			}
		}
	}
	imported := call("POST", base+"/github/import", `{"installation_id":"101","repository_id":"202"}`, config.WebURL, "application/json", nil)
	if imported.Code != 201 {
		t.Fatal("import failed", imported.Code, imported.Body.String())
	}
	duplicate := call("POST", base+"/github/import", `{"installation_id":"101","repository_id":"202"}`, config.WebURL, "application/json", nil)
	if duplicate.Code != 200 {
		t.Fatal("duplicate import was not idempotent")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM repositories WHERE project_id=$1`, project).Scan(&count); err != nil || count != 1 {
		t.Fatal("unexpected imported repository count", err)
	}
	disconnected := call("POST", base+"/github/disconnect", "{}", config.WebURL, "application/json", nil)
	if disconnected.Code != 200 || !strings.Contains(disconnected.Body.String(), `"disconnected":true`) {
		t.Fatal("disconnect failed")
	}
	if got := call("GET", base+"/github/installations", "", "", "", nil); got.Code != 409 {
		t.Fatal("disconnected access remained available")
	}
	replay := call("GET", path, "", "", "", cookie)
	if !strings.Contains(replay.Header().Get("Location"), "connection_result=failed") {
		t.Fatal("replay revived connection")
	}
}

func TestUnconfiguredIntegrationAndTaskLookup(t *testing.T) {
	f := setup(t)
	run := f.run(t)
	task := decode(t, f.request(t, "GET", "/api/v1/tasks/"+run["task_id"].(string), "", 200))
	if task["id"] != run["task_id"] {
		t.Fatal("task lookup changed identity")
	}
	f.request(t, "GET", "/api/v1/tasks/"+uuid.NewString(), "", 404)
	project := task["project_id"].(string)
	data := f.request(t, "GET", "/api/v1/projects/"+project+"/integrations", "", 200)
	if !strings.Contains(string(data), `"status":"not_configured"`) {
		t.Fatal("unconfigured provider status missing")
	}
	f.request(t, "POST", "/api/v1/projects/"+project+"/integrations/github/connect", "{}", 503)
}

func TestConsoleAppRegistrationHTTPBoundaries(t *testing.T) {
	pool := testsupport.Database(t)
	fixture := testsupport.NewProviderFixture()
	provider := httptest.NewServer(fixture)
	defer provider.Close()
	config := integrations.Config{APIURL: "https://api.circular.test", WebURL: "https://circular.test", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), GitHubURL: provider.URL, GitHubAPIURL: provider.URL, LinearURL: provider.URL, LinearAPIURL: provider.URL}
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), Integrations: config})
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Console registration')`, project); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project + "/integrations"
	call := func(method, path, body, origin, contentType string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", contentType)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{base + "/github/register", base + "/github/app", base + "/linear/app"} {
		if w := call("POST", path, `{"client_id":"fixture-linear"}`, "https://foreign.test", "application/json", nil); w.Code != 403 {
			t.Fatal("cross-origin app setup accepted", w.Code)
		}
		if w := call("POST", path, `{"client_id":"fixture-linear"}`, config.WebURL, "text/plain", nil); w.Code != 415 {
			t.Fatal("form submission accepted", w.Code)
		}
	}
	begin := call("POST", base+"/github/register", `{"organization":""}`, config.WebURL, "application/json", nil)
	if begin.Code != 200 {
		t.Fatal("registration failed", begin.Code, begin.Body.String())
	}
	cookies := begin.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("registration cookie missing")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/v1/integrations/github/registration-callback" || cookie.MaxAge != 600 {
		t.Fatal("registration cookie protections missing")
	}
	var registration integrations.AppRegistration
	if err := json.Unmarshal(begin.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(registration.URL)
	code, err := fixture.ManifestCode(registration.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	callback := "/api/v1/integrations/github/registration-callback?state=" + u.Query().Get("state") + "&code=" + code
	unbound := call("GET", callback, "", "", "", nil)
	if unbound.Code != 303 || !strings.Contains(unbound.Header().Get("Location"), "connection_result=failed") || fixture.Conversions.Load() != 0 {
		t.Fatal("unbound manifest callback reached provider")
	}
	success := call("GET", callback, "", "", "", cookie)
	if success.Code != 303 || success.Header().Get("Location") != provider.URL+"/apps/circular-fixture/installations/new" || success.Header().Get("Cache-Control") != "no-store" || success.Header().Get("Referrer-Policy") != "no-referrer" || success.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("manifest callback did not safely redirect to installation", success.Code)
	}
	replay := call("GET", callback, "", "", "", cookie)
	if !strings.Contains(replay.Header().Get("Location"), "connection_result=failed") || fixture.Conversions.Load() != 1 {
		t.Fatal("manifest callback replay accepted")
	}
	fixture.RenameGitHubApp("renamed-circular-fixture")
	if rejected := call("POST", base+"/github/app", `{"app_url":"another-fixture-app"}`, config.WebURL, "application/json", nil); rejected.Code != 422 {
		t.Fatal("different GitHub app accepted", rejected.Code)
	}
	updated := call("POST", base+"/github/app", `{"app_url":"`+provider.URL+`/apps/renamed-circular-fixture"}`, config.WebURL, "application/json", nil)
	if updated.Code != 200 {
		t.Fatal("renamed app URL could not be saved", updated.Code, updated.Body.String())
	}
	var connections []integrations.Connection
	if err := json.Unmarshal(call("GET", base, "", "", "", nil).Body.Bytes(), &connections); err != nil || connections[0].InstallationURL != provider.URL+"/apps/renamed-circular-fixture/installations/new" {
		t.Fatal("access link retained the previous app name", err)
	}
	if saved := call("POST", base+"/linear/app", `{"client_id":"fixture-linear"}`, config.WebURL, "application/json", nil); saved.Code != 200 {
		t.Fatal("public Client ID setup failed", saved.Code, saved.Body.String())
	}
	if connected := call("POST", base+"/linear/connect", `{}`, config.WebURL, "application/json", nil); connected.Code != 200 {
		t.Fatal("registered Linear app could not start OAuth", connected.Code)
	}
	status := call("GET", base, "", "", "", nil)
	if status.Code != 200 {
		t.Fatal("status unavailable", status.Code)
	}
	for _, secret := range []string{"client_secret", "credentials", "pem", "fixture-github-secret", "fixture-unused"} {
		if strings.Contains(status.Body.String(), secret) || strings.Contains(begin.Body.String(), secret) {
			t.Fatal("registration exposed app credentials")
		}
	}
}
