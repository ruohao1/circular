package httpapi_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
)

const proposalBody = `{"name":"Product engineer","purpose":"Build console features","instructions":"Read repository conventions and run relevant checks."}`

func TestOrchestratorModelRecommendationSurvivesStorageReviewAndCreation(t *testing.T) {
	f := setup(t)
	run := f.run(t)
	path := "/api/v1/runs/" + run["id"].(string) + "/agent-proposals"
	input := `{"name":"Execution engineer","purpose":"Maintain execution","instructions":"Read conventions and check recovery.","model":"gpt-5.6-terra","reasoning_effort":"high","model_reason":"Use Terra with high reasoning for changes spanning worker ownership and recovery."}`
	proposal := decode(t, f.request(t, "POST", path, input, 201))
	config := proposal["backend_config"].(map[string]any)
	if config["model"] != "gpt-5.6-terra" || config["reasoning_effort"] != "high" || proposal["model_reason"] != "Use Terra with high reasoning for changes spanning worker ownership and recovery." {
		t.Fatal("model recommendation was not saved", proposal)
	}
	var saved []map[string]any
	if err := json.Unmarshal(f.request(t, "GET", path, "", 200), &saved); err != nil || len(saved) != 1 || saved[0]["model_reason"] != proposal["model_reason"] {
		t.Fatal("reload lost model explanation", err, saved)
	}
	agent := decode(t, f.request(t, "POST", path+"/"+proposal["id"].(string)+"/create", input, 200))
	config = agent["backend_config"].(map[string]any)
	if config["model"] != "gpt-5.6-terra" || config["reasoning_effort"] != "high" || len(config) != 2 {
		t.Fatal("creation replaced orchestrator settings with defaults", agent)
	}
	if agent["instructions"] != "Read conventions and check recovery." {
		t.Fatal("reason leaked into instructions")
	}
}

func TestProposalReviewCreatesOnceInTheRunProjectAndDoesNotStartWork(t *testing.T) {
	f := setup(t)
	run := f.run(t)
	path := "/api/v1/runs/" + run["id"].(string) + "/agent-proposals"
	if got := string(f.request(t, "GET", path, "", 200)); got != "[]\n" {
		t.Fatal(got)
	}
	proposal := decode(t, f.request(t, "POST", path, proposalBody, 201))
	if proposal["status"] != "pending" || proposal["agent_id"] != nil || proposal["backend_config"].(map[string]any)["model"] != "gpt-6-astra" {
		t.Fatal(proposal)
	}
	retry := decode(t, f.request(t, "POST", path, proposalBody, 201))
	if retry["id"] != proposal["id"] {
		t.Fatal("save retry duplicated proposal")
	}
	var beforeAgents, beforeRuns int
	if err := f.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM agents),(SELECT count(*) FROM runs)").Scan(&beforeAgents, &beforeRuns); err != nil {
		t.Fatal(err)
	}
	createPath := path + "/" + proposal["id"].(string) + "/create"
	edited := `{"name":"UI engineer","purpose":"Build console features","instructions":"Follow the product conventions.","model":"gpt-5.6-luna","reasoning_effort":"max"}`
	var wait sync.WaitGroup
	ids := make(chan string, 6)
	for range 6 {
		wait.Go(func() { agent := decode(t, f.request(t, "POST", createPath, edited, 200)); ids <- agent["id"].(string) })
	}
	wait.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("concurrent creation duplicated agent")
		}
		first = id
	}
	agent := decode(t, f.request(t, "POST", createPath, proposalBody, 200))
	if agent["id"] != first || agent["name"] != "UI engineer" || agent["backend"] != "codex" || agent["instructions"] != "Follow the product conventions." || agent["backend_config"].(map[string]any)["reasoning_effort"] != "max" {
		t.Fatal("retry changed the reviewed agent", agent)
	}
	var project string
	var afterAgents, afterRuns int
	if err := f.pool.QueryRow(t.Context(), "SELECT project_id FROM tasks WHERE id=$1", run["task_id"]).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if agent["project_id"] != project {
		t.Fatal("created in wrong project")
	}
	if err := f.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM agents),(SELECT count(*) FROM runs)").Scan(&afterAgents, &afterRuns); err != nil || afterAgents != beforeAgents+1 || afterRuns != beforeRuns {
		t.Fatal("review created extra agents or queued a Run", err)
	}
	f.request(t, "POST", path+"/"+proposal["id"].(string)+"/dismiss", `{}`, 409)
	var listed []map[string]any
	if err := json.Unmarshal(f.request(t, "GET", path, "", 200), &listed); err != nil || len(listed) != 1 || listed[0]["status"] != "created" || listed[0]["name"] != "Product engineer" {
		t.Fatal("lost original proposal or status", err, listed)
	}
}

func TestProposalConflictDismissalOwnershipAndLimits(t *testing.T) {
	f := setup(t)
	run := f.run(t)
	path := "/api/v1/runs/" + run["id"].(string) + "/agent-proposals"
	proposal := decode(t, f.request(t, "POST", path, proposalBody, 201))
	action := path + "/" + proposal["id"].(string)
	f.request(t, "POST", action+"/create", `{"name":"Engineer","purpose":"Test","instructions":"Keep conventions"}`, 409)
	f.request(t, "POST", action+"/create", `{"name":"Bad","purpose":"Test","instructions":"Keep conventions","model":"gpt-5.6-luna","reasoning_effort":"ultra"}`, 422)
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','succeeded',2,'{}')", other, run["task_id"], run["agent_id"]); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/v1/runs/"+other+"/agent-proposals/"+proposal["id"].(string)+"/create", proposalBody, 404)
	for range 2 {
		got := decode(t, f.request(t, "POST", action+"/dismiss", `{}`, 200))
		if got["status"] != "dismissed" {
			t.Fatal(got)
		}
	}
	f.request(t, "POST", action+"/create", proposalBody, 409)
	for i := 1; i < 12; i++ {
		f.request(t, "POST", path, fmt.Sprintf(`{"name":"Role %d","purpose":"Test","instructions":"Read conventions"}`, i), 201)
	}
	f.request(t, "POST", path, `{"name":"Too many","purpose":"Test","instructions":"Read conventions"}`, 409)
	f.request(t, "POST", path, proposalBody, 201)
	f.request(t, "GET", "/api/v1/runs/"+uuid.NewString()+"/agent-proposals", "", 404)
}
