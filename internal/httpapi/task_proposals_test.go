package httpapi_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
)

func TestTaskProposalBrowserDecisionBoundary(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Approval"})
	config := integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture", ClientSecret: "fixture-secret"}, ApproverGitHubIDs: []string{"101"}}
	h, err := httpapi.New(f.pool, httpapi.Config{ArtifactRoot: t.TempDir(), Integrations: config, CORSOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, origin, cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "circular_approval_session", Value: cookie})
		}
		r.Header.Set("X-Circular-CSRF", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(map[string]any{"project_id": project["id"], "request_key": uuid.NewString(), "draft": map[string]any{"title": "Report export", "purpose": "Share results", "description": "Export the selected report", "acceptance_criteria": []string{"CSV contains selected rows"}, "completion_policy": map[string]any{"mode": "direct_completion", "requirements": []any{}}}})
	w := call("POST", "/api/v1/task-proposals", string(body), "", "", "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := decode(t, w.Body.Bytes())
	path := "/api/v1/task-proposals/" + p["id"].(string) + "/decision"
	decision := `{"expected_revision":1,"decision":"approved","reason":""}`
	for _, origin := range []string{"", "https://evil.invalid", "http://localhost:5173"} {
		w = call("POST", path, decision, origin, "", "")
		if w.Code != 401 && w.Code != 403 {
			t.Fatalf("anonymous decision: %d %s", w.Code, w.Body.String())
		}
	}
	cookie := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	hash := sha256.Sum256([]byte(cookie))
	csrfHash := sha256.Sum256([]byte("circular-approval-csrf:" + cookie))
	csrf := hex.EncodeToString(csrfHash[:])
	if _, err = f.pool.Exec(t.Context(), `INSERT INTO approval_sessions(token_hash,user_id,login,app_client_id,expires_at) VALUES($1,'101','reviewer','fixture',$2)`, hex.EncodeToString(hash[:]), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ origin, csrf string }{{"https://evil.invalid", csrf}, {"", csrf}, {"http://localhost:5173", "wrong"}} {
		w = call("POST", path, decision, test.origin, cookie, test.csrf)
		if w.Code != 403 {
			t.Fatal("CSRF accepted", w.Code, w.Body.String())
		}
	}
	w = call("GET", "/api/v1/approval-auth/session", "", "http://localhost:5173", cookie, "")
	if w.Code != 200 || decode(t, w.Body.Bytes())["csrf_token"] != csrf || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", path, `{"expected_revision":1,"decision":"approved","approver_id":"202"}`, "http://localhost:5173", cookie, csrf)
	if w.Code != 422 {
		t.Fatal("caller identity accepted", w.Code, w.Body.String())
	}
	w = call("POST", path, decision, "http://localhost:5173", cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	out := decode(t, w.Body.Bytes())
	receipt := out["decision"].(map[string]any)
	if receipt["approver_id"] != "101" {
		t.Fatal(receipt)
	}
	w = call("POST", "/api/v1/approval-auth/logout", `{}`, "http://localhost:5173", cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", path, decision, "http://localhost:5173", cookie, csrf)
	if w.Code != 401 {
		t.Fatal("logout ineffective", w.Code)
	}
}

func TestTaskProposalLegacyCreateIsPending(t *testing.T) {
	f := setup(t)
	p := f.create(t, "projects", map[string]any{"name": "Private work"})
	raw := f.request(t, "POST", "/api/v1/tasks", `{"project_id":"`+p["id"].(string)+`","title":"Agent suggestion","description":"Not yet approved"}`, 202)
	out := decode(t, raw)
	if out["status"] != "pending" || out["task_id"] != nil {
		t.Fatal(out)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
