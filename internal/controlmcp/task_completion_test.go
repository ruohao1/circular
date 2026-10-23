package controlmcp_test

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompletionToolsSeparateReadOnlyAndMutation(t *testing.T) {
	task, session := uuid.NewString(), uuid.NewString()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"+task+"/") {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"stage":"in_review"}`)
	}))
	defer api.Close()
	rw := connect(t, controlmcp.Config{APIURL: api.URL})
	ro := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	for _, tool := range []string{"get_task_completion", "prepare_task_pr_review", "list_task_pr_reviews"} {
		call(t, ro, tool, object{"task_id": task})
	}
	for _, tool := range []string{"refresh_task_completion", "bind_task_pull_request", "launch_task_pr_review"} {
		args := object{"task_id": task}
		if tool == "bind_task_pull_request" {
			args["session_id"] = session
			args["expected_revision"] = 1
			args["number"] = 1
		}
		if tool == "launch_task_pr_review" {
			args["request_key"] = uuid.NewString()
			args["mode"] = "normal"
			args["expected_input_fingerprint"] = strings.Repeat("a", 64)
		}
		call(t, rw, tool, args)
		rejects(t, ro, tool, args, "")
	}
	rejects(t, rw, "bind_task_pull_request", object{"task_id": task, "number": 1, "session_id": session, "expected_revision": 1, "url": "https://evil.invalid"}, "")
	rejects(t, rw, "set_task_completion_policy", object{"task_id": task}, "")
}
