package controlmcp_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskProposalMCPHasNoConsentTool(t *testing.T) {
	_, config, project, _ := taskWorkMCP(t)
	client := connect(t, config)
	p := call(t, client, "propose_task", object{"project_id": project, "request_key": uuid.NewString(), "proposer": "Codex", "draft": object{"title": "Export CSV", "purpose": "Share report", "description": "Export selected report", "acceptance_criteria": []string{"Rows match report"}, "open_questions": []string{}, "prerequisite_task_ids": []string{}, "sources": []any{}, "completion_policy": object{"mode": "direct_completion", "requirements": []any{}}}})["proposal"].(object)
	if p["status"] != "pending" || p["task_id"] != nil || p["review_url"] == nil {
		t.Fatal(p)
	}
	got := call(t, client, "get_task_proposal", object{"proposal_id": p["id"]})["proposal"].(object)
	if got["revision"] != float64(1) {
		t.Fatal(got)
	}
	config.ReadOnly = true
	readonly := connect(t, config)
	tools, err := readonly.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	read := false
	for _, tool := range tools.Tools {
		if tool.Name == "get_task_proposal" {
			read = true
		}
		if tool.Name == "propose_task" || tool.Name == "revise_task_proposal" || tool.Name == "approve_task_proposal" || tool.Name == "reject_task_proposal" {
			t.Fatal("decision or mutation tool exposed", tool.Name)
		}
	}
	if !read {
		t.Fatal("missing proposal read")
	}
	tools, err = client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "approve_task_proposal" || tool.Name == "reject_task_proposal" {
			t.Fatal("model can decide", tool.Name)
		}
	}
}

func TestTaskProposalMCPPreservesPlanningFieldsOnRevision(t *testing.T) {
	draft := object{"title": "Export", "purpose": "Share", "description": "Scope", "acceptance_criteria": []string{"Complete"}, "open_questions": []string{}, "prerequisite_task_ids": []string{}, "sources": []any{}, "completion_policy": object{"mode": "direct_completion", "requirements": []any{}}, "priority": "urgent", "preferred_agent_id": uuid.NewString(), "planned_start_date": "2026-10-28", "planned_finish_date": "2026-10-29"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in object
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(object{"draft": in["draft"]})
	}))
	defer server.Close()
	c := connect(t, controlmcp.Config{APIURL: server.URL})
	got := call(t, c, "revise_task_proposal", object{"proposal_id": uuid.NewString(), "expected_revision": 1, "draft": draft})["proposal"].(object)["draft"].(object)
	for _, key := range []string{"priority", "preferred_agent_id", "planned_start_date", "planned_finish_date"} {
		if got[key] != draft[key] {
			t.Fatal("planning field lost", key, got)
		}
	}
}
