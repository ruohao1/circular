package integrations_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestReviewExternalPreparationHistoryAndPublication(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "reopened"}[reopen], func(t *testing.T) {
			f := setup(t)
			f.connect(t, "github")
			snap := testsupport.SeedExternalPRReviewSource(t, f.pool, uuid.MustParse(f.project))
			f.provider.SetReviewPR(snap.PR, "open")
			prepared, err := f.service.PrepareTaskPRReview(t.Context(), snap.TaskID.String(), "")
			if err != nil || !prepared.Ready {
				t.Fatal(prepared, err)
			}
			req := prreviews.LaunchRequest{RequestKey: uuid.New(), Mode: "normal", ExpectedInputFingerprint: prepared.Snapshot.InputFingerprint}
			review, err := f.service.LaunchTaskPRReview(t.Context(), snap.TaskID.String(), req)
			if err != nil {
				t.Fatal(err)
			}
			page, err := f.service.ListTaskPRReviews(t.Context(), snap.TaskID.String(), 20, "")
			if err != nil || len(page.Items) != 1 || page.Items[0].ID != review.ID {
				t.Fatal(page, err)
			}
			other, err := f.service.ListPRReviews(t.Context(), snap.SourceRunID.String(), 20, "")
			if err != nil || len(other.Items) != 0 {
				t.Fatal("external history appeared under zero native run", other, err)
			}
			testsupport.CompletePRReview(t, f.pool, review, f.root, prreviews.Report{Summary: "Independent review complete", Coverage: "complete", Findings: []prreviews.Finding{}, Checks: []prreviews.Check{}, Limitations: []string{}})
			if reopen {
				tx, _ := f.pool.Begin(t.Context())
				defer tx.Rollback(t.Context())
				var revision, board int64
				if err = tx.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, snap.TaskID).Scan(&revision, &board); err != nil {
					t.Fatal(err)
				}
				if _, err = postgres.MoveTask(t.Context(), tx, snap.TaskID, postgres.TaskMoveInput{ExpectedRevision: revision, ExpectedBoardRevision: board, Status: "ready", Placement: "end"}); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := f.service.ProcessPRReviewPublication(t.Context()); !worked || err != nil {
				t.Fatal(worked, err)
			}
			got, err := f.service.PRReview(t.Context(), review.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			want := "published"
			if reopen {
				want = "skipped"
			}
			if got.GitHub.Status != want || got.Linear.Status != "skipped" {
				t.Fatal(got.GitHub, got.Linear)
			}
			replayed, err := f.service.LaunchTaskPRReview(t.Context(), snap.TaskID.String(), req)
			if err != nil || replayed.ID != review.ID {
				t.Fatal("replay read mutable source", err)
			}
			var coding, reservations int
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs WHERE kind='coding'),(SELECT count(*) FROM external_effect_reservations)`).Scan(&coding, &reservations); err != nil || coding != 0 || reservations != 0 {
				t.Fatal(coding, reservations, err)
			}
		})
	}
}
