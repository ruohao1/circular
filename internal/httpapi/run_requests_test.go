package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestNativeLaunchReportsExternalOwner(t *testing.T) {
	f := setup(t)
	_, agent, task := queueFixture(t, f, "External owner", true)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET status='ready',description='Ready requirements' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	testsupport.ApproveTask(t, f.pool, uuid.MustParse(task))
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	claimed, err := postgres.ClaimExternalWork(t.Context(), tx, postgres.ExternalWorkClaim{TaskID: uuid.MustParse(task), SessionID: uuid.New(), Secret: bytes.Repeat([]byte{42}, 32), ClientName: "Claude", ExpectedRevision: 1}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw := f.request(t, "POST", "/api/v1/runs", fmt.Sprintf(`{"task_id":%q,"agent_id":%q,"request_key":"native"}`, task, agent), 409)
	var conflict struct {
		Code   string                   `json:"code"`
		Detail string                   `json:"detail"`
		Owner  postgres.WorkSessionView `json:"owner"`
	}
	if err = json.Unmarshal(raw, &conflict); err != nil || conflict.Code != "task_owned" || conflict.Owner.ID != claimed.Session.ID || !strings.Contains(conflict.Detail, claimed.Session.ID.String()) || bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("digest")) {
		t.Fatal(string(raw), err)
	}
}

func TestLaunchKeysSerializeRetriesAndRejectConflictingPayloads(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Launch retries"})
	agent := f.create(t, "agents", map[string]any{"name": "Simulator", "project_id": project["id"]})
	other := f.create(t, "agents", map[string]any{"name": "Other", "project_id": project["id"]})
	repository := f.create(t, "repositories", map[string]any{"project_id": project["id"], "name": "Source", "clone_url": "/fixture"})
	task := f.create(t, "tasks", map[string]any{"title": "One launch", "description": "Prepared requirements", "repository_id": repository["id"], "project_id": project["id"]})
	f.directCompletion(t, task["id"].(string))
	values := map[string]any{"task_id": task["id"], "agent_id": agent["id"], "request_key": "first-intent", "external_refs": map[string]any{"source": "MCP"}}
	raw, _ := json.Marshal(values)
	var first map[string]any
	created := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+"/api/v1/runs", strings.NewReader(string(raw)))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			var run map[string]any
			if err := json.NewDecoder(res.Body).Decode(&run); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if res.StatusCode != 201 && res.StatusCode != 200 {
				t.Error("launch failed", res.StatusCode, run)
				return
			}
			if res.StatusCode == 201 {
				created++
			}
			if first == nil {
				first = run
			}
			if run["id"] != first["id"] || run["attempt"] != float64(1) {
				t.Error("duplicate launch", run)
			}
		})
	}
	wg.Wait()
	if first == nil || created != 1 {
		t.Fatal("concurrent first launch must create exactly one Run", created)
	}
	values["agent_id"] = other["id"]
	conflict, _ := json.Marshal(values)
	f.request(t, "POST", "/api/v1/runs", string(conflict), 409)
	values["agent_id"] = agent["id"]
	values["external_refs"] = map[string]any{"source": "changed"}
	conflict, _ = json.Marshal(values)
	f.request(t, "POST", "/api/v1/runs", string(conflict), 409)
	if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET status='succeeded' WHERE id=$1", first["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE agents SET enabled=false WHERE id=$1", agent["id"]); err != nil {
		t.Fatal(err)
	}
	if replay := decode(t, f.request(t, "POST", "/api/v1/runs", string(raw), 200)); replay["id"] != first["id"] || replay["status"] != "succeeded" {
		t.Fatal("terminal replay changed", replay)
	}
	// Simulate completed cleanup, then explicitly prepare the next attempt.
	if _, err := f.pool.Exec(t.Context(), `UPDATE work_sessions SET outcome='succeeded',finished_at=now(),released_at=now(),release_reason='completed fixture' WHERE run_id=$1`, first["id"]); err != nil {
		t.Fatal(err)
	}
	detail := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task["id"].(string)+"/planning", "", 200))
	f.request(t, "POST", "/api/v1/tasks/"+task["id"].(string)+"/move", fmt.Sprintf(`{"expected_revision":2,"expected_board_revision":%v,"status":"ready","placement":"end"}`, detail["board_revision"]), 200)
	values["agent_id"], values["request_key"] = other["id"], "second-intent"
	second := f.create(t, "runs", values)
	if second["attempt"] != float64(2) {
		t.Fatal("new intent did not create another attempt", second)
	}
	delete(values, "request_key")
	unkeyed, _ := json.Marshal(values)
	f.request(t, "POST", "/api/v1/runs", string(unkeyed), 409)
	for _, key := range []string{"", "  "} {
		values["request_key"] = key
		bad, _ := json.Marshal(values)
		f.request(t, "POST", "/api/v1/runs", string(bad), 422)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM runs").Scan(&count); err != nil || count != 2 {
		t.Fatal("unexpected attempts", err, count)
	}
}
