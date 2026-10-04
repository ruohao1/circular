package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/agents"
)

func TestProjectIncludesDiscoveryAndPreparesTaskWithoutStartingRun(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Discover this project"})
	projectID := project["id"].(string)
	var list []map[string]any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/agents?project_id="+projectID, "", 200), &list); err != nil {
		t.Fatal(err)
	}
	var discovery, reviewer map[string]any
	for _, item := range list {
		if item["preset"] == agents.DiscoveryPreset {
			discovery = item
		}
		if item["preset"] == agents.ReviewerPreset {
			reviewer = item
		}
	}
	if len(list) != 2 || discovery == nil || reviewer == nil || discovery["backend"] != "codex" || discovery["enabled"] != true || discovery["instructions"] != agents.DiscoveryInstructions {
		t.Fatal("project did not receive its discovery Agent", list)
	}
	agentID := discovery["id"].(string)
	repository := f.create(t, "repositories", map[string]any{"project_id": projectID, "name": "Project source", "clone_url": "/fixture/source"})
	path := "/api/v1/projects/" + projectID + "/discovery"
	body, _ := json.Marshal(map[string]any{"repository_id": repository["id"]})
	task := decode(t, f.request(t, "POST", path, string(body), 201))
	if task["project_id"] != projectID || task["repository_id"] != repository["id"] || task["title"] != "Understand Project source" || task["description"] != agents.DiscoveryTaskDescription {
		t.Fatal("discovery Task lost repository context", task)
	}
	reference := task["external_refs"].(map[string]any)["circular"].(map[string]any)
	if reference["kind"] != agents.DiscoveryPreset || reference["agent_id"] != agentID {
		t.Fatal("discovery Task lost Agent identity")
	}
	var agentCount, runCount int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agents WHERE project_id=$1),(SELECT count(*) FROM runs)`, projectID).Scan(&agentCount, &runCount); err != nil || agentCount != 2 || runCount != 0 {
		t.Fatal("preparation duplicated Agents or started a Run", err)
	}
	// A normal launch reuses the prepared Task and the supplied Agent.
	run := f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agentID})
	if run["backend"] != "codex" {
		t.Fatal("discovery used the wrong backend")
	}
	other := f.create(t, "projects", map[string]any{"name": "Other project"})
	otherPath := "/api/v1/projects/" + other["id"].(string) + "/discovery"
	f.request(t, "POST", otherPath, string(body), 404)
	f.request(t, "POST", path, `{"repository_id":"`+uuid.NewString()+`"}`, 404)
	f.request(t, "POST", path, `{}`, 422)
	f.request(t, "POST", "/api/v1/projects/"+uuid.NewString()+"/discovery", string(body), 404)
	if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET name='Customized discovery',enabled=false,instructions='Keep my instructions' WHERE id=$1`, agentID); err != nil {
		t.Fatal(err)
	}
	if response := f.request(t, "POST", path, string(body), 409); !strings.Contains(string(response), "disabled") {
		t.Fatal("disabled Agent was replaced")
	}
	var name, instructions string
	var enabled bool
	if err := f.pool.QueryRow(t.Context(), `SELECT name,instructions,enabled FROM agents WHERE id=$1`, agentID).Scan(&name, &instructions, &enabled); err != nil || name != "Customized discovery" || instructions != "Keep my instructions" || enabled {
		t.Fatal("customized Agent changed", err)
	}
}
