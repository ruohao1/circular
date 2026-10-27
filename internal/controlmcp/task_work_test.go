package controlmcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/testsupport"
)

func taskWorkMCP(t *testing.T) (*pgxpool.Pool, controlmcp.Config, string, string) {
	t.Helper()
	pool := testsupport.Database(t)
	project, repo, task := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects(id,name,description) VALUES($1,'MCP work','Use the documented roadmap')`, []any{project}},
		{`INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Source','/fixture','main','{}')`, []any{repo, project}},
		{`INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,'Prepared work','Implement the agreed change','ready','{}','{"mode":"direct_completion","requirements":[{"key":"tests","description":"Record validation"}]}')`, []any{task, project, repo}},
	} {
		if _, err := pool.Exec(t.Context(), seed.sql, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	testsupport.ApproveTask(t, pool, uuid.MustParse(task))
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	return pool, controlmcp.Config{APIURL: api.URL}, project, task
}
func TestTaskWorkMCPUsesSharedLifecycleAcrossReconnect(t *testing.T) {
	pool, config, project, task := taskWorkMCP(t)
	client := connect(t, config)
	ready := call(t, client, "list_task_planning", object{"project_id": project, "status": "ready", "limit": 1})
	if ready["groups"].([]any)[0].(object)["items"].([]any)[0].(object)["task"].(object)["id"] != task {
		t.Fatal(ready)
	}
	view := call(t, client, "get_task_work", object{"task_id": task})["work"].(object)
	if view["project"].(object)["description"] != "Use the documented roadmap" || view["task"].(object)["description"] != "Implement the agreed change" || view["completion"].(object)["policy"] == nil {
		t.Fatal(view)
	}
	claim := object{"task_id": task, "session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": view["task"].(object)["revision"], "client_name": "Codex", "client_version": "1"}
	accepted := call(t, client, "claim_task_work", claim)["work"].(object)
	if accepted["created"] != true || accepted["credential"].(object)["session_secret"] != claim["session_secret"] {
		t.Fatal(accepted)
	}
	// A fresh server/client has no in-memory session credentials or replay cache.
	client = connect(t, config)
	replay := call(t, client, "claim_task_work", claim)["work"].(object)
	if replay["created"] != false || !reflect.DeepEqual(accepted["session"], replay["session"]) {
		t.Fatal(replay)
	}
	beat := object{"session_id": claim["session_id"], "session_secret": claim["session_secret"], "sequence": 1}
	heartbeat := call(t, client, "heartbeat_task_work", beat)["work"].(object)
	if heartbeat["session"].(object)["heartbeat_sequence"] != float64(1) {
		t.Fatal(heartbeat)
	}
	finish := object{"session_id": claim["session_id"], "session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": "succeeded", "summary": "Implemented and tested"}
	rejects(t, client, "finish_task_work", finish, "report_required")
	finish["report"] = object{"result": "Implemented and tested", "evidence": []any{object{"key": "tests", "detail": "All tests passed"}}}
	result := call(t, client, "finish_task_work", finish)["work"].(object)
	resultReplay := call(t, connect(t, config), "finish_task_work", finish)["work"].(object)
	if !reflect.DeepEqual(result["receipt"], resultReplay["receipt"]) {
		t.Fatal(resultReplay)
	}
	rejects(t, client, "heartbeat_task_work", beat, "work_claim_lost")
	view = call(t, client, "get_task_work", object{"task_id": task})["work"].(object)
	if view["task"].(object)["status"] != "completed" || view["owner"] != nil {
		t.Fatal(view)
	}
	history := call(t, client, "get_work_session", object{"session_id": claim["session_id"]})["work"].(object)
	if len(history["receipts"].([]any)) != 2 {
		t.Fatal(history)
	}
	for _, value := range []object{view, history, result} {
		raw, _ := json.Marshal(value)
		if strings.Contains(string(raw), "session_secret") || strings.Contains(string(raw), claim["session_secret"].(string)) {
			t.Fatal("secret in inspection/report", string(raw))
		}
	}
	var runs, sessions int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM work_sessions)`).Scan(&runs, &sessions); err != nil || runs != 0 || sessions != 1 {
		t.Fatal(runs, sessions, err)
	}
}
func TestTaskWorkMCPReadOnlyAndRevokedOwner(t *testing.T) {
	pool, config, _, task := taskWorkMCP(t)
	rw := connect(t, config)
	config.ReadOnly = true
	ro := connect(t, config)
	claim := object{"task_id": task, "session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": 1, "client_name": "Claude"}
	call(t, rw, "claim_task_work", claim)
	call(t, ro, "get_task_work", object{"task_id": task})
	call(t, ro, "get_work_session", object{"session_id": claim["session_id"]})
	beat := object{"session_id": claim["session_id"], "session_secret": claim["session_secret"], "sequence": 1}
	finish := object{"session_id": claim["session_id"], "session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": "failed", "summary": "Stopped"}
	release := object{"task_id": task, "expected_session_id": claim["session_id"], "operation_id": uuid.NewString(), "reason": "User confirmed the client stopped"}
	for tool, args := range map[string]object{"claim_task_work": claim, "heartbeat_task_work": beat, "finish_task_work": finish, "release_task_work": release} {
		rejects(t, ro, tool, args, "")
	}
	var receipts int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM work_session_receipts`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatal(receipts, err)
	}
	released := call(t, rw, "release_task_work", release)["work"].(object)
	if released["pending_cleanup"] != false || released["receipt"].(object)["actor"] != "user" {
		t.Fatal(released)
	}
	rejects(t, rw, "heartbeat_task_work", beat, "get_task_work")
	rejects(t, rw, "finish_task_work", finish, "work_claim_lost")
}
func TestTaskWorkMCPUncertainResponseRetainsRetryIdentity(t *testing.T) {
	pool, config, _, task := taskWorkMCP(t)
	// Forward the actual mutation, then lose the response. Retrying must recover it.
	once := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		forwarded, err := http.NewRequestWithContext(r.Context(), r.Method, config.APIURL+r.URL.Path, strings.NewReader(string(body)))
		if err != nil {
			t.Error(err)
			return
		}
		forwarded.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(forwarded)
		if err != nil {
			t.Error(err)
			return
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/claim") && once {
			once = false
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		w.Write(data)
	}))
	defer proxy.Close()
	client := connect(t, controlmcp.Config{APIURL: proxy.URL})
	claim := object{"task_id": task, "session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": 1, "client_name": "Codex"}
	rejects(t, client, "claim_task_work", claim, "session_id")
	replay := call(t, connect(t, config), "claim_task_work", claim)["work"].(object)
	if replay["created"] != false {
		t.Fatal(replay)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM work_sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestTaskWorkMCPToolAnnotationsAndValidation(t *testing.T) {
	_, config, _, task := taskWorkMCP(t)
	client := connect(t, config)
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readNames := map[string]bool{"get_task_work": true, "get_work_session": true}
	writeNames := map[string]bool{"claim_task_work": true, "heartbeat_task_work": true, "finish_task_work": true, "release_task_work": true}
	for _, tool := range tools.Tools {
		if !readNames[tool.Name] && !writeNames[tool.Name] {
			continue
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != readNames[tool.Name] || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatal(tool)
		}
		delete(readNames, tool.Name)
		delete(writeNames, tool.Name)
	}
	if len(readNames)+len(writeNames) != 0 {
		t.Fatal("missing work tools", readNames, writeNames)
	}
	for _, args := range []object{
		{"task_id": task, "session_id": uuid.NewString(), "session_secret": "short", "expected_revision": 1, "client_name": "Codex"},
		{"task_id": task, "session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": 1, "client_name": "Codex", "force": true},
		{"task_id": "../projects", "session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": 1, "client_name": "Codex"},
	} {
		rejects(t, client, "claim_task_work", args, "")
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_task_work", Arguments: object{"task_id": task}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
}
