package postgres

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func (r *RunResources) ReviewCandidate() (*prreviews.ValidatedReport, error) {
	if err := r.guard(); err != nil {
		return nil, err
	}
	if r.kind != runstate.PRReview {
		return nil, ErrResourceState
	}
	var raw []byte
	var hash string
	if err := r.tx.QueryRow(r.ctx, `SELECT candidate_report,report_sha256 FROM pr_reviews WHERE run_id=$1`, r.id).Scan(&raw, &hash); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	context, err := r.ReviewContext()
	if err != nil {
		return nil, err
	}
	value, err := prreviews.ValidateReport(raw, context)
	if err != nil || value.SHA256 != hash {
		return nil, ErrResourceConflict
	}
	return &value, nil
}
func (r *RunResources) PersistReviewArtifact(worktree, name string, content artifacts.Content) (artifacts.Record, error) {
	var record artifacts.Record
	if err := r.guard(); err != nil {
		return record, err
	}
	if r.kind != runstate.PRReview {
		return record, ErrResourceState
	}
	if err := r.validContent(content, name); err != nil {
		return record, err
	}
	value, err := r.ReviewContext()
	if err != nil {
		return record, err
	}
	kind, media, digest := "", "application/json", ""
	switch name {
	case "pr-review-context.json":
		kind = "pr_review_context"
		digest, err = prreviews.Fingerprint(value)
	case "pr-review-diff.patch":
		kind = "pr_review_diff"
		media = "text/x-diff"
		digest = value.DiffSHA256
	case "pr-review-report.json":
		kind = "pr_review_report"
		candidate, e := r.ReviewCandidate()
		if e != nil {
			return record, e
		}
		if candidate == nil {
			return record, ErrResourceState
		}
		digest = candidate.SHA256
	default:
		return record, ErrResourceState
	}
	if err != nil || content.SHA256 != digest {
		return record, ErrResourceConflict
	}
	record = artifacts.Record{ID: artifacts.PRReviewID(r.id, name), RunID: r.id, Kind: kind, URI: content.URI, Metadata: map[string]any{"media_type": media, "size_bytes": content.SizeBytes, "sha256": content.SHA256, "review_id": value.ReviewID.String()}}
	if err := r.persistArtifact(worktree, record, "worker"); err != nil {
		return record, err
	}
	if kind == "pr_review_report" {
		if _, err := r.tx.Exec(r.ctx, `UPDATE pr_reviews SET report_artifact_id=$2 WHERE run_id=$1`, r.id, record.ID); err != nil {
			return record, err
		}
	}
	return record, nil
}
func (r *RunResources) CompletePRReview() error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.kind != runstate.PRReview || r.status != runstate.Finalizing {
		return ErrResourceState
	}
	value, err := r.ReviewContext()
	if err != nil {
		return err
	}
	contextSHA, err := prreviews.Fingerprint(value)
	if err != nil {
		return err
	}
	candidate, err := r.ReviewCandidate()
	if err != nil {
		return err
	}
	expected := map[string]string{"pr-review-context.json": contextSHA, "pr-review-diff.patch": value.DiffSHA256}
	if candidate != nil {
		expected["pr-review-report.json"] = candidate.SHA256
	}
	for name, sha := range expected {
		var valid bool
		kind := map[string]string{"pr-review-context.json": "pr_review_context", "pr-review-diff.patch": "pr_review_diff", "pr-review-report.json": "pr_review_report"}[name]
		if err := r.tx.QueryRow(r.ctx, `SELECT run_id=$2 AND metadata->>'sha256'=$3 AND metadata->>'review_id'=$4 AND kind=$5 AND uri=$6 FROM artifacts WHERE id=$1`, artifacts.PRReviewID(r.id, name), r.id, sha, value.ReviewID.String(), kind, "artifact://"+r.id.String()+"/"+name).Scan(&valid); err != nil || !valid {
			return ErrResourceConflict
		}
	}
	var reportError string
	var retained *uuid.UUID
	if err := r.tx.QueryRow(r.ctx, `SELECT report_error,report_artifact_id FROM pr_reviews WHERE run_id=$1`, r.id).Scan(&reportError, &retained); err != nil {
		return err
	}
	if candidate != nil && (retained == nil || *retained != artifacts.PRReviewID(r.id, "pr-review-report.json")) {
		return ErrResourceConflict
	}
	assessment := prreviews.Assess(runstate.Succeeded, candidate)
	if candidate == nil && reportError == "" {
		reportError = "The reviewer exited without submitting a valid structured report."
	}
	if reportError != "" {
		assessment = prreviews.AssessmentIncomplete
	}
	if _, err := r.tx.Exec(r.ctx, `UPDATE pr_reviews SET assessment=$2,report_error=$3,updated_at=now() WHERE run_id=$1`, r.id, assessment, reportError); err != nil {
		return err
	}
	if err := r.executionTransition(runstate.Succeeded, nil); err != nil {
		return err
	}
	if candidate != nil && reportError == "" {
		if _, err := r.tx.Exec(r.ctx, `INSERT INTO pr_review_publications(review_id,marker) VALUES($1,$2) ON CONFLICT DO NOTHING`, value.ReviewID, "<!-- circular:pr-review:"+value.ReviewID.String()+" -->"); err != nil {
			return err
		}
	}
	if err := QueuePRReviewLinear(r.ctx, r.tx, value.ReviewID); err != nil {
		return err
	}
	return r.event("run.completed", "worker", map[string]any{"review_id": value.ReviewID.String(), "assessment": assessment})
}
