package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/contracts"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

type fixture struct {
	server *httptest.Server
	pool   *pgxpool.Pool
	root   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testsupport.Database(t)
	root := filepath.Join(t.TempDir(), "artifacts")
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: root, CORSOrigins: []string{"http://localhost:5173"}, SSEPollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	return fixture{server, pool, root}
}

func (f fixture) request(t *testing.T, method, path, body string, status int) json.RawMessage {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status && !(status == 0 && (response.StatusCode == 200 || response.StatusCode == 202)) {
		t.Fatalf("%s %s: %d, expected %d: %s", method, path, response.StatusCode, status, data)
	}
	return data
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f fixture) create(t *testing.T, kind string, body map[string]any) map[string]any {
	t.Helper()
	// Resource fixtures are seeded independently; proposal tests exercise creation.
	if kind == "tasks" {
		id := uuid.New()
		value := func(key, fallback string) any {
			if v, ok := body[key]; ok {
				return v
			}
			return fallback
		}
		refs := body["external_refs"]
		if refs == nil {
			refs = map[string]any{}
		}
		raw, _ := json.Marshal(refs)
		_, err := f.pool.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,repository_id,parent_task_id,status,priority,preferred_agent_id,planned_start_date,planned_finish_date,external_refs,completion_policy) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::text::date,$11::text::date,$12,'{"mode":"direct_completion","requirements":[]}')`, id, body["project_id"], body["title"], value("description", ""), body["repository_id"], body["parent_task_id"], value("status", "open"), value("priority", "none"), body["preferred_agent_id"], body["planned_start_date"], body["planned_finish_date"], raw)
		if err != nil {
			t.Fatal(err)
		}
		if text, _ := body["description"].(string); strings.TrimSpace(text) != "" {
			testsupport.ApproveTask(t, f.pool, id)
		}
		return decode(t, f.request(t, "GET", "/api/v1/tasks/"+id.String(), "", 200))
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, f.request(t, "POST", "/api/v1/"+kind, string(raw), 201))
}

func (f fixture) run(t *testing.T) map[string]any {
	t.Helper()
	project := f.create(t, "projects", map[string]any{"name": "API fixture"})
	agent := f.create(t, "agents", map[string]any{"name": "Engineer", "project_id": project["id"]})
	repository := f.create(t, "repositories", map[string]any{"project_id": project["id"], "name": "Source", "clone_url": "/fixture"})
	task := f.create(t, "tasks", map[string]any{"title": "Exercise HTTP", "description": "Prepared requirements", "repository_id": repository["id"], "project_id": project["id"]})
	f.directCompletion(t, task["id"].(string))
	return f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agent["id"]})
}

func TestHTTPContractAndValidationDoNotRequireDatabaseAccess(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/api/v1/health", "", 200}, {"GET", "/openapi.json", "", 200}, {"POST", "/api/v1/projects", `{"name":""}`, 422}, {"POST", "/api/v1/projects", `{"name":null}`, 422}, {"POST", "/api/v1/projects", `{}`, 422}, {"POST", "/api/v1/projects", `[]`, 422}, {"POST", "/api/v1/projects", `{} {}`, 422}, {"GET", "/api/v1/runs/not-a-uuid", "", 422}, {"GET", "/api/v1/runs/" + uuid.NewString() + "/events?limit=1001", "", 422}, {"GET", "/api/v1/runs/" + uuid.NewString() + "/events?after=-1", "", 422}} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if response.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
		if test.path == "/openapi.json" && !bytes.Equal(response.Body.Bytes(), contracts.OpenAPI) {
			t.Fatal("served contract differs from checked-in contract")
		}
	}
}

func TestResourceCreationPreservesDefaultsScopeAndExecutionProjection(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "First", "ignored": "discard"})
	if project["description"] != nil || project["ignored"] != nil {
		t.Fatal(project)
	}
	repository := f.create(t, "repositories", map[string]any{"project_id": project["id"], "name": "Source", "clone_url": "/fixture/source"})
	if repository["default_branch"] != "main" {
		t.Fatal(repository)
	}
	agent := f.create(t, "agents", map[string]any{"project_id": project["id"], "name": "Engineer"})
	if agent["backend"] != "fake" || agent["enabled"] != true || agent["instructions"] != "" {
		t.Fatal(agent)
	}
	task := f.create(t, "tasks", map[string]any{"project_id": project["id"], "repository_id": repository["id"], "title": "Implement"})
	if task["status"] != "open" || task["description"] != "" {
		t.Fatal(task)
	}
	f.approveMutation(t, "PATCH", "/api/v1/tasks/"+task["id"].(string), `{"expected_revision":1,"description":"Prepared requirements"}`)
	f.approveMutation(t, "PUT", "/api/v1/tasks/"+task["id"].(string)+"/completion-policy", `{"expected_revision":2,"policy":{"mode":"direct_completion","requirements":[]}}`)
	run := f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agent["id"]})
	if run["attempt"] != float64(1) || run["status"] != "queued" || run["worker_id"] != nil {
		t.Fatal(run)
	}
	id := run["id"].(string)
	detail := decode(t, f.request(t, "GET", "/api/v1/runs/"+id+"/execution", "", 200))
	if detail["workspace"] != nil || detail["last_event_sequence"] != float64(0) || len(detail["artifacts"].([]any)) != 0 {
		t.Fatal(detail)
	}
	if detail["usage"].(map[string]any)["output_tokens"] != float64(0) {
		t.Fatal(detail)
	}
	for _, kind := range []string{"repositories", "agents", "tasks", "runs"} {
		var rows []any
		_ = json.Unmarshal(f.request(t, "GET", "/api/v1/"+kind+"?project_id="+project["id"].(string), "", 200), &rows)
		want := 1
		if kind == "agents" {
			want = 3 // User Agent plus discovery and PR reviewer presets.
		}
		if len(rows) != want {
			t.Fatal(kind, rows)
		}
	}
	foreign := f.create(t, "projects", map[string]any{"name": "Foreign"})
	bad, _ := json.Marshal(map[string]any{"project_id": foreign["id"], "repository_id": repository["id"], "title": "Forbidden"})
	if string(f.request(t, "POST", "/api/v1/tasks", string(bad), 422)) != "{\"detail\":\"repository belongs to another project\"}\n" {
		t.Fatal("cross-project Repository accepted")
	}
	other := f.create(t, "agents", map[string]any{"project_id": foreign["id"], "name": "Other"})
	unowned := f.create(t, "tasks", map[string]any{"project_id": project["id"], "repository_id": repository["id"], "title": "Unowned", "description": "Prepared requirements"})
	bad, _ = json.Marshal(map[string]any{"task_id": unowned["id"], "agent_id": other["id"]})
	f.request(t, "POST", "/api/v1/runs", string(bad), 422)
	if _, err := f.pool.Exec(t.Context(), "UPDATE agents SET enabled=false WHERE id=$1", agent["id"]); err != nil {
		t.Fatal(err)
	}
	bad, _ = json.Marshal(map[string]any{"task_id": unowned["id"], "agent_id": agent["id"]})
	f.request(t, "POST", "/api/v1/runs", string(bad), 422)
}

func TestConcurrentAttemptsAndCancellationAreAtomicAndReplayable(t *testing.T) {
	f := setup(t)
	first := f.run(t)
	payload, _ := json.Marshal(map[string]any{"task_id": first["task_id"], "agent_id": first["agent_id"]})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			conflict := decode(t, f.request(t, "POST", "/api/v1/runs", string(payload), 409))
			if conflict["code"] != "task_owned" || conflict["owner"].(map[string]any)["run_id"] != first["id"] {
				t.Error("conflicting launch hid owner", conflict)
			}
		})
	}
	wg.Wait()
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("conflicting attempts escaped", count, err)
	}
	id := first["id"].(string)
	for range 2 {
		run := decode(t, f.request(t, "POST", "/api/v1/runs/"+id+"/cancel", "", 200))
		if run["status"] != "cancelled" || run["finished_at"] == nil {
			t.Fatal(run)
		}
	}
	var events []map[string]any
	_ = json.Unmarshal(f.request(t, "GET", "/api/v1/runs/"+id+"/events", "", 200), &events)
	if len(events) != 1 || events[0]["type"] != "run.cancelled" || events[0]["source"] != "api" || events[0]["sequence"] != float64(1) {
		t.Fatal(events)
	}
	if string(f.request(t, "GET", "/api/v1/runs/"+id+"/events?after=1", "", 200)) != "[]\n" {
		t.Fatal("cursor replay duplicated cancellation")
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET status='succeeded' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/v1/runs/"+id+"/cancel", "", 409)
}

func TestSSEReplaysAfterBothCursorsAndReleasesConnectionsOnDisconnect(t *testing.T) {
	f := setup(t)
	id := f.run(t)["id"].(string)
	for n := 1; n <= 3; n++ {
		if _, err := f.pool.Exec(t.Context(), "INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) VALUES($1,$2,$3,'agent.message.delta','fixture','{}',now())", uuid.New(), id, n); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/api/v1/runs/"+id+"/events/stream?after=2", nil)
	req.Header.Set("Last-Event-ID", "1")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal(response.Status)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "id: 3\n" {
		t.Fatal(line, err)
	}
	cancel()
	_ = response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for f.pool.Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.pool.Stat().AcquiredConns() != 0 {
		t.Fatal("disconnected SSE retained a database connection")
	}
	for _, cursor := range []string{"invalid", "-1", ""} {
		request, _ := http.NewRequest("GET", f.server.URL+"/api/v1/runs/"+id+"/events/stream", nil)
		request.Header.Set("Last-Event-ID", cursor)
		resp, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal(cursor, resp.Status)
		}
	}
}

func TestArtifactDownloadsAreRunScopedAndIntegrityChecked(t *testing.T) {
	f := setup(t)
	id := f.run(t)["id"].(string)
	runID := uuid.MustParse(id)
	store, err := artifacts.NewLocalStore(f.root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.Write(t.Context(), runID, "git-diff.patch", []byte("retained diff\n"))
	if err != nil {
		t.Fatal(err)
	}
	artifactID := uuid.New()
	metadata := map[string]any{"sha256": content.SHA256, "size_bytes": content.SizeBytes}
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'diff',$3,$4)", artifactID, runID, content.URI, metadata); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/runs/" + id + "/artifacts/" + artifactID.String() + "/content"
	if string(f.request(t, "GET", path, "", 200)) != "retained diff\n" {
		t.Fatal("artifact bytes changed")
	}
	f.request(t, "GET", strings.Replace(path, id, uuid.NewString(), 1), "", 404)
	if _, err := f.pool.Exec(t.Context(), `UPDATE artifacts SET metadata='{"sha256":"wrong"}' WHERE id=$1`, artifactID); err != nil {
		t.Fatal(err)
	}
	f.request(t, "GET", path, "", 409)
}

// Seed the explicit contract independently of the launch behavior being tested.
func (f fixture) directCompletion(t *testing.T, task string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET completion_policy='{"mode":"direct_completion","requirements":[]}' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	testsupport.ApproveTask(t, f.pool, uuid.MustParse(task))
}

// approveMutation tests the proposal response explicitly before supplying a fixture decision.
func (f fixture) approveMutation(t *testing.T, method, path, body string) json.RawMessage {
	t.Helper()
	raw := f.request(t, method, path, body, 0)
	if !bytes.Contains(raw, []byte(`"draft"`)) {
		return raw
	}
	var p postgres.TaskProposal
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	id := testsupport.ApproveProposal(t, f.pool, p.ID)
	return f.request(t, "GET", "/api/v1/tasks/"+id.String(), "", 200)
}

func (f fixture) approvePlanning(t *testing.T, path, body string) json.RawMessage {
	t.Helper()
	f.approveMutation(t, "PATCH", path, body)
	return f.request(t, "GET", path+"/planning", "", 200)
}
