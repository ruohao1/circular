package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
)

func proposalDraft(f planningFixture) postgres.ProposalDraft {
	return postgres.ProposalDraft{Title: "Add export", Purpose: "Share a report", Description: "Export the selected report as CSV", AcceptanceCriteria: []string{"The exported rows match the selected report"}, RepositoryID: &f.repo, PrerequisiteTaskIDs: []uuid.UUID{}, OpenQuestions: []string{}, Sources: []postgres.ProposalSource{}, CompletionPolicy: completion.Policy{Mode: "direct_completion", Requirements: []completion.Requirement{}}}
}
func proposalTx(t *testing.T, f planningFixture, fn func(context.Context, pgx.Tx) error) error {
	t.Helper()
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = fn(t.Context(), tx); err != nil {
		return err
	}
	return tx.Commit(t.Context())
}
func newProposal(t *testing.T, f planningFixture, in postgres.TaskProposalInput) postgres.TaskProposal {
	t.Helper()
	var p postgres.TaskProposal
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		p, err = postgres.CreateTaskProposal(ctx, tx, in, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func decideProposal(t *testing.T, f planningFixture, p postgres.TaskProposal, decision string) (postgres.TaskProposal, error) {
	t.Helper()
	var out postgres.TaskProposal
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = postgres.DecideTaskProposal(ctx, tx, p.ID, p.Revision, decision, postgres.TaskApprover{ID: "101", Login: "reviewer"}, "")
		return err
	})
	return out, err
}
func TestTaskProposalApprovalCreatesOnceAndBindsRevision(t *testing.T) {
	f := planningSeed(t)
	in := postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: proposalDraft(f), Proposer: "coding client"}
	p := newProposal(t, f, in)
	if p.Status != "pending" || p.Revision != 1 || p.TaskID != nil {
		t.Fatal(p)
	}
	if replay := newProposal(t, f, in); replay.ID != p.ID {
		t.Fatal("duplicate proposal", replay)
	}
	var revised postgres.TaskProposal
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		d := in.Draft
		d.Title = "Export selected report"
		var err error
		revised, err = postgres.ReviseTaskProposal(ctx, tx, p.ID, 1, 0, d)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decideProposal(t, f, p, "approved"); !errors.Is(err, postgres.ErrTaskProposalConflict) {
		t.Fatalf("stale decision: %v", err)
	}
	results := make(chan postgres.TaskProposal, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := decideProposal(t, f, revised, "approved")
			results <- out
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id *uuid.UUID
	for out := range results {
		if out.TaskID == nil {
			t.Fatal(out)
		}
		if id != nil && *id != *out.TaskID {
			t.Fatal("duplicate task")
		}
		id = out.TaskID
	}
	var tasks, runs int
	var status string
	if err = f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM tasks),(SELECT count(*) FROM runs),status FROM tasks WHERE id=$1`, id).Scan(&tasks, &runs, &status); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 0 || status != "ready" {
		t.Fatalf("tasks=%d runs=%d status=%s", tasks, runs, status)
	}
	if err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error { return postgres.RequireTaskApproval(ctx, tx, *id) }); err != nil {
		t.Fatal(err)
	}
	if _, err = decideProposal(t, f, revised, "rejected"); !errors.Is(err, postgres.ErrTaskProposalConflict) {
		t.Fatalf("opposite decision: %v", err)
	}
}
func TestTaskProposalRejectClarificationAndDependency(t *testing.T) {
	f := planningSeed(t)
	d := proposalDraft(f)
	d.OpenQuestions = []string{"Which columns?"}
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: d})
	if _, err := decideProposal(t, f, p, "approved"); !errors.Is(err, postgres.ErrTaskProposalInput) {
		t.Fatalf("unclear proposal approved: %v", err)
	}
	rejected, err := decideProposal(t, f, p, "rejected")
	if err != nil || rejected.TaskID != nil || rejected.Status != "rejected" {
		t.Fatal(rejected, err)
	}
	dep := f.task(t, "Dependency", "open", "{}")
	d = proposalDraft(f)
	d.PrerequisiteTaskIDs = []uuid.UUID{dep}
	p = newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: d})
	out, err := decideProposal(t, f, p, "approved")
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err = f.p.QueryRow(t.Context(), `SELECT status FROM tasks WHERE id=$1`, out.TaskID).Scan(&status); err != nil || status != "open" {
		t.Fatal(status, err)
	}
}
func TestTaskProposalConfirmsExistingWithoutResettingHistory(t *testing.T) {
	f := planningSeed(t)
	id := f.unapprovedTask(t, "Existing", "completed", "{}")
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error { return postgres.RequireTaskApproval(ctx, tx, id) })
	if !errors.Is(err, postgres.ErrTaskApprovalRequired) {
		t.Fatalf("historical consent invented: %v", err)
	}
	d := proposalDraft(f)
	d.Title = "Existing"
	d.Description = "original"
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), TargetTaskID: &id, ExpectedTaskRevision: 1, Draft: d})
	out, err := decideProposal(t, f, p, "approved")
	if err != nil || out.TaskID == nil || *out.TaskID != id {
		t.Fatal(out, err)
	}
	var status string
	var count int
	if err = f.p.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM tasks) FROM tasks WHERE id=$1`, id).Scan(&status, &count); err != nil || status != "completed" || count != 1 {
		t.Fatal(status, count, err)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE tasks SET description='unreviewed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error { return postgres.RequireTaskApproval(ctx, tx, id) })
	if !errors.Is(err, postgres.ErrTaskApprovalRequired) {
		t.Fatalf("changed scope reused receipt: %v", err)
	}
}
func TestTaskProposalAmendmentRejectsStaleTask(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Existing", "open", "{}")
	d := proposalDraft(f)
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), TargetTaskID: &id, ExpectedTaskRevision: 1, Draft: d})
	if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET revision=revision+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := decideProposal(t, f, p, "approved"); !errors.Is(err, postgres.ErrTaskProposalConflict) {
		t.Fatalf("stale amendment accepted: %v", err)
	}
}

