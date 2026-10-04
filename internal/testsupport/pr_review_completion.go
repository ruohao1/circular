package testsupport

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

// CompletePRReview is a provider-test fixture, never a production completion path.
func CompletePRReview(t *testing.T, pool *pgxpool.Pool, review prreviews.Review, root string, report prreviews.Report) {
	t.Helper()
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prreviews.CanonicalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.Write(t.Context(), review.RunID, "pr-review-report.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(t.Context(), review.RunID, content); err != nil {
		t.Fatal(err)
	}
	id := artifacts.PRReviewID(review.RunID, "pr-review-report.json")
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'pr_review_report',$3,jsonb_build_object('sha256',$4::text,'size_bytes',$5::bigint,'review_id',$6::text))`, id, review.RunID, content.URI, content.SHA256, content.SizeBytes, review.ID.String()); err != nil {
		t.Fatal(err)
	}
	assessment := prreviews.Assess(runstate.Succeeded, &prreviews.ValidatedReport{Report: report})
	if _, err = tx.Exec(t.Context(), `UPDATE pr_reviews SET candidate_report=$2,report_sha256=$3,report_artifact_id=$4,assessment=$5 WHERE id=$1`, review.ID, raw, content.SHA256, id, assessment); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE runs SET status='succeeded',finished_at=now() WHERE id=$1`, review.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO pr_review_publications(review_id,marker) VALUES($1,$2)`, review.ID, "<!-- circular:pr-review:"+review.ID.String()+" -->"); err != nil {
		t.Fatal(err)
	}
	if err = postgres.QueuePRReviewLinear(t.Context(), tx, review.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
