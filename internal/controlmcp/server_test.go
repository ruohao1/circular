package controlmcp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/testsupport"
)

type object = map[string]any

func connect(t *testing.T, config controlmcp.Config) *mcp.ClientSession {
	t.Helper()
	server, err := controlmcp.New(config)
	if err != nil {
		t.Fatal(err)
	}
	s, c := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "control-test", Version: "1"}, nil).Connect(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func call(t *testing.T, client *mcp.ClientSession, name string, args object) object {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("%s failed: %v %+v", name, err, result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out object
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rejects(t *testing.T, client *mcp.ClientSession, name string, args object, message string) {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err == nil && !result.IsError {
		t.Fatalf("%s accepted invalid input: %+v", name, result)
	}
	encoded, _ := json.Marshal(result)
	if message != "" && !strings.Contains(string(encoded)+" "+toString(err), message) {
		t.Fatalf("%s error did not contain %q: %s %v", name, message, encoded, err)
	}
}

func toString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestControlMCPWorkflowUsesPublicAPIAndPreservesRunIntent(t *testing.T) {
	pool := testsupport.Database(t)
	root := t.TempDir()
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	config := controlmcp.Config{APIURL: api.URL + "/api/v1/", WebURL: "http://localhost:5173"}
	if err := controlmcp.Check(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	client := connect(t, config)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 39 {
		t.Fatal("missing tool surface", err, listed)
	}
	project := call(t, client, "create_project", object{"name": "MCP project"})["project"].(object)
	projectID := project["id"].(string)
	projects := call(t, client, "list_projects", object{"limit": 1})
	if projects["total"] != float64(1) || len(projects["items"].([]any)) != 1 {
		t.Fatal(projects)
	}
	models := call(t, client, "list_models", object{})["catalog"].(object)
	if models["default_model"] != "gpt-6-astra" {
		t.Fatal(models)
	}
	repo := call(t, client, "add_repository", object{"project_id": projectID, "name": "Source", "clone_url": "/unused-test-repo"})["repository"].(object)
	if repo["default_branch"] != "main" {
		t.Fatal(repo)
	}
	agent := call(t, client, "create_agent", object{"project_id": projectID, "name": "Specialist", "instructions": "Implement focused changes.", "model": "gpt-5.6-terra", "reasoning_effort": "high"})["agent"].(object)
	if agent["backend"] != "codex" || agent["backend_config"].(object)["reasoning_effort"] != "high" {
		t.Fatal(agent)
	}
	updated := call(t, client, "update_agent_model", object{"agent_id": agent["id"], "model": "gpt-5.6-luna", "reasoning_effort": "max"})["agent"].(object)
	if updated["instructions"] != agent["instructions"] || updated["backend_config"].(object)["model"] != "gpt-5.6-luna" {
		t.Fatal(updated)
	}
	rejects(t, client, "create_agent", object{"project_id": projectID, "name": "Invalid", "instructions": "Test", "model": "gpt-5.6-luna", "reasoning_effort": "ultra"}, "reasoning")
	fake := call(t, client, "create_agent", object{"project_id": projectID, "name": "Simulator", "instructions": "Simulated work", "backend": "fake"})["agent"].(object)
	discovery := call(t, client, "prepare_discovery", object{"project_id": projectID, "repository_id": repo["id"]})
	if discovery["agent_id"] == nil {
		t.Fatal("missing discovery Agent", discovery)
	}
	task := call(t, client, "create_task", object{"project_id": projectID, "repository_id": repo["id"], "title": "Exercise MCP", "description": "Verify control without a model call"})["task"].(object)
	if call(t, client, "get_task", object{"task_id": task["id"]})["task"].(object)["description"] != task["description"] {
		t.Fatal("task lost description")
	}
	for _, resource := range []string{"repositories", "agents", "tasks"} {
		page := call(t, client, "list_"+resource, object{"project_id": projectID, "limit": 1})
		if len(page["items"].([]any)) != 1 {
			t.Fatal(resource, page)
		}
		if resource != "repositories" && (page["has_more"] != true || page["next_offset"] != float64(1)) {
			t.Fatal("missing cursor", page)
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM runs").Scan(&count); err != nil || count != 0 {
		t.Fatal("preparation started execution", err)
	}
	launch := object{"task_id": task["id"], "agent_id": fake["id"], "request_key": "mcp-first-attempt"}
	run := call(t, client, "start_run", launch)["run"].(object)
	runID := run["id"].(string)
	if run["status"] != "queued" || run["backend"] != "fake" || run["request_key"] != launch["request_key"] {
		t.Fatal(run)
	}
	// A new MCP process/session has no local retry cache, but returns the same Run.
	reconnected := connect(t, config)
	if call(t, reconnected, "start_run", launch)["run"].(object)["id"] != runID {
		t.Fatal("reconnection duplicated launch")
	}
	rejects(t, client, "start_run", object{"task_id": task["id"], "agent_id": agent["id"], "request_key": launch["request_key"]}, "409")
	if call(t, client, "list_runs", object{"project_id": projectID, "task_id": task["id"]})["total"] != float64(1) {
		t.Fatal("duplicate attempt")
	}
	proposalArgs := object{"run_id": runID, "name": "Proposed engineer", "purpose": "Maintain the console", "instructions": "Follow UI conventions.", "model": "gpt-5.6-sol", "reasoning_effort": "medium", "model_reason": "Suitable for focused UI work."}
	proposal := call(t, client, "propose_agent", proposalArgs)["proposal"].(object)
	if proposal["status"] != "pending" || proposal["model_reason"] != proposalArgs["model_reason"] {
		t.Fatal(proposal)
	}
	if call(t, client, "propose_agent", proposalArgs)["proposal"].(object)["id"] != proposal["id"] {
		t.Fatal("proposal duplicated")
	}
	inspected := call(t, client, "get_run", object{"run_id": runID})
	if len(inspected["agent_proposals"].([]any)) != 1 || inspected["console_url"] != "http://localhost:5173/runs/"+runID {
		t.Fatal(inspected)
	}
	proposalArgs["proposal_id"] = proposal["id"]
	accepted := call(t, client, "create_proposed_agent", proposalArgs)["agent"].(object)
	if accepted["backend_config"].(object)["model"] != "gpt-5.6-sol" {
		t.Fatal(accepted)
	}
	if call(t, reconnected, "create_proposed_agent", proposalArgs)["agent"].(object)["id"] != accepted["id"] {
		t.Fatal("accept retry duplicated agent")
	}
	rejects(t, client, "dismiss_agent_proposal", object{"run_id": runID, "proposal_id": proposal["id"]}, "409")
	delete(proposalArgs, "proposal_id")
	proposalArgs["name"] = "Unused role"
	unused := call(t, client, "propose_agent", proposalArgs)["proposal"].(object)
	for range 2 {
		if call(t, client, "dismiss_agent_proposal", object{"run_id": runID, "proposal_id": unused["id"]})["proposal"].(object)["status"] != "dismissed" {
			t.Fatal("dismiss failed")
		}
	}
	for range 2 {
		if call(t, client, "cancel_run", object{"run_id": runID})["run"].(object)["status"] != "cancelled" {
			t.Fatal("cancel failed")
		}
	}
	if call(t, reconnected, "start_run", launch)["run"].(object)["status"] != "cancelled" {
		t.Fatal("retry restarted terminal run")
	}
	events := call(t, client, "get_run_events", object{"run_id": runID, "limit": 1})
	if events["next_after"] != float64(1) || len(events["events"].([]any)) != 1 {
		t.Fatal(events)
	}
	if _, raw := events["events"].([]any)[0].(object)["raw"]; raw {
		t.Fatal("raw envelope leaked")
	}
	if len(call(t, client, "get_run_events", object{"run_id": runID, "after": 1})["events"].([]any)) != 0 {
		t.Fatal("event replay repeated")
	}
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.Write(t.Context(), uuid.MustParse(runID), "git-diff.patch", []byte("+héllo 🌍\n"))
	if err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.NewString()
	if _, err := pool.Exec(t.Context(), "INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'diff',$3,$4)", artifactID, runID, content.URI, object{"sha256": content.SHA256}); err != nil {
		t.Fatal(err)
	}
	part := call(t, client, "read_artifact", object{"run_id": runID, "artifact_id": artifactID, "offset": 1, "limit": 5})
	if part["text"] != "héllo" || part["next_offset"] != float64(6) || part["has_more"] != true {
		t.Fatal("text chunk corrupted", part)
	}
	rejects(t, client, "read_artifact", object{"run_id": uuid.NewString(), "artifact_id": artifactID}, "404")
	if _, err := pool.Exec(t.Context(), `UPDATE artifacts SET metadata='{"sha256":"wrong"}' WHERE id=$1`, artifactID); err != nil {
		t.Fatal(err)
	}
	rejects(t, client, "read_artifact", object{"run_id": runID, "artifact_id": artifactID}, "409")
	foreign := call(t, client, "create_project", object{"name": "Another project"})["project"].(object)
	rejects(t, client, "create_task", object{"project_id": foreign["id"], "repository_id": repo["id"], "title": "Wrong scope"}, "another project")
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM runs").Scan(&count); err != nil || count != 1 {
		t.Fatal("extra execution created", err, count)
	}
}

func TestReadOnlyAndInvalidInputsNeverReachMutationEndpoints(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Error("read-only mode sent a mutation")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer api.Close()
	client := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	list, err := client.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 20 {
		t.Fatal(err, list)
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatal("missing read-only annotation", tool)
		}
	}
	for _, name := range []string{"start_run", "cancel_run", "create_agent", "create_project", "propose_agent", "create_proposed_agent", "dismiss_agent_proposal"} {
		rejects(t, client, name, object{}, "")
	}
	for _, args := range []object{{"run_id": "../../projects"}, {"run_id": uuid.Nil.String()}, {"run_id": uuid.NewString(), "unexpected": "ignored?"}} {
		rejects(t, client, "get_run", args, "")
	}
	rejects(t, client, "list_projects", object{"offset": -1}, "offset")
	rejects(t, client, "list_projects", object{"limit": 101}, "limit")
	rejects(t, client, "get_run_events", object{"run_id": uuid.NewString(), "after": -1}, "after")
	rejects(t, client, "read_artifact", object{"run_id": uuid.NewString(), "artifact_id": uuid.NewString(), "limit": 32001}, "limit")
	if requests.Load() != 0 {
		t.Fatal("invalid call reached API")
	}
	if call(t, client, "list_projects", object{})["total"] != float64(0) || requests.Load() != 1 {
		t.Fatal("read-only inspection failed")
	}
}

func TestControlMCPRejectsOldAPIAndBoundsHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"old API", 200, `{"components":{"schemas":{"RunCreate":{"properties":{}}}}}`, "upgrade Circular"},
		{"unavailable", 503, `{"detail":"maintenance"}`, "maintenance"},
		{"redirect", 302, "", "302"},
		{"oversized", 200, strings.Repeat("x", (8<<20)+1), "8 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					posts.Add(1)
				}
				if test.status == 302 {
					w.Header().Set("Location", "http://127.0.0.1:1/should-not-follow")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer api.Close()
			client := connect(t, controlmcp.Config{APIURL: api.URL})
			rejects(t, client, "start_run", object{"task_id": uuid.NewString(), "agent_id": uuid.NewString(), "request_key": "intent"}, test.want)
			if posts.Load() != 0 {
				t.Fatal("launch sent without retry support")
			}
		})
	}
	for _, address := range []string{"file:///tmp", "http://user:secret@localhost", "http://localhost?q=1", "http://localhost#secret", "http://localhost/arbitrary"} {
		if _, err := controlmcp.New(controlmcp.Config{APIURL: address}); err == nil {
			t.Fatal("invalid URL accepted", address)
		}
	}
}