func TestTaskProposalAdmissionRequiresCurrentScope(t *testing.T) {
	f := planningSeed(t)
	id := f.unapprovedTask(t, "Historical", "ready", "{}")
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		_, err := postgres.CreateRun(ctx, tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
		return err
	})
	var ineligible *postgres.WorkIneligibleError
	if !errors.As(err, &ineligible) {
		t.Fatalf("unapproved native admission: %v", err)
	}
	_, err = claimWork(t, f, externalClaim(id))
	if !errors.As(err, &ineligible) {
		t.Fatalf("unapproved external admission: %v", err)
	}
}

func TestTaskProposalRejectedRevisionCanReturnToReview(t *testing.T) {
	f := planningSeed(t)
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: proposalDraft(f)})
	if _, err := decideProposal(t, f, p, "rejected"); err != nil {
		t.Fatal(err)
	}
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p.Draft.Description = "Clarified export scope"
		p, e = postgres.ReviseTaskProposal(ctx, tx, p.ID, p.Revision, 0, p.Draft)
		return e
	})
	if err != nil || p.Status != "pending" || p.Revision != 2 || p.Decision != nil {
		t.Fatal("rejected proposal could not be revised", p, err)
	}
	if _, err = decideProposal(t, f, p, "approved"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM task_proposal_decisions WHERE proposal_id=$1`, p.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("earlier rejection receipt lost", count, err)
	}
}

func TestTaskProposalRejectsInvalidControlText(t *testing.T) {
	f := planningSeed(t)
	for _, bad := range []string{"bad\x00key", string([]byte{0xff})} {
		err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
			_, err := postgres.CreateTaskProposal(ctx, tx, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: bad, Draft: proposalDraft(f)}, nil)
			return err
		})
		if !errors.Is(err, postgres.ErrTaskProposalInput) {
			t.Fatalf("request key should be invalid input, got %v", err)
		}
	}
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: proposalDraft(f)})
	for _, bad := range []string{"bad\x00reason", string([]byte{0xff})} {
		err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
			_, err := postgres.DecideTaskProposal(ctx, tx, p.ID, p.Revision, "rejected", postgres.TaskApprover{ID: "101", Login: "reviewer"}, bad)
			return err
		})
		if !errors.Is(err, postgres.ErrTaskProposalInput) {
			t.Fatalf("decision reason should be invalid input, got %v", err)
		}
	}
}
