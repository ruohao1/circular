package httpapi

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"net/http"

	"github.com/ruohao1/circular/internal/agents"
)

// Preparing a discovery Task does not queue a Run. The launcher presents the
// selected Repository and supplied Agent before the user starts execution.
func (a *api) createDiscoveryTask(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "DiscoveryCreate")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "discovery task") {
		return
	}
	defer rollback(ctx, tx)
	var name string
	err = tx.QueryRow(ctx, `SELECT name FROM repositories WHERE id=$1 AND project_id=$2`, values["repository_id"], project).Scan(&name)
	if dbError(w, err, "repository") {
		return
	}
	id, err := agents.EnsureDiscovery(ctx, tx, project)
	if dbError(w, err, "discovery agent") {
		return
	}
	var enabled bool
	if dbError(w, tx.QueryRow(ctx, `SELECT enabled FROM agents WHERE id=$1`, id).Scan(&enabled), "discovery agent") {
		return
	}
	if !enabled {
		problem(w, 409, "The repository discovery agent is disabled")
		return
	}
	repo := uuid.MustParse(values["repository_id"].(string))
	agent := uuid.MustParse(id)
	source, _ := json.Marshal(map[string]any{"circular": map[string]any{"kind": agents.DiscoveryPreset, "agent_id": id}})
	a.proposeTask(w, r, tx, postgres.TaskProposalInput{ProjectID: uuid.MustParse(project), Proposer: "repository discovery", Draft: postgres.ProposalDraft{Title: "Understand " + name, Purpose: "Understand the repository before planning work", Description: agents.DiscoveryTaskDescription, AcceptanceCriteria: []string{"Summarize the repository and proposed agent roles"}, RepositoryID: &repo, PreferredAgentID: &agent, CompletionPolicy: completion.Policy{Mode: "direct_completion", Requirements: []completion.Requirement{{Key: "discovery_report", Description: "Summarize the repository and proposed agent roles"}}}}}, source)
}
