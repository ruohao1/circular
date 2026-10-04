package postgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func runningReview(t *testing.T, binary bool) (*pgxpool.Pool, *postgres.Resources, prreviews.Review) {
	t.Helper()
	pool := testsupport.Database(t)
	snap := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snap, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snap.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	acquire(t, postgres.NewQueue(pool), "review-events")
	resources, err := postgres.NewResources(pool, "review-events")
	if err != nil {
		t.Fatal(err)
	}
	value := prreviews.Context{ReviewID: review.ID, RunID: review.RunID, Snapshot: snap, MergeBaseSHA: snap.PR.BaseSHA, DiffSHA256: func() string { v := sha256.Sum256([]byte("fixture diff\n")); return hex.EncodeToString(v[:]) }(), Files: []prreviews.ChangedFile{{NewPath: "x.go", Status: "added", HeadLines: 3, HeadChanged: []prreviews.LineRange{{Start: 1, End: 3}}, Binary: binary}}, Evidence: []prreviews.Evidence{}, Limitations: []string{}}
	hash, _ := prreviews.Fingerprint(value)
	if err = resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
		if err := r.PersistReviewContext(value, hash); err != nil {
			return err
		}
		if _, err := r.CreatePending(filepath.Join(t.TempDir(), review.RunID.String())); err != nil {
			return err
		}
		if _, err := r.RecordContainer("review-fixture-container"); err != nil {
			return err
		}
		_, err := r.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return pool, resources, review
}
func emptyReviewReport() prreviews.Report {
	return prreviews.Report{Summary: "Reviewed", Coverage: "complete", Findings: []prreviews.Finding{}, Checks: []prreviews.Check{}, Limitations: []string{}}
}
func TestReviewCandidateValidationAndAtomicEvents(t *testing.T) {
	pool, resources, review := runningReview(t, false)
	report := emptyReviewReport()
	appendReport := func(report prreviews.Report) error {
		return resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
			return r.AppendBackendEvent("circular", "pr_review.report.submitted", map[string]any{"report": report}, map[string]any{"report": report})
		})
	}
	report.Findings = []prreviews.Finding{{Severity: "high", Title: "Broken", Path: "private-forged.go", Side: "head", StartLine: 99, EndLine: 99, Evidence: "proof", Consequence: "breaks", SuggestedFix: "fix"}}
	if err := appendReport(report); err != nil {
		t.Fatal(err)
	}
	var candidate bool
	var last string
	if err := pool.QueryRow(t.Context(), `SELECT candidate_report IS NOT NULL,report_error FROM pr_reviews WHERE id=$1`, review.ID).Scan(&candidate, &last); err != nil || candidate || last == "" {
		t.Fatal("forged source accepted", err)
	}
	var leaked int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE run_id=$1 AND (data::text LIKE '%private-forged%' OR raw::text LIKE '%private-forged%')`, review.RunID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("rejection retained unsafe content", err)
	}
	report = emptyReviewReport()
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_review_event CHECK(type<>'pr_review.report.submitted')`); err != nil {
		t.Fatal(err)
	}
	if err := appendReport(report); err == nil {
		t.Fatal("fault did not reject event")
	}
	if err := pool.QueryRow(t.Context(), `SELECT candidate_report IS NOT NULL FROM pr_reviews WHERE id=$1`, review.ID).Scan(&candidate); err != nil || candidate {
		t.Fatal("candidate escaped rollback", err)
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT reject_review_event`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := appendReport(report); err != nil {
			t.Fatal(err)
		}
	}
	report.Summary = "Conflicting second assessment"
	if err := appendReport(report); !errors.Is(err, prreviews.ErrConflictingReport) {
		t.Fatal("conflict replaced original", err)
	}
	got, err := postgres.NewPRReviewStore(pool).Get(t.Context(), review.ID)
	if err != nil || got.Assessment != prreviews.AssessmentPending || got.Report != nil {
		t.Fatal("candidate became completed report", err)
	}
	if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
		return r.AppendBackendEvent("circular", "agent.proposed", map[string]any{}, map[string]any{})
	}); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatal("review can propose agents", err)
	}
}
func TestReviewBinaryCannotClaimCompleteCoverage(t *testing.T) {
	pool, resources, review := runningReview(t, true)
	if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
		return r.AppendBackendEvent("circular", "pr_review.report.submitted", map[string]any{"report": emptyReviewReport()}, map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	var coverage string
	if err := pool.QueryRow(t.Context(), `SELECT candidate_report->>'coverage' FROM pr_reviews WHERE id=$1`, review.ID).Scan(&coverage); err != nil || coverage != "incomplete" {
		t.Fatal(coverage, err)
	}
}
func TestCodingRunCannotSubmitReviewCandidate(t *testing.T) {
	pool := testsupport.Database(t)
	run := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "coding-review-guard")
	resources, err := postgres.NewResources(pool, "coding-review-guard")
	if err != nil {
		t.Fatal(err)
	}
	if err := resources.WithRun(t.Context(), run, func(r *postgres.RunResources) error {
		if _, err := r.CreatePending(filepath.Join(t.TempDir(), run.String())); err != nil {
			return err
		}
		if _, err := r.RecordContainer("fixture"); err != nil {
			return err
		}
		_, err := r.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := resources.WithRun(t.Context(), run, func(r *postgres.RunResources) error {
		return r.AppendBackendEvent("circular", "pr_review.report.submitted", map[string]any{"report": emptyReviewReport()}, map[string]any{})
	}); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatal("coding accepted review", err)
	}
}
