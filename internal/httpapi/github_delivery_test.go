package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGitHubDeliveryHTTPGuardsAndProjectOptIn(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "PR delivery HTTP"})
	settingsPath := "/api/v1/projects/" + project["id"].(string) + "/integrations/github/run-delivery"
	var settings map[string]any
	if err := json.Unmarshal(f.request(t, "GET", settingsPath, "", 200), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["enabled"] != false || settings["authorized"] != false {
		t.Fatal("unexpected default publishing", settings)
	}
	f.request(t, "POST", settingsPath, `{"enabled":true}`, 403)
	f.request(t, "POST", settingsPath, `{"enabled":false}`, 200)
	f.request(t, "POST", settingsPath, `{"enabled":"true"}`, 422)
	f.request(t, "GET", "/api/v1/projects/"+uuid.NewString()+"/integrations/github/run-delivery", "", 404)
	agent := f.create(t, "agents", map[string]any{"project_id": project["id"], "name": "Fixture"})
	task := f.create(t, "tasks", map[string]any{"project_id": project["id"], "title": "Not ready to publish"})
	run := f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agent["id"]})
	path := "/api/v1/runs/" + run["id"].(string) + "/github-delivery"
	var delivery map[string]any
	if err := json.Unmarshal(f.request(t, "GET", path, "", 200), &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery["status"] != "not_ready" {
		t.Fatal("unready run advertised publishable", delivery)
	}
	f.request(t, "POST", path, `{}`, 409)
	f.request(t, "POST", path, `[]`, 422)
	f.request(t, "POST", "/api/v1/runs/"+uuid.NewString()+"/github-delivery", `{}`, 404)
	for _, test := range []struct {
		path, origin, content string
		want                  int
	}{
		{path, "https://untrusted.test", "application/json", 403},
		{path, "", "text/plain", 415},
		{settingsPath, "https://untrusted.test", "application/json", 403},
	} {
		r := httptest.NewRequest("POST", test.path, strings.NewReader(`{}`))
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Content-Type", test.content)
		w := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("publish guard returned %d,want %d", w.Code, test.want)
		}
	}
}
