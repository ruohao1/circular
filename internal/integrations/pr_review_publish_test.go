package integrations_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func completedReview(t *testing.T) (fixture, prreviews.Review) {
	t.Helper()
	f := setup(t)
	f.connect(t, "github")
	snapshot := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(snapshot.PR, "open")
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET external_refs=jsonb_set(external_refs::jsonb,'{linear,account_id}','"20000000-0000-4000-8000-000000000004"')::json WHERE id=$1`, snapshot.TaskID); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.service.PreparePRReview(t.Context(), snapshot.SourceRunID.String(), snapshot.Reviewer.AgentID.String())
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.LaunchPRReview(t.Context(), snapshot.SourceRunID.String(), prreviews.LaunchRequest{RequestKey: uuid.New(), ReviewerID: snapshot.Reviewer.AgentID, Mode: "normal", ExpectedInputFingerprint: prepared.Snapshot.InputFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	testsupport.CompletePRReview(t, f.pool, r, f.root, prreviews.Report{Summary: "Review complete", Coverage: "complete", Findings: []prreviews.Finding{}, Checks: []prreviews.Check{}, Limitations: []string{}})
	return f, r
}
func TestReviewLostResponseRecoversWithoutAnotherPost(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "hidden"}[hidden], func(t *testing.T) {
			f, r := completedReview(t)
			marker := "<!-- circular:pr-review:" + r.ID.String() + " -->"
			for i := range 4 {
				author := "7"
				commit := r.Snapshot.PR.HeadSHA
				if i%2 == 0 {
					author = "8"
				} else {
					commit = strings.Repeat("c", 40)
				}
				f.provider.AddReviewReceipt(testsupport.FixtureGitHubReview{ID: "9", CommitID: commit, Body: marker, HTMLURL: r.Snapshot.PR.URL + "#pullrequestreview-9", AuthorID: author})
			}
			f.provider.LoseReviewResponse.Store(true)
			f.provider.HideReviews.Store(hidden)
			if worked, err := f.service.ProcessPRReviewPublication(t.Context()); !worked || err != nil {
				t.Fatal(worked, err)
			}
			// A new API process must use the durable started/author/body receipt.
			recovered, err := integrations.New(f.pool, f.config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = recovered.RetryPRReviewPublication(t.Context(), r.ID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err = recovered.ProcessPRReviewPublication(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := recovered.PRReview(t.Context(), r.ID.String())
			want := "published"
			if hidden {
				want = "uncertain"
			}
			if err != nil || got.GitHub.Status != want || f.provider.ReviewCreates.Load() != 1 {
				t.Fatal("lost or duplicated review", got.GitHub, f.provider.ReviewCreates.Load(), err)
			}
			if !hidden && f.provider.ReviewLists.Load() < 3 {
				t.Fatal("later pages not checked")
			}
			var runs int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE kind='pr_review'`).Scan(&runs); err != nil || runs != 1 {
				t.Fatal("retry launched model", runs, err)
			}
		})
	}
}
func TestReviewPublicationChecksCurrentCodePermissionAndConcurrentClaims(t *testing.T) {
	for _, mode := range []string{"base", "head", "closed", "permission", "rate_limit", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			f, r := completedReview(t)
			pr := r.Snapshot.PR
			switch mode {
			case "base":
				pr.BaseSHA = strings.Repeat("c", 40)
				f.provider.SetReviewPR(pr, "open")
			case "head":
				pr.HeadSHA = strings.Repeat("c", 40)
				f.provider.SetReviewPR(pr, "open")
			case "closed":
				f.provider.SetReviewPR(pr, "closed")
			case "permission":
				f.provider.ReviewPermissionDenied.Store(true)
			case "rate_limit":
				f.provider.ReviewReject.Store(429)
			}
			var group sync.WaitGroup
			for range 2 {
				group.Go(func() {
					if _, err := f.service.ProcessPRReviewPublication(t.Context()); err != nil {
						t.Error(err)
					}
				})
			}
			group.Wait()
			got, err := f.service.PRReview(t.Context(), r.ID.String())
			if err != nil {
				t.Fatal(err)
			}
			want := "skipped"
			creates := int64(0)
			if mode == "concurrent" {
				want = "published"
				creates = 1
			}
			if mode == "permission" || mode == "rate_limit" {
				want = "retrying"
			}
			if mode == "rate_limit" {
				creates = 1
			}
			if got.GitHub.Status != want || f.provider.ReviewCreates.Load() != creates {
				t.Fatal(mode, got.GitHub, f.provider.ReviewCreates.Load())
			}
			if mode == "rate_limit" {
				var started bool
				var backoff bool
				if err := f.pool.QueryRow(t.Context(), `SELECT started,next_attempt_at>now()+interval '100 seconds' FROM pr_review_publications WHERE review_id=$1`, r.ID).Scan(&started, &backoff); err != nil || started || !backoff {
					t.Fatal("definitive rate rejection not retryable", started, backoff, err)
				}
			}
		})
	}
}
