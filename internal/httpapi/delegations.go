package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func delegationLink(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*delegations.Record, error) {
	var link delegations.Record
	err := tx.QueryRow(ctx, `SELECT id,parent_run_id,target_agent_id,child_task_id,child_run_id,request_key,status FROM delegations WHERE request_key IS NOT NULL AND (parent_run_id=$1 OR child_run_id=$1)`, id).Scan(&link.ID, &link.ParentRunID, &link.TargetAgentID, &link.ChildTaskID, &link.ChildRunID, &link.RequestKey, &link.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &link, err
}

func (a *api) delegationTargets(w http.ResponseWriter, r *http.Request) {
	parent, err := uuid.Parse(r.PathValue("agent_id"))
	if err != nil || parent == uuid.Nil {
		problem(w, 422, "agent_id must be a UUID")
		return
	}
	var input struct {
		TargetIDs []uuid.UUID `json:"target_ids"`
	}
	if r.Method == http.MethodPut {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 32769))
		if err != nil || len(raw) > 32768 || prreviews.DecodeStrict(raw, &input) != nil || input.TargetIDs == nil {
			problem(w, 422, "target_ids must be an array of Agent UUIDs")
			return
		}
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Agent") {
		return
	}
	defer rollback(r.Context(), tx)
	if r.Method == http.MethodPut {
		err = postgres.SetDelegationTargets(r.Context(), tx, parent, input.TargetIDs)
		if errors.Is(err, postgres.ErrDelegationPolicy) {
			problem(w, 422, err.Error())
			return
		}
		if dbError(w, err, "Agent") {
			return
		}
	}
	targets, err := postgres.DelegationTargets(r.Context(), tx, parent)
	if dbError(w, err, "Agent") {
		return
	}
	input.TargetIDs = []uuid.UUID{}
	for _, target := range targets {
		input.TargetIDs = append(input.TargetIDs, target.AgentID)
	}
	if dbError(w, tx.Commit(r.Context()), "Agent") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, input)
}
