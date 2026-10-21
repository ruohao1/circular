package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func externalReviewFixture(t *testing.T) (planningFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := planningSeed(t)
	if _, err := f.p.Exec(t.Context(), `UPDATE repositories SET name='fixture/private-source',clone_url='https://github.com/fixture/private-source.git',external_refs='{"github":{"repository_id":"101","installation_id":"42"}}' WHERE id=$1`, f.repo); err != nil {
		t.Fatal(err)
	}
	task := f.task(t, "External change", "ready", "{}")
	completionPolicy(t, f, task, "review_required")
	completionChecks(t, f, 0, "Tests")
	tx, _ := f.p.Begin(t.Context())
	reviewer, err := agents.EnsureReviewer(t.Context(), tx, f.project.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO pr_review_settings(project_id,reviewer_id) VALUES($1,$2)`, f.project, reviewer); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	claim := externalClaim(task)
	claim.ExpectedRevision, _ = f.versions(t, task)
	if _, err = claimWork(t, f, claim); err != nil {
		t.Fatal(err)
	}
	number := 1
	if _, err = finishReport(t, f, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: claim.SessionID, Secret: claim.Secret}, OperationID: uuid.New(), Outcome: "succeeded", Report: &completion.Report{Result: "External result", Evidence: []completion.Evidence{{Key: "tests", Detail: "External agent says passed"}}, PullRequestNumber: &number}}); err != nil {
		t.Fatal(err)
	}
	return f, task, claim.SessionID
}

func prepareExternalReview(t *testing.T, f planningFixture, session uuid.UUID) prreviews.LaunchSnapshot {
	t.Helper()
	snapshot, err := postgres.NewPRReviewStore(f.p).SourceForWork(t.Context(), session, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PR.BaseSHA = strings.Repeat("a", 40)
	snapshot.PR.HeadSHA = strings.Repeat("b", 40)
	snapshot.PR.BaseRef = "main"
	snapshot.PR.HeadRef = "external/change"
	snapshot.PR.URL = "https://github.com/fixture/private-source/pull/1"
	snapshot, err = prreviews.SealSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestReviewExternalLaunchAndReplay(t *testing.T) {
	f, task, session := externalReviewFixture(t)
	store := postgres.NewPRReviewStore(f.p)
	snapshot := prepareExternalReview(t, f, session)
	if snapshot.SourceRunID != uuid.Nil || snapshot.Version != 2 || snapshot.Source == nil || snapshot.Source.WorkSessionID == nil || *snapshot.Source.WorkSessionID != session || len(snapshot.Evidence) != 1 || snapshot.Evidence[0].Limitation == "" {
		t.Fatal("untrusted evidence not labelled", snapshot)
	}
	req := prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}
	review, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req})
	if err != nil {
		t.Fatal(err)
	}
	if review.Report != nil || review.Assessment != prreviews.AssessmentPending {
		t.Fatal("coding claim became independent review", review)
	}
	for _, query := range []string{
		`UPDATE runs SET parent_run_id=$2 WHERE id=$1`,
		`UPDATE pr_reviews SET source_run_id=$2 WHERE run_id=$1`,
	} {
		if _, err = f.p.Exec(t.Context(), query, review.RunID, review.RunID); err == nil {
			t.Fatal("external review accepted native identity", query)
		}
	}
	var coding, native, sessions int
	var parent *uuid.UUID
	if err = f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs WHERE kind='coding'),(SELECT count(*) FROM work_sessions WHERE kind='native'),(SELECT count(*) FROM external_session_runs),parent_run_id FROM runs WHERE id=$1`, review.RunID).Scan(&coding, &native, &sessions, &parent); err != nil || coding != 0 || native != 0 || sessions != 0 || parent != nil {
		t.Fatal("synthetic native identity", coding, native, sessions, parent, err)
	}
	if _, err = f.move(t, task, "ready", "end", nil); err != nil {
		t.Fatal(err)
	}
	replay, err := store.ReplaySource(t.Context(), *snapshot.Source, req)
	if err != nil || replay == nil || replay.ID != review.ID {
		t.Fatal("lost acknowledgement", replay, err)
	}
	req.RequestKey = uuid.New()
	if _, err = store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req}); err == nil {
		t.Fatal("superseded source launched")
	}
}

func TestCompletionReviewLatestRequestedAndScope(t *testing.T) {
	f, task, session := externalReviewFixture(t)
	store := postgres.NewPRReviewStore(f.p)
	snapshot := prepareExternalReview(t, f, session)
	req := prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}
	first, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req})
	if err != nil {
		t.Fatal(err)
	}
	testsupport.CompletePRReview(t, f.p, first, t.TempDir(), emptyReviewReport())
	for _, status := range []string{"queued", "failed", "cancelled"} {
		req.RequestKey = uuid.New()
		req.Mode = "again"
		req.PreviousReviewID = first.ID
		next, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req})
		if err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			if _, err = f.p.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, next.RunID, status); err != nil {
				t.Fatal(err)
			}
		}
		var latest uuid.UUID
		if err = f.p.QueryRow(t.Context(), `SELECT latest_review_id FROM task_completions WHERE session_id=$1`, session).Scan(&latest); err != nil || latest != next.ID {
			t.Fatal("fell back to older attempt", latest, err)
		}
	}
	for _, change := range []string{"task", "repository", "session"} {
		forged := snapshot
		switch change {
		case "task":
			forged.TaskID = uuid.New()
		case "repository":
			forged.RepositoryID = uuid.New()
		case "session":
			id := uuid.New()
			forged.Source = &prreviews.Source{WorkSessionID: &id}
		}
		forged, _ = prreviews.SealSnapshot(forged)
		req = prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: forged.InputFingerprint, Mode: "normal"}
		if _, err = store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: forged, Request: req}); err == nil {
			t.Fatal("cross-scope launch", change)
		}
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE tasks SET status='completed' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	req = prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}
	if _, err = store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req}); !errors.Is(err, prreviews.ErrSourceInvalid) {
		t.Fatal("completed task launched", err)
	}
}

func TestCompletionReviewIntentFencedByDone(t *testing.T) {
	f, task, session := externalReviewFixture(t)
	snapshot := prepareExternalReview(t, f, session)
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE tasks SET status='completed' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	started, result := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := postgres.NewPRReviewStore(f.p).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), Mode: "normal", ExpectedInputFingerprint: snapshot.InputFingerprint}})
		result <- err
	}()
	<-started
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, prreviews.ErrSourceInvalid) {
		t.Fatal("intent crossed final stage transaction", err)
	}
	var count int
	if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM pr_review_launch_intents`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
