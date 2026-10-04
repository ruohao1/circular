package httpapi_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestLinearRunUpdateHTTPBoundaries(t *testing.T) {
	f := setup(t)
	run := f.run(t)
	task := decode(t, f.request(t, "GET", "/api/v1/tasks/"+run["task_id"].(string), "", 200))
	path := "/api/v1/projects/" + task["project_id"].(string) + "/integrations/linear/run-updates"
	settings := decode(t, f.request(t, "GET", path, "", 200))
	if settings["enabled"] != false || settings["authorized"] != false || settings["pending_count"] != float64(0) {
		t.Fatal("new projects must not publish updates without authorization", settings)
	}
	f.request(t, "POST", path, `{"enabled":true}`, 409)
	f.request(t, "POST", path, `{"enabled":false}`, 200)
	for _, body := range []string{`{}`, `{"enabled":"true"}`, `{"enabled":null}`} {
		f.request(t, "POST", path, body, 422)
	}
	for _, method := range []string{"GET", "POST"} {
		f.request(t, method, "/api/v1/projects/"+uuid.NewString()+"/integrations/linear/run-updates", `{"enabled":false}`, 404)
		f.request(t, method, "/api/v1/projects/not-a-uuid/integrations/linear/run-updates", `{"enabled":false}`, 422)
	}
	for _, test := range []struct {
		origin, content string
		want            int
	}{{"https://untrusted.test", "application/json", 403}, {"", "text/plain", 415}} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"enabled":false}`))
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Content-Type", test.content)
		w := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("unsafe settings mutation accepted: got %d, want %d", w.Code, test.want)
		}
	}
	deliveryPath := "/api/v1/runs/" + run["id"].(string) + "/linear-delivery"
	delivery := decode(t, f.request(t, "GET", deliveryPath, "", 200))
	if delivery["status"] != "not_linked" || delivery["issue_url"] != "" || delivery["last_delivered_at"] != nil {
		t.Fatal("unlinked run should not imply an issue or delivery", delivery)
	}
	f.request(t, "GET", "/api/v1/runs/"+uuid.NewString()+"/linear-delivery", "", 404)
	f.request(t, "GET", "/api/v1/runs/invalid/linear-delivery", "", 422)
}
