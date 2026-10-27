package testsupport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
)

// ApproveTask gives a prepared fixture a real scope receipt. Production callers
// obtain the approver from the independent browser session instead.
func ApproveTask(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	ApproveTaskTx(t, tx, id)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func ApproveTaskTx(t *testing.T, tx pgx.Tx, id uuid.UUID) {
	t.Helper()
	if err := ApproveTaskContext(t.Context(), tx, id); err != nil {
		t.Fatal(err)
	}
}

// ApproveTaskContext prepares a scope receipt for owned integration fixtures.
func ApproveTaskContext(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	in, err := postgres.TaskProposalDraft(ctx, tx, id)
	if err != nil {
		return err
	}
	in.Proposer = "test fixture"
	in.Draft.Purpose = "Exercise the documented test case"
	in.Draft.AcceptanceCriteria = []string{"The fixture assertions hold"}
	// Prepare context without shifting the revisions under test.
	contextJSON, _ := json.Marshal(map[string]any{"purpose": in.Draft.Purpose, "acceptance_criteria": in.Draft.AcceptanceCriteria})
	if _, err = tx.Exec(ctx, `UPDATE tasks SET approval_context=$2 WHERE id=$1`, id, contextJSON); err != nil {
		return err
	}
	p, err := postgres.CreateTaskProposal(ctx, tx, in, nil)
	if err != nil {
		return err
	}
	_, err = postgres.DecideTaskProposal(ctx, tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "fixture-reviewer"}, "")
	return err
}

// ApproveProposal supplies the human step in tests that exercise an agent proposal.
func ApproveProposal(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) uuid.UUID {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	p, err := postgres.ReadTaskProposal(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	if p.Draft.Purpose == "" {
		p.Draft.Purpose = "Exercise this test"
		changed = true
	}
	if len(p.Draft.AcceptanceCriteria) == 0 {
		p.Draft.AcceptanceCriteria = []string{"The fixture assertions hold"}
		changed = true
	}
	if p.Draft.CompletionPolicy.Mode == "unresolved" {
		p.Draft.CompletionPolicy.Mode = "direct_completion"
		changed = true
	}
	if changed {
		p, err = postgres.ReviseTaskProposal(t.Context(), tx, p.ID, p.Revision, p.ExpectedTaskRevision, p.Draft)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err = postgres.DecideTaskProposal(t.Context(), tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "fixture-reviewer"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return *p.TaskID
}
