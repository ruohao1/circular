package controlmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
)

func TestGitHubPublishingToolsPreserveRunAndRespectReadOnly(t *testing.T) {
	project, run := uuid.NewString(), uuid.NewString()
	var publishes, settingsChanges atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/" + project + "/integrations/github/run-delivery":
			if r.Method == "POST" {
				var body object
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["enabled"] != true {
					t.Error("publishing preference changed", body)
				}
				settingsChanges.Add(1)
			}
			fmt.Fprint(w, `{"enabled":true,"authorized":true,"permission_message":"","pending_count":0,"failed_count":0,"last_error":""}`)
		case "/api/v1/runs/" + run + "/github-delivery":
			if r.Method == "POST" {
				publishes.Add(1)
			}
			fmt.Fprint(w, `{"status":"pending","pull_request_url":"","number":0,"branch":"circular/run/test","base_branch":"main","base_commit":"","draft":true,"error":"","retryable":false}`)
		default:
			t.Error("unexpected publishing endpoint", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	client := connect(t, controlmcp.Config{APIURL: api.URL})
	call(t, client, "get_github_publishing", object{"project_id": project})
	call(t, client, "set_github_publishing", object{"project_id": project, "enabled": true})
	for range 2 {
		result := call(t, client, "publish_run_pull_request", object{"run_id": run})
		if result["delivery"].(object)["status"] != "pending" {
			t.Fatal("queued state hidden", result)
		}
	}
	call(t, client, "get_run_pull_request", object{"run_id": run})
	ro := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	call(t, ro, "get_github_publishing", object{"project_id": project})
	call(t, ro, "get_run_pull_request", object{"run_id": run})
	rejects(t, ro, "publish_run_pull_request", object{"run_id": run}, "")
	rejects(t, ro, "set_github_publishing", object{"project_id": project, "enabled": true}, "")
	rejects(t, client, "publish_run_pull_request", object{"run_id": "../other"}, "")
	if publishes.Load() != 2 || settingsChanges.Load() != 1 {
		t.Fatal("read-only or invalid call changed publication", publishes.Load(), settingsChanges.Load())
	}
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "publish_run_pull_request" || tool.Name == "set_github_publishing" {
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
				t.Fatal("publication hides external writes")
			}
		}
	}
}
