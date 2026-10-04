package integrations

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

func bodyReview() prreviews.Review {
	return prreviews.Review{ID: uuid.New(), RunID: uuid.New(), Assessment: prreviews.AssessmentFindings, MergeBaseSHA: strings.Repeat("a", 40), Snapshot: prreviews.LaunchSnapshot{PR: prreviews.PRIdentity{RepositoryName: "fixture/private-source", Number: 1, URL: "https://github.com/fixture/private-source/pull/1", HeadSHA: strings.Repeat("b", 40)}, Reviewer: prreviews.ReviewerSnapshot{Name: "reviewer", Model: "fixture", ReasoningEffort: "low"}}, Report: &prreviews.Report{Summary: "Summary", Coverage: "complete", Findings: []prreviews.Finding{}, Checks: []prreviews.Check{}, Limitations: []string{}}}
}
func TestReviewReceiptRequiresMarkerCommitAuthorAndPR(t *testing.T) {
	review := bodyReview()
	marker := "<!-- circular:pr-review:" + review.ID.String() + " -->"
	receipt := githubReviewReceipt{ID: "9", CommitID: review.Snapshot.PR.HeadSHA, Body: "Assessment\n\n" + marker, HTMLURL: review.Snapshot.PR.URL + "#pullrequestreview-9", AuthorID: "7"}
	if !matchesReviewReceipt(receipt, review, marker, "7") {
		t.Fatal("valid receipt rejected")
	}
	for name, edit := range map[string]func(*githubReviewReceipt){"author": func(r *githubReviewReceipt) { r.AuthorID = "8" }, "commit": func(r *githubReviewReceipt) { r.CommitID = strings.Repeat("c", 40) }, "pr": func(r *githubReviewReceipt) {
		r.HTMLURL = "https://github.com/fixture/private-source/pull/2#pullrequestreview-9"
	}, "marker": func(r *githubReviewReceipt) { r.Body = "unrelated" }} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			edit(&changed)
			if matchesReviewReceipt(changed, review, marker, "7") {
				t.Fatal("foreign receipt accepted")
			}
		})
	}
}
func TestUnavailableFreshnessNeverClaimsCurrent(t *testing.T) {
	now := time.Now()
	captured := prreviews.PRIdentity{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	if r := reviewFreshness(captured, captured.BaseSHA, captured.HeadSHA, "unavailable", &now, "Reconnect"); r.Status != "unavailable" || r.CheckedAt == nil {
		t.Fatal(r)
	}
	if r := reviewFreshness(captured, strings.Repeat("c", 40), captured.HeadSHA, "open", &now, ""); r.Status != "outdated" {
		t.Fatal(r)
	}
}
func TestReviewBodyBoundsEscapesAndKeepsCounts(t *testing.T) {
	review := bodyReview()
	for i := range 50 {
		side := "head"
		if i == 0 {
			side = "base"
		}
		review.Report.Findings = append(review.Report.Findings, prreviews.Finding{Severity: "high", Title: strings.Repeat("界[*@", 100), Path: "old ) 文.go", Side: side, StartLine: 1, EndLine: 2, Evidence: strings.Repeat("界[*@", 1000), Consequence: "bad", SuggestedFix: "fix"})
	}
	body, err := reviewBody(review, "http://localhost:5173")
	if err != nil || utf8.RuneCountInString(body) > 30000 || !strings.Contains(body, "50 blocking") || !strings.Contains(body, "omitted") || !strings.Contains(body, "/runs/"+review.RunID.String()) || !strings.Contains(body, "/blob/"+review.MergeBaseSHA+"/old%20%29%20%E6%96%87.go#L1-L2") || strings.Contains(body, "[*@") {
		t.Fatal("body identity, bounds or escaping", len(body), err)
	}
	review.Assessment = prreviews.AssessmentIncomplete
	review.Report.Coverage = "incomplete"
	body, err = reviewBody(review, "http://localhost:5173")
	if err != nil || !strings.Contains(body, "Assessment: Incomplete") {
		t.Fatal("incomplete hidden", err)
	}
}
