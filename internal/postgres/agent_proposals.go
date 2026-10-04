package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/agentproposals"
)

// RecordAgentProposal locks the Run first and saves a bounded, deduplicated
// recommendation. It never inserts into agents or runs.
func RecordAgentProposal(ctx context.Context, tx pgx.Tx, run uuid.UUID, draft agentproposals.Draft) (uuid.UUID, error) {
	input, err := agentproposals.Normalize(draft.Input)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(draft.ID)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, agentproposals.ErrInvalid
	}
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT id FROM runs WHERE id=$1 FOR UPDATE", run).Scan(&runID); err != nil {
		return uuid.Nil, err
	}
	fingerprint := input.Fingerprint()
	var existing uuid.UUID
	err = tx.QueryRow(ctx, "SELECT id FROM agent_proposals WHERE run_id=$1 AND fingerprint=$2", run, fingerprint).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM agent_proposals WHERE run_id=$1", run).Scan(&count); err != nil {
		return uuid.Nil, err
	}
	if count >= agentproposals.MaxProposals {
		return uuid.Nil, agentproposals.ErrLimit
	}
	config, _ := json.Marshal(input.Config())
	_, err = tx.Exec(ctx, "INSERT INTO agent_proposals(id,run_id,fingerprint,name,purpose,instructions,backend_config,model_reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, run, fingerprint, input.Name, input.Purpose, input.Instructions, config, input.ModelReason)
	return id, err
}
