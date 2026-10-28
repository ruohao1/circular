package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestTaskProposalLinearReuseAndSourceAmendment(t *testing.T) {
	f := planningSeed(t)
	source := json.RawMessage(`{"linear":{"issue_id":"fixture-issue","account_id":"fixture-workspace","identifier":"TEST-1","url":"https://linear.app/fixture/issue/TEST-1"}}`)
	propose := func(description string) (uuid.UUID, *postgres.TaskProposal) {
		t.Helper()
		var id uuid.UUID
		var p *postgres.TaskProposal
		err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			id, p, e = postgres.ProposeLinearTask(ctx, tx, f.project, f.repo, "Source work", description, source)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		return id, p
	}
	id, p := propose("Original scope")
	if id != uuid.Nil || p == nil || p.TaskID != nil {
		t.Fatal(id, p)
	}
	_, retry := propose("Original scope")
	if retry.ID != p.ID {
		t.Fatal("pending import duplicated")
	}
	id = testsupport.ApproveProposal(t, f.p, p.ID)
	reused, p := propose("Original scope")
	if p != nil || reused != id {
		t.Fatal("unchanged approval not reused", reused, p)
	}
	otherRepo := uuid.New()
	if _, err := f.p.Exec(t.Context(), `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Other','/other','main','{}')`, otherRepo, f.project); err != nil {
		t.Fatal(err)
	}
	err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := postgres.ProposeLinearTask(ctx, tx, f.project, otherRepo, "Source work", "Original scope", source)
		return err
	})
	if !errors.Is(err, postgres.ErrTaskProposalConflict) {
		t.Fatal("source repository change should conflict before proposing", err)
	}
	_, amend := propose("Changed scope")
	if amend == nil || amend.TargetTaskID == nil || *amend.TargetTaskID != id {
		t.Fatal(amend)
	}
	var description string
	if err := f.p.QueryRow(t.Context(), `SELECT description FROM tasks WHERE id=$1`, id).Scan(&description); err != nil || description != "Original scope" {
		t.Fatal("unapproved source changed Task", description, err)
	}
	if approved := testsupport.ApproveProposal(t, f.p, amend.ID); approved != id {
		t.Fatal("amendment replaced Task")
	}
	if reused, p = propose("Changed scope"); reused != id || p != nil {
		t.Fatal(reused, p)
	}
	var count int
	if err := f.p.QueryRow(t.Context(), `SELECT count(*) FROM tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Model-supplied revisions cannot overwrite provider-verified source content.
	_, tampered := propose("Provider revision")
	err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		tampered.Draft.Description = "Agent replacement"
		changed, e := postgres.ReviseTaskProposal(ctx, tx, tampered.ID, tampered.Revision, tampered.ExpectedTaskRevision, tampered.Draft)
		if e != nil {
			return e
		}
		*tampered = changed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decideProposal(t, f, *tampered, "approved"); !errors.Is(err, postgres.ErrTaskProposalConflict) {
		t.Fatal("changed verified source accepted", err)
	}
}

func TestTaskProposalSemanticEditsStayPending(t *testing.T) {
	for _, field := range []string{"title", "description", "repository", "dependencies", "policy"} {
		t.Run(field, func(t *testing.T) {
			f := planningSeed(t)
			id := f.task(t, "Original", "ready", "{}")
			rev, board := f.versions(t, id)
			var p *postgres.TaskProposal
			err := proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
				if field == "policy" {
					var e error
					p, e = postgres.SetTaskCompletionPolicyOrPropose(ctx, tx, id, rev, completion.Policy{Mode: "review_required"})
					return e
				}
				patch := postgres.TaskPlanningPatch{}
				switch field {
				case "title":
					patch.Title = strptr("Changed title")
				case "description":
					patch.Description = strptr("Changed instructions")
				case "repository":
					patch.RepositorySet = true
				case "dependencies":
					patch.PrerequisitesSet = true
					patch.PrerequisiteTaskIDs = []uuid.UUID{f.task(t, "Prerequisite", "open", "{}")}
				}
				_, proposal, e := postgres.UpdateTaskPlanningOrPropose(ctx, tx, id, rev, patch)
				p = proposal
				return e
			})
			if err != nil || p == nil || p.Status != "pending" {
				t.Fatal(p, err)
			}
			actualRev, actualBoard := f.versions(t, id)
			if actualRev != rev || (field != "dependencies" && actualBoard != board) {
				t.Fatal("proposal changed Task revision or board", actualRev, actualBoard)
			}
			if err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error { return postgres.RequireTaskApproval(ctx, tx, id) }); err != nil {
				t.Fatal("old scope lost approval", err)
			}
			if _, err = decideProposal(t, f, *p, "approved"); err != nil {
				t.Fatal(err)
			}
			actualRev, _ = f.versions(t, id)
			if actualRev <= rev {
				t.Fatal("approved edit not applied")
			}
		})
	}
}

func TestTaskProposalSessionExpiryWhileWaitingForProjectLock(t *testing.T) {
	f := planningSeed(t)
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: proposalDraft(f)})
	if _, err := f.p.Exec(t.Context(), `INSERT INTO approval_sessions(token_hash,user_id,login,app_client_id,expires_at) VALUES('expiry-test','101','reviewer','fixture',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	locker, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(t.Context())
	if _, err = locker.Exec(t.Context(), `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, f.project); err != nil {
		t.Fatal(err)
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var pid int
	if err = tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, e := postgres.DecideTaskProposal(ctx, tx, p.ID, p.Revision, "approved", postgres.TaskApprover{ID: "101", Login: "reviewer", SessionHash: "expiry-test"}, "")
		result <- e
	}()
	blocked := false
	for !blocked && ctx.Err() == nil {
		if err = f.p.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			time.Sleep(time.Millisecond)
		}
	}
	if !blocked {
		t.Fatal("decision did not wait for project lock")
	}
	if _, err = f.p.Exec(ctx, `UPDATE approval_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash='expiry-test'`); err != nil {
		t.Fatal(err)
	}
	if err = locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, postgres.ErrTaskProposalSession) {
		t.Fatal("expired queued decision accepted", err)
	}
}

func TestTaskProposalAcceptanceCriteriaAreVersionedExecutionScope(t *testing.T) {
	f := planningSeed(t)
	p := newProposal(t, f, postgres.TaskProposalInput{ProjectID: f.project, RequestKey: uuid.NewString(), Draft: proposalDraft(f)})
	approved, err := decideProposal(t, f, p, "approved")
	if err != nil {
		t.Fatal(err)
	}
	id := *approved.TaskID
	var in postgres.TaskProposalInput
	err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		in, e = postgres.TaskProposalDraft(ctx, tx, id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := f.versions(t, id)
	in.Draft.AcceptanceCriteria = []string{"Export includes the selected columns only"}
	amendment := newProposal(t, f, in)
	if _, err = decideProposal(t, f, amendment, "approved"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.versions(t, id)
	if after <= rev {
		t.Fatal("acceptance criteria changed without a Task revision")
	}
	var run uuid.UUID
	err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		r, e := postgres.CreateRun(ctx, tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
		run = r.RunID
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.NewQueue(f.p).Acquire(t.Context(), "approval-context"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewResources(f.p, "approval-context")
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithRun(t.Context(), run, func(r *postgres.RunResources) error {
		input, e := r.InvocationInput()
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(input)
		if !bytes.Contains(raw, []byte("Export includes the selected columns only")) {
			t.Fatal("approved criteria missing from frozen input", string(raw))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = proposalTx(t, f, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		in, e = postgres.TaskProposalDraft(ctx, tx, id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Draft.AcceptanceCriteria = []string{"Different work while owned"}
	in.RequestKey = uuid.NewString()
	pending := newProposal(t, f, in)
	if _, err = decideProposal(t, f, pending, "approved"); !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("changed criteria during active work", err)
	}
}
