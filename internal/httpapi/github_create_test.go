package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGitHubCreationHTTPValidationAndOrigin(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "GitHub create HTTP"})
	path := "/api/v1/projects/" + project["id"].(string) + "/integrations/github/repositories"
	valid := map[string]any{"installation_id": "101", "name": "new-repo", "request_key": uuid.NewString()}
	for _, change := range []map[string]any{{"installation_id": "../1"}, {"name": true}, {"name": ""}, {"request_key": "bad"}, {"description": nil}, {"visibility": false}, {"visibility": "internal"}} {
		input := map[string]any{}
		for key, value := range valid {
			input[key] = value
		}
		for key, value := range change {
			input[key] = value
		}
		body, _ := json.Marshal(input)
		f.request(t, "POST", path, string(body), 422)
	}
	body, _ := json.Marshal(valid)
	f.request(t, "POST", path, string(body), 409) // A project must connect GitHub first.
	f.request(t, "POST", "/api/v1/projects/"+uuid.NewString()+"/integrations/github/repositories", string(body), 404)
	for _, test := range []struct {
		origin, content string
		want            int
	}{{"https://untrusted.test", "application/json", 403}, {"", "text/plain", 415}} {
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Content-Type", test.content)
		w := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("unsafe create returned%d,want%d", w.Code, test.want)
		}
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM github_repository_creations`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid or disconnected request saved a creation intent", count, err)
	}
}
