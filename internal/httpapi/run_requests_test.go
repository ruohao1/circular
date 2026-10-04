package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestLaunchKeysSerializeRetriesAndRejectConflictingPayloads(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Launch retries"})
	agent := f.create(t, "agents", map[string]any{"name": "Simulator", "project_id": project["id"]})
	other := f.create(t, "agents", map[string]any{"name": "Other", "project_id": project["id"]})
	task := f.create(t, "tasks", map[string]any{"title": "One launch", "project_id": project["id"]})
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
	values["agent_id"], values["request_key"] = other["id"], "second-intent"
	second := f.create(t, "runs", values)
	if second["attempt"] != float64(2) {
		t.Fatal("new intent did not create another attempt", second)
	}
	delete(values, "request_key")
	if f.create(t, "runs", values)["attempt"] != float64(3) {
		t.Fatal("unkeyed legacy behavior changed")
	}
	for _, key := range []string{"", "  "} {
		values["request_key"] = key
		bad, _ := json.Marshal(values)
		f.request(t, "POST", "/api/v1/runs", string(bad), 422)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM runs").Scan(&count); err != nil || count != 3 {
		t.Fatal("unexpected attempts", err, count)
	}
}
