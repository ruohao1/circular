package postgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func completionPolicy(t *testing.T, f planningFixture, id uuid.UUID, mode string) {
	t.Helper()
	rev, _ := f.versions(t, id)
	_, err := workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
		return true, postgres.SetTaskCompletionPolicy(t.Context(), tx, id, rev, completion.Policy{Mode: mode, Requirements: []completion.Requirement{{Key: "tests", Description: "Show tests"}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	testsupport.ApproveTask(t, f.p, id)
}
func completionChecks(t *testing.T, f planningFixture, revision int64, name string) completion.CheckConfig {
	t.Helper()
	out, err := workTx(t, f.p, func(tx pgx.Tx) (completion.CheckConfig, error) {
		return postgres.SetRepositoryChecks(t.Context(), tx, f.repo, revision, completion.CheckConfig{Checks: []completion.RequiredCheck{{WorkflowID: "123", AppID: "42", WorkflowPath: ".github/workflows/ci.yml", Name: name}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCompletionAdmissionFreeze(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Implement", "ready", "{}")
	if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET completion_policy=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := claimWork(t, f, externalClaim(id)); err == nil {
		t.Fatal("unset policy claimed")
	}
	_, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.RunLaunchResult, error) {
		return postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
	})
	if err == nil {
		t.Fatal("unset policy launched")
	}
	completionPolicy(t, f, id, "review_required")
	in := externalClaim(id)
	in.ExpectedRevision, _ = f.versions(t, id)
	if _, err := claimWork(t, f, in); err == nil {
		t.Fatal("review work admitted without checks")
	}
	var count int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs)+(SELECT count(*) FROM work_sessions)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed admission wrote work", count, err)
	}
	config := completionChecks(t, f, 0, "Go tests")
	if _, err := claimWork(t, f, in); err != nil {
		t.Fatal(err)
	}
	var before, after []byte
	var session uuid.UUID
	if err := f.p.QueryRow(t.Context(), `SELECT completion_contract,completion_session_id FROM tasks WHERE id=$1`, id).Scan(&before, &session); err != nil || session != in.SessionID {
		t.Fatal(session, err)
	}
	var c completion.Contract
	if err := json.Unmarshal(before, &c); err != nil || c.Fingerprint == "" || c.CheckConfig.Revision != config.Revision {
		t.Fatal(c, err)
	}
	completionChecks(t, f, config.Revision, "Different checks")
	if err := f.p.QueryRow(t.Context(), `SELECT completion_contract FROM tasks WHERE id=$1`, id).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatal("frozen contract changed", err)
	}
	rev, _ := f.versions(t, id)
	_, err = workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
		return true, postgres.SetTaskCompletionPolicy(t.Context(), tx, id, rev, completion.Policy{Mode: "direct_completion"})
	})
	if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("policy weakened", err)
	}
	if _, err := f.patch(t, id, rev, postgres.TaskPlanningPatch{Priority: strptr("high")}); err != nil {
		t.Fatal("metadata locked", err)
	}
	if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET completion_contract=NULL WHERE id=$1`, id); err == nil {
		t.Fatal("DB allowed contract unfreeze")
	}
	if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET completion_policy='{"mode":"direct_completion","requirements":[]}' WHERE id=$1`, id); err == nil {
		t.Fatal("DB allowed policy weakening")
	}
	if _, err := f.p.Exec(t.Context(), `UPDATE work_sessions SET released_at=claimed_at,release_reason='User released' WHERE id=$1`, in.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, id, "ready", "end", nil); err != nil {
		t.Fatal(err)
	}
	rev, _ = f.versions(t, id)
	_, err = workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
		return true, postgres.SetTaskCompletionPolicy(t.Context(), tx, id, rev, completion.Policy{Mode: "direct_completion"})
	})
	if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("reopen unlocked contract", err)
	}
	in2 := externalClaim(id)
	in2.ExpectedRevision = rev
	if _, err := claimWork(t, f, in2); err != nil {
		t.Fatal("retry", err)
	}
	if err := f.p.QueryRow(t.Context(), `SELECT completion_contract FROM tasks WHERE id=$1`, id).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatal("retry adopted new checks", err)
	}
	if _, err := claimWork(t, f, in); err != nil {
		t.Fatal("historical replay stopped working", err)
	}
}

func TestCompletionAdmissionRejectsConfiguredReviewer(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Review work", "open", "{}")
	completionPolicy(t, f, id, "review_required")
	completionChecks(t, f, 0, "Tests")
	if _, err := f.p.Exec(t.Context(), `INSERT INTO pr_review_settings(project_id,reviewer_id) VALUES($1,$2) ON CONFLICT(project_id) DO UPDATE SET reviewer_id=$2`, f.project, f.agent); err != nil {
		t.Fatal(err)
	}
	_, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.RunLaunchResult, error) {
		return postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
	})
	if err == nil {
		t.Fatal("reviewer admitted as coder")
	}
	// Linear persists actionable waiting states in the same transaction.
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	_, err = postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
	if err == nil {
		t.Fatal("reviewer admitted")
	}
	var frozen bool
	if err = tx.QueryRow(t.Context(), `SELECT completion_contract IS NOT NULL FROM tasks WHERE id=$1`, id).Scan(&frozen); err != nil || frozen {
		t.Fatal("rejected launch froze the plan", frozen, err)
	}
}
