package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/postgres"
)

func proposalInput(w http.ResponseWriter, r *http.Request) (agentproposals.Input, bool) {
	values, ok := body(w, r, "AgentProposalCreate")
	if !ok {
		return agentproposals.Input{}, false
	}
	raw, _ := json.Marshal(values)
	var input agentproposals.Input
	if json.Unmarshal(raw, &input) != nil {
		problem(w, 422, agentproposals.ErrInvalid.Error())
		return input, false
	}
	input, err := agentproposals.Normalize(input)
	if err != nil {
		problem(w, 422, err.Error())
		return input, false
	}
	return input, true
}

func (a *api) agentProposals(w http.ResponseWriter, r *http.Request) {
	run, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	var exists string
	if dbError(w, a.pool.QueryRow(r.Context(), "SELECT id FROM runs WHERE id=$1", run).Scan(&exists), "run") {
		return
	}
	data, err := records(r.Context(), a.pool, "agent_proposals", "AgentProposalRead", "WHERE t.run_id=$1 ORDER BY t.created_at,t.id", run)
	if dbError(w, err, "agent proposals") {
		return
	}
	respond(w, 200, data)
}

// This also lets older Markdown reports enter the same durable review flow.
func (a *api) saveAgentProposal(w http.ResponseWriter, r *http.Request) {
	run, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	input, ok := proposalInput(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "proposal") {
		return
	}
	defer rollback(ctx, tx)
	id, err := postgres.RecordAgentProposal(ctx, tx, run, agentproposals.Draft{ID: uuid.NewString(), Input: input})
	if errors.Is(err, agentproposals.ErrLimit) {
		problem(w, 409, err.Error())
		return
	}
	if dbError(w, err, "run") {
		return
	}
	data, err := record(ctx, tx, "agent_proposals", "AgentProposalRead", "WHERE t.id=$1", id)
	if dbError(w, err, "proposal") || dbError(w, tx.Commit(ctx), "proposal") {
		return
	}
	respond(w, 201, data)
}

func (a *api) createProposedAgent(w http.ResponseWriter, r *http.Request) {
	a.resolveAgentProposal(w, r, true)
}

func (a *api) dismissAgentProposal(w http.ResponseWriter, r *http.Request) {
	a.resolveAgentProposal(w, r, false)
}

func (a *api) resolveAgentProposal(w http.ResponseWriter, r *http.Request, create bool) {
	run, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	id, ok := identifier(w, r.PathValue("proposal_id"), "path", "proposal_id")
	if !ok {
		return
	}
	var input agentproposals.Input
	if create {
		input, ok = proposalInput(w, r)
		if !ok {
			return
		}
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "proposal") {
		return
	}
	defer rollback(ctx, tx)
	// Always lock Run before proposal, matching worker ingestion. Project
	// ownership comes from this Run's Task, never from agent-provided data.
	var project string
	err = tx.QueryRow(ctx, "SELECT t.project_id FROM runs r JOIN tasks t ON t.id=r.task_id WHERE r.id=$1 FOR UPDATE OF r", run).Scan(&project)
	if dbError(w, err, "run") {
		return
	}
	var status string
	var agent *string
	err = tx.QueryRow(ctx, "SELECT status,agent_id FROM agent_proposals WHERE id=$1 AND run_id=$2 FOR UPDATE", id, run).Scan(&status, &agent)
	if dbError(w, err, "proposal") {
		return
	}
	if create && status == "dismissed" {
		problem(w, 409, "This suggestion was dismissed")
		return
	}
	if !create && status == "created" {
		problem(w, 409, "This suggestion has already been used to create an agent")
		return
	}
	if create && status == "pending" {
		config, _ := json.Marshal(input.Config())
		agentID := uuid.NewString()
		err = tx.QueryRow(ctx, `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled)
			VALUES($1,$2,$3,'codex',$4,$5,true) ON CONFLICT(project_id,name) DO NOTHING RETURNING id`, agentID, project, input.Name, input.Instructions, config).Scan(&agentID)
		if errors.Is(err, pgx.ErrNoRows) {
			problem(w, 409, "An agent with this name already exists. Choose another name.")
			return
		}
		if dbError(w, err, "agent") {
			return
		}
		agent = &agentID
		// Keep the original recommendation immutable. User edits belong to the
		// resulting Agent; retries and older reports still identify this draft.
		_, err = tx.Exec(ctx, "UPDATE agent_proposals SET status='created',agent_id=$2,updated_at=now() WHERE id=$1", id, agentID)
		if dbError(w, err, "proposal") {
			return
		}
	} else if !create && status == "pending" {
		_, err = tx.Exec(ctx, "UPDATE agent_proposals SET status='dismissed',updated_at=now() WHERE id=$1", id)
		if dbError(w, err, "proposal") {
			return
		}
	}
	var data json.RawMessage
	if create {
		data, err = record(ctx, tx, "agents", "AgentRead", "WHERE t.id=$1", *agent)
	} else {
		data, err = record(ctx, tx, "agent_proposals", "AgentProposalRead", "WHERE t.id=$1", id)
	}
	if dbError(w, err, "proposal") || dbError(w, tx.Commit(ctx), "proposal") {
		return
	}
	respond(w, 200, data)
}
