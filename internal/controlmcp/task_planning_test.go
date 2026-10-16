package controlmcp_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTaskPlanningMCP(t *testing.T) {
	pool := testsupport.Database(t)
	handler, e := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	api := httptest.NewServer(handler)
	defer api.Close()
	config := controlmcp.Config{APIURL: api.URL, WebURL: "http://localhost:5173"}
	client := connect(t, config)
	project := call(t, client, "create_project", object{"name": "Planning"})["project"].(object)["id"]
	repo := call(t, client, "add_repository", object{"project_id": project, "name": "Source", "clone_url": "/fixture"})["repository"].(object)
	agent := call(t, client, "create_agent", object{"project_id": project, "name": "Engineer", "instructions": "Test", "backend": "fake"})["agent"].(object)
	task := call(t, client, "create_task", object{"project_id": project, "title": "A", "priority": "high", "repository_id": repo["id"], "preferred_agent_id": agent["id"]})["task"].(object)
	if task["priority"] != "high" {
		t.Fatal(task)
	}
	detail := call(t, client, "get_task_planning", object{"task_id": task["id"]})
	if detail["task"].(object)["id"] != task["id"] || detail["console_url"] == nil {
		t.Fatal(detail)
	}
	patch := call(t, client, "update_task", object{"task_id": task["id"], "expected_revision": 1, "title": "Changed", "repository_id": nil, "preferred_agent_id": nil})
	if patch["task"].(object)["priority"] != "high" || patch["task"].(object)["repository_id"] != nil || patch["task"].(object)["preferred_agent_id"] != nil {
		t.Fatal("omission reset priority", patch)
	}
	moved := call(t, client, "move_task", object{"task_id": task["id"], "expected_revision": 2, "expected_board_revision": patch["board_revision"], "status": "ready", "placement": "end"})
	if moved["task"].(object)["status"] != "ready" {
		t.Fatal(moved)
	}
	page := call(t, client, "list_task_planning", object{"project_id": project, "status": "ready"})
	if len(page["groups"].([]any)) != 1 {
		t.Fatal(page)
	}
	rejects(t, client, "update_task", object{"task_id": task["id"], "expected_revision": 1, "title": "stale"}, "409")
	var count int
	if e = pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	config.ReadOnly = true
	readonly := connect(t, config)
	listed, e := readonly.ListTools(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "update_task" || tool.Name == "move_task" {
			t.Fatal("mutation in read-only mode")
		}
	}
}
func TestTaskPlanningMCPOlderAPI(t *testing.T) {
	var posts atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			posts.Add(1)
			_, _ = w.Write([]byte(`{"id":"saved"}`))
			return
		}
		_, _ = w.Write([]byte(`{"paths":{},"components":{"schemas":{"TaskCreate":{"properties":{}}}}}`))
	}))
	defer api.Close()
	client := connect(t, controlmcp.Config{APIURL: api.URL})
	input := object{"project_id": uuid.NewString(), "title": "A", "priority": "high"}
	rejects(t, client, "create_task", input, "planning")
	if posts.Load() != 0 {
		t.Fatal("older API received unsupported create")
	}
	delete(input, "priority")
	call(t, client, "create_task", input)
	if posts.Load() != 1 {
		t.Fatal(posts.Load())
	}
}
