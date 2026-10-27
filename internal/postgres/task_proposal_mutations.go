package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
)

// Reuse the existing edit checks, but roll back scope changes until a user decides.
// Locks acquired before the savepoint survive that rollback.
func proposeTaskMutation(ctx context.Context, tx pgx.Tx, id uuid.UUID, mutate func() error) (*TaskProposal, error) {
	if _, err := lockPlanningTask(ctx, tx, id); err != nil {
		return nil, err
	}
	before, err := TaskProposalDraft(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	scope := func() ([]byte, error) {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT `+taskApprovalScopeSQL+` FROM tasks t WHERE t.id=$1`, id).Scan(&raw)
		return raw, err
	}
	original, err := scope()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SAVEPOINT proposal_edit`); err != nil {
		return nil, err
	}
	if err = mutate(); err != nil {
		return nil, err
	}
	changed, err := scope()
	if err != nil {
		return nil, err
	}
	if bytes.Equal(original, changed) {
		_, err = tx.Exec(ctx, `RELEASE SAVEPOINT proposal_edit`)
		return nil, err
	}
	after, err := TaskProposalDraft(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT proposal_edit`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `RELEASE SAVEPOINT proposal_edit`); err != nil {
		return nil, err
	}
	after.ExpectedTaskRevision = before.ExpectedTaskRevision
	after.Proposer = "private API"
	after.RequestKey = ""
	raw, _ := json.Marshal(after)
	hash := sha256.Sum256(raw)
	after.RequestKey = "edit:" + hex.EncodeToString(hash[:])
	p, err := CreateTaskProposal(ctx, tx, after, nil)
	return &p, err
}
func UpdateTaskPlanningOrPropose(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64, patch TaskPlanningPatch) (TaskPlanningResult, *TaskProposal, error) {
	var result TaskPlanningResult
	p, err := proposeTaskMutation(ctx, tx, id, func() error {
		var err error
		result, err = UpdateTaskPlanning(ctx, tx, id, revision, patch)
		return err
	})
	return result, p, err
}
func SetTaskCompletionPolicyOrPropose(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64, policy completion.Policy) (*TaskProposal, error) {
	return proposeTaskMutation(ctx, tx, id, func() error { return SetTaskCompletionPolicy(ctx, tx, id, revision, policy) })
}
