package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (s *Service) attachReviewLinearURL(ctx context.Context, tx pgx.Tx, run uuid.UUID, link string) error {
	// UPDATE takes the outbox row lock; its predicate is rechecked after waiting
	// for a delivery. A nonempty body is the durable start/frozen-body receipt.
	_, err := tx.Exec(ctx, `UPDATE linear_run_updates SET summary=jsonb_set(summary::jsonb,'{github_url}',to_jsonb($2::text))::text WHERE run_id=$1 AND phase='pr_review' AND status='pending' AND body=''`, run, link)
	return err
}
func linearReviewBody(web string, u linearUpdate) string {
	var v prreviews.LinearSummary
	if json.Unmarshal([]byte(u.Summary), &v) != nil {
		return "**Circular PR review incomplete**\n\n[Open review in Circular](" + web + "/runs/" + u.Run + ")"
	}
	label := "completed"
	if v.Assessment == prreviews.AssessmentIncomplete {
		label = "incomplete"
	}
	body := fmt.Sprintf("**Circular PR review %s**\n\nReviewed commit: `%s`\n\n", label, v.HeadSHA)
	if v.ReportError != "" {
		body += "No complete structured report was available. See Circular for the review status.\n\n"
	} else {
		body += fmt.Sprintf("%d blocking findings · %d critical · %d high · %d medium · %d low\n\n", v.Critical+v.High+v.Medium, v.Critical, v.High, v.Medium, v.Low)
	}
	body += "[Open review in Circular](" + strings.TrimRight(web, "/") + "/runs/" + v.RunID.String() + ")"
	if v.GitHubURL != "" {
		body += "\n\n[View GitHub feedback](" + v.GitHubURL + ")"
	}
	return body
}
