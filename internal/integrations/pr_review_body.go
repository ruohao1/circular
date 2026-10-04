package integrations

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ruohao1/circular/internal/prreviews"
)

type githubReviewReceipt struct{ ID, CommitID, Body, HTMLURL, AuthorID string }

func (r *githubReviewReceipt) UnmarshalJSON(raw []byte) error {
	var v struct {
		ID       json.Number `json:"id"`
		CommitID string      `json:"commit_id"`
		Body     string      `json:"body"`
		URL      string      `json:"html_url"`
		User     struct {
			ID json.Number `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*r = githubReviewReceipt{string(v.ID), v.CommitID, v.Body, v.URL, string(v.User.ID)}
	return nil
}
func matchesReviewReceipt(r githubReviewReceipt, review prreviews.Review, marker, author string) bool {
	return positiveNumber(r.ID) && positiveNumber(author) && r.AuthorID == author && r.CommitID == review.Snapshot.PR.HeadSHA && r.HTMLURL == review.Snapshot.PR.URL+"#pullrequestreview-"+r.ID && strings.HasSuffix(strings.TrimSpace(r.Body), marker)
}
func reviewText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "!", "\\!", "#", "\\#", "|", "\\|", "~", "\\~", "@", "@\u200b").Replace(s)
}
func boundedReviewText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n]) + "…"
	}
	return reviewText(s)
}
func reviewLocation(r prreviews.Review, f prreviews.Finding) string {
	sha := r.Snapshot.PR.HeadSHA
	if f.Side == "base" {
		sha = r.MergeBaseSHA
	}
	parts := strings.Split(f.Path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
		parts[i] = strings.NewReplacer("(", "%28", ")", "%29").Replace(parts[i])
	}
	return "https://github.com/" + r.Snapshot.PR.RepositoryName + "/blob/" + sha + "/" + strings.Join(parts, "/") + "#L" + strconv.Itoa(f.StartLine) + "-L" + strconv.Itoa(f.EndLine)
}
func reviewBody(r prreviews.Review, web string) (string, error) {
	if r.Report == nil || !githubPRPath.MatchString("/"+r.Snapshot.PR.RepositoryName+"/pull/"+strconv.Itoa(r.Snapshot.PR.Number)) || !prreviews.CommitPattern.MatchString(r.Snapshot.PR.HeadSHA) {
		return "", prreviews.ErrSourceInvalid
	}
	u, err := url.Parse(web)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", ErrConfiguration
	}
	label := map[prreviews.Assessment]string{prreviews.AssessmentFindings: "Findings", prreviews.AssessmentNoBlocking: "No blocking findings", prreviews.AssessmentIncomplete: "Incomplete"}[r.Assessment]
	if label == "" {
		return "", prreviews.ErrReviewUnavailable
	}
	counts := map[string]int{}
	for _, f := range r.Report.Findings {
		counts[f.Severity]++
	}
	body := fmt.Sprintf("## Circular PR reviewer\n\nAssessment: %s\n\nReviewed commit: [%s](https://github.com/%s/commit/%s)\n\nReviewer: %s · %s · %s\n\nCoverage: %s\n\n%d blocking findings · %d critical · %d high · %d medium · %d low\n\n%s\n\n", label, r.Snapshot.PR.HeadSHA, r.Snapshot.PR.RepositoryName, r.Snapshot.PR.HeadSHA, boundedReviewText(r.Snapshot.Reviewer.Name, 200), boundedReviewText(r.Snapshot.Reviewer.Model, 100), boundedReviewText(r.Snapshot.Reviewer.ReasoningEffort, 30), reviewText(r.Report.Coverage), counts["critical"]+counts["high"]+counts["medium"], counts["critical"], counts["high"], counts["medium"], counts["low"], boundedReviewText(r.Report.Summary, 1800))
	findings := slices.Clone(r.Report.Findings)
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	slices.SortStableFunc(findings, func(a, b prreviews.Finding) int { return rank[a.Severity] - rank[b.Severity] })
	omitted, details := 0, 0
	for _, f := range findings {
		entry := fmt.Sprintf("### %s: %s\n\n[%s:%d–%d](%s)\n\n", strings.ToUpper(f.Severity), boundedReviewText(f.Title, 200), boundedReviewText(f.Path, 250), f.StartLine, f.EndLine, reviewLocation(r, f))
		description := fmt.Sprintf("Evidence: %s\n\nConsequence: %s\n\nSuggested fix: %s\n\n", boundedReviewText(f.Evidence, 1400), boundedReviewText(f.Consequence, 600), boundedReviewText(f.SuggestedFix, 600))
		if utf8.RuneCountInString(body+entry) > 24500 {
			omitted++
			continue
		}
		if utf8.RuneCountInString(body+entry+description) > 24500 {
			details++
			body += entry
			continue
		}
		body += entry + description
	}
	body += "Checks and limitations:\n\n"
	for _, c := range r.Report.Checks {
		entry := fmt.Sprintf("- %s — %s. %s %s\n", boundedReviewText(c.Method, 140), reviewText(c.Outcome), boundedReviewText(c.Evidence, 200), boundedReviewText(c.NotRunReason, 200))
		if utf8.RuneCountInString(body+entry) > 28000 {
			details++
			continue
		}
		body += entry
	}
	for _, l := range r.Report.Limitations {
		entry := "- " + boundedReviewText(l, 400) + "\n"
		if utf8.RuneCountInString(body+entry) > 28000 {
			details++
			continue
		}
		body += entry
	}
	if omitted > 0 || details > 0 {
		body += fmt.Sprintf("\n%d findings omitted here; %d entries have details omitted. See the complete report in Circular.\n", omitted, details)
	}
	body += "\n[Open full review in Circular](" + strings.TrimRight(web, "/") + "/runs/" + r.RunID.String() + ")\n\n<!-- circular:pr-review:" + r.ID.String() + " -->"
	if utf8.RuneCountInString(body) > prreviews.MaxGitHubBodyRunes {
		return "", prreviews.ErrReportLimit
	}
	return body, nil
}
