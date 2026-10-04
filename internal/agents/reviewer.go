package agents

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5"
)

const ReviewerPreset = "pr-reviewer"
const ReviewerName = "PR reviewer"

//go:embed reviewer.md
var ReviewerInstructions string

func EnsureReviewer(ctx context.Context, tx pgx.Tx, project string) (string, error) {
	return ensurePreset(ctx, tx, project, ReviewerPreset, ReviewerName, ReviewerInstructions)
}

func BackfillReviewers(ctx context.Context, tx pgx.Tx) error {
	if err := backfill(ctx, tx, EnsureReviewer); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO pr_review_settings(project_id,reviewer_id)
	 SELECT project_id,id FROM agents WHERE preset=$1 ON CONFLICT(project_id) DO NOTHING`, ReviewerPreset)
	return err
}
