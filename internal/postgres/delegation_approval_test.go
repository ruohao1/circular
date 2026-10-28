package postgres_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"testing"
)

func TestDelegationApprovalWaitAndRefusalResume(t *testing.T) {
	for _, revision := range []string{"rejected", "pending", "approved"} {
		t.Run(revision, func(t *testing.T) {
			f := delegationAdmissionFixture(t)
			record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if record.Status != "awaiting_approval" || record.ChildTaskID != nil || record.ChildRunID != nil || record.ProposalID == nil {
				t.Fatal(record)
			}
			q := postgres.NewQueue(f.pool)
			if claim, err := q.Acquire(t.Context(), "idle-worker"); err != nil || claim != nil {
				t.Fatal("pending approval executed", claim, err)
			}
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			p, err := postgres.ReadTaskProposal(t.Context(), tx, *record.ProposalID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = postgres.DecideTaskProposal(t.Context(), tx, p.ID, p.Revision, "rejected", postgres.TaskApprover{ID: "101", Login: "reviewer"}, "Continue the original task"); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if revision != "rejected" {
				tx, err = f.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				p, err = postgres.ReviseTaskProposal(t.Context(), tx, p.ID, p.Revision, p.ExpectedTaskRevision, p.Draft)
				if err != nil {
					t.Fatal(err)
				}
				if revision == "approved" {
					if _, err = postgres.DecideTaskProposal(t.Context(), tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "reviewer"}, ""); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			claim, err := q.Acquire(t.Context(), "resume-worker")
			if err != nil || claim == nil || claim.RunID != f.id || !claim.Continuation {
				t.Fatal(claim, err)
			}
			store, _ := postgres.NewResources(f.pool, "resume-worker")
			store, _ = store.ForInvocation(2)
			if err = store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
				_, checkpoint, result, err := r.ContinuationContext()
				if err == nil && (result.Outcome != "approval_rejected" || result.ChildRunID != uuid.Nil || checkpoint.SourceCommit != f.checkpoint.SourceCommit) {
					t.Fatal(result, checkpoint)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			wantTasks := 1
			if revision == "approved" {
				wantTasks = 2
			}
			var tasks, runs int
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM tasks),(SELECT count(*) FROM runs)`).Scan(&tasks, &runs); err != nil || tasks != wantTasks || runs != 1 {
				t.Fatal(tasks, runs, err)
			}
		})
	}
}
func TestDelegationApprovalDoesNotLaunchUntilWorkerAdmission(t *testing.T) {
	f := delegationAdmissionFixture(t)
	record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := postgres.ReadTaskProposal(t.Context(), tx, *record.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := postgres.DecideTaskProposal(t.Context(), tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "reviewer"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("approval launched", runs, err)
	}
	q := postgres.NewQueue(f.pool)
	claim, err := q.Acquire(t.Context(), "child-worker")
	if err != nil || claim == nil || claim.RunID == f.id {
		t.Fatal(claim, err)
	}
	var task uuid.UUID
	if err = f.pool.QueryRow(t.Context(), `SELECT task_id FROM runs WHERE id=$1`, claim.RunID).Scan(&task); err != nil || task != *out.TaskID {
		t.Fatal(task, err)
	}
	if other, err := q.Acquire(t.Context(), "other-worker"); err != nil || other != nil {
		t.Fatal("duplicate child", other, err)
	}
}

func approvePendingDelegation(t *testing.T, f supervisorFixture) {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var id uuid.UUID
	if err = tx.QueryRow(t.Context(), `SELECT proposal_id FROM delegations WHERE parent_run_id=$1`, f.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	p, err := postgres.ReadTaskProposal(t.Context(), tx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.DecideTaskProposal(t.Context(), tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "fixture-reviewer"}, ""); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = postgres.AdmitApprovedDelegationForTest(postgres.NewQueue(f.pool), t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedDelegationRechecksChangedAgentPolicy(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE agents SET enabled=false WHERE id=$1`,
		`UPDATE agents SET instructions='Changed instructions' WHERE id=$1`,
		`DELETE FROM agent_delegation_targets WHERE target_agent_id=$1`,
	} {
		t.Run(mutation, func(t *testing.T) {
			f := delegationAdmissionFixture(t)
			record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			testsupport.ApproveProposal(t, f.pool, *record.ProposalID)
			if _, err = f.pool.Exec(t.Context(), mutation, f.target); err != nil {
				t.Fatal(err)
			}
			claim, err := postgres.NewQueue(f.pool).Acquire(t.Context(), "resume-worker")
			if err != nil || claim == nil || claim.RunID != f.id || !claim.Continuation {
				t.Fatal(claim, err)
			}
			var outcome string
			var runs int
			if err = f.pool.QueryRow(t.Context(), `SELECT result->>'outcome',(SELECT count(*) FROM runs) FROM delegations WHERE id=$1`, record.ID).Scan(&outcome, &runs); err != nil || outcome != "admission_rejected" || runs != 1 {
				t.Fatal(outcome, runs, err)
			}
		})
	}
}
