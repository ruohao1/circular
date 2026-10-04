package postgres

import (
	"encoding/json"

	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func (r *RunResources) reviewRejection() error {
	if r.kind != runstate.PRReview {
		return ErrResourceState
	}
	message := prreviews.ErrInvalidReport.Error()
	if _, err := r.tx.Exec(r.ctx, `UPDATE pr_reviews SET report_error=$2,updated_at=now() WHERE run_id=$1`, r.id, message); err != nil {
		return err
	}
	// Never retain rejected locations, model-supplied diagnostics or raw content.
	return r.event("pr_review.report.rejected", "circular", map[string]any{"reason": message})
}
func (r *RunResources) submitReviewCandidate(data map[string]any) error {
	if r.kind != runstate.PRReview {
		return ErrResourceState
	}
	if len(data) != 1 {
		return r.reviewRejection()
	}
	raw, err := json.Marshal(data["report"])
	if err != nil {
		return r.reviewRejection()
	}
	context, err := r.ReviewContext()
	if err != nil {
		return err
	}
	validated, err := prreviews.ValidateReport(raw, context)
	if err != nil {
		return r.reviewRejection()
	}
	normalized, err := json.Marshal(validated.Report)
	if err != nil {
		return err
	}
	result, err := r.tx.Exec(r.ctx, `UPDATE pr_reviews SET candidate_report=$2::jsonb,report_sha256=$3,report_error='',updated_at=now() WHERE run_id=$1 AND (candidate_report IS NULL OR (candidate_report=$2::jsonb AND report_sha256=$3))`, r.id, normalized, validated.SHA256)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return prreviews.ErrConflictingReport
	}
	// Persist the canonical report only. Scope, digest and verdict derive from
	// trusted state, not envelope fields or a caller-supplied checksum.
	return r.event("pr_review.report.submitted", "circular", map[string]any{"report": validated.Report})
}
