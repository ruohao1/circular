package postgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func TestReviewCompletionRequiresRetainedContextAndReport(t *testing.T) {
	for _, scenario := range []string{"missing", "clean", "finding", "incomplete", "failed", "wrong_kind", "wrong_uri"} {
		t.Run(scenario, func(t *testing.T) {
			pool, resources, review := runningReview(t, false)
			report := emptyReviewReport()
			if scenario == "finding" {
				report.Findings = []prreviews.Finding{{Severity: "high", Title: "Defect", Path: "x.go", Side: "head", StartLine: 1, EndLine: 1, Evidence: "proof", Consequence: "failure", SuggestedFix: "fix"}}
			}
			if scenario == "incomplete" {
				report.Coverage = "incomplete"
				report.Limitations = []string{"Required checks unavailable."}
			}
			if scenario != "missing" {
				if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
					return r.AppendBackendEvent("circular", "pr_review.report.submitted", map[string]any{"report": report}, map[string]any{})
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
				if scenario == "failed" {
					return r.RecordFailure("Fixture process failed", nil)
				}
				return r.BeginFinalizing()
			}); err != nil {
				t.Fatal(err)
			}
			if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.Complete() }); !errors.Is(err, postgres.ErrResourceState) {
				t.Fatal("generic completion bypassed review gate", err)
			}
			if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.CompletePRReview() }); err == nil {
				t.Fatal("unretained output completed")
			}
			store, err := artifacts.NewLocalStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var value prreviews.Context
			var worktree string
			if err = resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
				var err error
				value, err = r.ReviewContext()
				if err != nil {
					return err
				}
				state, err := r.State()
				if err != nil {
					return err
				}
				worktree = state.Workspace.WorktreePath
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			contextJSON, err := prreviews.CanonicalJSON(value)
			if err != nil {
				t.Fatal(err)
			}
			content := map[string][]byte{"pr-review-context.json": contextJSON, "pr-review-diff.patch": []byte("fixture diff\n")}
			if scenario != "missing" {
				content["pr-review-report.json"], err = prreviews.CanonicalJSON(report)
				if err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range content {
				stored, err := store.Write(t.Context(), review.RunID, name, data)
				if err != nil {
					t.Fatal(err)
				}
				if err = resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
					_, err := r.PersistReviewArtifact(worktree, name, stored)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "wrong_kind" || scenario == "wrong_uri" {
				id := artifacts.PRReviewID(review.RunID, "pr-review-context.json")
				query := `UPDATE artifacts SET kind='diff' WHERE id=$1`
				if scenario == "wrong_uri" {
					query = `UPDATE artifacts SET uri='artifact://wrong/context.json' WHERE id=$1`
				}
				if _, err := pool.Exec(t.Context(), query, id); err != nil {
					t.Fatal(err)
				}
				if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.CompletePRReview() }); err == nil {
					t.Fatal("mismatched artifact identity accepted")
				}
				return
			}
			if scenario != "failed" {
				if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.CompletePRReview() }); err != nil {
					t.Fatal(err)
				}
			}
			got, err := postgres.NewPRReviewStore(pool).Get(t.Context(), review.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := prreviews.AssessmentNoBlocking
			if scenario == "finding" {
				want = prreviews.AssessmentFindings
			}
			if scenario == "missing" || scenario == "incomplete" || scenario == "failed" {
				want = prreviews.AssessmentIncomplete
			}
			if got.Assessment != want {
				t.Fatal(got.Assessment, want)
			}
			var publications int
			if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_review_publications WHERE review_id=$1`, review.ID).Scan(&publications); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if scenario == "missing" || scenario == "failed" {
				expected = 0
			}
			if publications != expected {
				t.Fatal("publication without successful retained report", publications)
			}
			var codingDiffs int
			if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM artifacts WHERE run_id=$1 AND kind='diff'`, review.RunID).Scan(&codingDiffs); err != nil || codingDiffs != 0 {
				t.Fatal(codingDiffs, err)
			}
			if scenario == "missing" && (got.Report != nil || got.ReportError == "") {
				b, _ := json.Marshal(got)
				t.Fatal("missing report fabricated", string(b))
			}
		})
	}
}
