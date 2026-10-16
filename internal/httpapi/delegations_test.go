package httpapi_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestDelegationPolicyHTTP(t *testing.T) {
	f := setup(t)
	project, parent, _ := queueFixture(t, f, "Parent", false)
	target := f.create(t, "agents", map[string]any{"project_id": project, "name": "Reviewer", "backend": "fake", "instructions": "Review"})["id"].(string)
	path := "/api/v1/agents/" + parent + "/delegation-targets"
	f.request(t, "GET", path, "", 200)
	f.request(t, "PUT", path, `{"target_ids":["`+target+`"]}`, 200)
	_, foreign, _ := queueFixture(t, f, "Foreign", false)
	for _, bad := range []string{`{}`, `null`, `{"target_ids":null}`, `{"target_ids":[],"target_ids":[]}`, `{"target_ids":[],"project_id":"x"}`, `{"target_ids":["` + parent + `"]}`, `{"target_ids":["` + foreign + `"]}`, `{"target_ids":["bad"]}`} {
		f.request(t, "PUT", path, bad, 422)
	}
	var result struct {
		TargetIDs []string `json:"target_ids"`
	}
	if err := json.Unmarshal(f.request(t, "GET", path, "", 200), &result); err != nil || len(result.TargetIDs) != 1 || result.TargetIDs[0] != target {
		t.Fatal(result, err)
	}
	f.request(t, "PUT", path, `{"target_ids":[]}`, 200)
	f.request(t, "GET", "/api/v1/agents/"+uuid.NewString()+"/delegation-targets", "", 404)
}

func delegationReadFixture(t *testing.T) (fixture, string, string, string) {
	t.Helper()
	f := setup(t)
	parent := f.run(t)["id"].(string)
	child, id := uuid.NewString(), uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,parent_run_id,backend,status,attempt,external_refs) SELECT $2,task_id,agent_id,id,backend,'queued',attempt+1,'{}' FROM runs WHERE id=$1`, parent, child); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO delegations(id,parent_run_id,target_agent_id,child_task_id,child_run_id,request_key,input_fingerprint,objective_json,objective,depth,status) SELECT $1,$2,agent_id,task_id,id,'read',$4,'{"title":"Inspect","description":"Inspect"}','Inspect',1,'waiting' FROM runs WHERE id=$3`, id, parent, child, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	return f, parent, child, id
}

func TestDelegationExecutionRead(t *testing.T) {
	f, parent, child, id := delegationReadFixture(t)
	for _, run := range []string{parent, child} {
		got := decode(t, f.request(t, "GET", "/api/v1/runs/"+run+"/execution", "", 200))
		link, ok := got["delegation"].(map[string]any)
		if !ok || link["id"] != id || link["parent_run_id"] != parent || link["child_run_id"] != child || got["invocation"] != float64(1) {
			t.Fatal(got)
		}
	}
}

func TestDelegationUsageByInvocation(t *testing.T) {
	f, parent, child, _ := delegationReadFixture(t)
	for i, item := range []struct{ run, data string }{
		{parent, `{"input_tokens":3,"output_tokens":4}`},
		{parent, `{"input_tokens":10,"output_tokens":20,"invocation":1}`},
		{parent, `{"input_tokens":7,"output_tokens":8,"invocation":2}`},
		{parent, `{"input_tokens":7,"output_tokens":8,"invocation":2}`},
		{child, `{"input_tokens":100,"output_tokens":200,"invocation":1}`},
	} {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) VALUES($1,$2,$3,'usage.updated','fixture',$4,now())`, uuid.New(), item.run, i+1, item.data); err != nil {
			t.Fatal(err)
		}
	}
	got := decode(t, f.request(t, "GET", "/api/v1/runs/"+parent+"/execution", "", 200))
	usage := got["usage"].(map[string]any)
	if len(usage) != 2 || usage["input_tokens"] != float64(17) || usage["output_tokens"] != float64(28) {
		t.Fatal(usage)
	}
}
