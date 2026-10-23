package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/prreviews"
)

// SourceForWork includes accepted agent claims as context, never as a review verdict.
func (s *PRReviewStore) SourceForWork(ctx context.Context, session, reviewer uuid.UUID) (prreviews.LaunchSnapshot, error) {
	return completionReviewSource(ctx, s.pool, session, reviewer)
}

func completionReviewSource(ctx context.Context, q reviewQueryer, session, reviewer uuid.UUID) (prreviews.LaunchSnapshot, error) {
	var value prreviews.LaunchSnapshot
	var report completion.Report
	var run *uuid.UUID
	var number *int
	err := q.QueryRow(ctx, `SELECT t.id,t.project_id,repo.id,repo.clone_url,t.title,t.description,t.external_refs,
 repo.external_refs->'github'->>'installation_id',repo.external_refs->'github'->>'repository_id',repo.name,c.report,w.run_id,pr.number
 FROM work_sessions w JOIN tasks t ON t.id=w.task_id JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id
 JOIN task_completions c ON c.session_id=w.id AND c.task_id=t.id
 JOIN task_completion_pull_requests pr ON pr.session_id=c.session_id
 WHERE w.id=$1 AND w.id=t.completion_session_id AND w.released_at IS NOT NULL AND w.outcome='succeeded' AND NOT w.needs_user_resolution
 AND t.status NOT IN ('completed','cancelled') AND t.completion_contract->'policy'->>'mode'='review_required'
 AND c.report IS NOT NULL AND NOT EXISTS(SELECT 1 FROM work_sessions owner WHERE owner.task_id=t.id AND owner.released_at IS NULL)`, session).
		Scan(&value.TaskID, &value.ProjectID, &value.RepositoryID, &value.CloneURL, &value.TaskTitle, &value.TaskDescription, &value.TaskExternalRefs, &value.PR.InstallationID, &value.PR.GitHubRepositoryID, &value.PR.RepositoryName, &report, &run, &number)
	if err != nil {
		return value, reviewUnavailable(err)
	}
	if number == nil {
		return value, prreviews.ErrSourceInvalid
	}
	value.PR.Number = *number
	if run != nil {
		value.SourceRunID = *run
	} else {
		value.Version = 2
		value.Source = &prreviews.Source{WorkSessionID: &session}
	}
	var coder uuid.UUID
	if run != nil {
		if err = q.QueryRow(ctx, `SELECT agent_id FROM runs WHERE id=$1 AND status='succeeded' AND worker_id IS NULL AND NOT EXISTS(SELECT 1 FROM workspaces WHERE run_id=$1 AND status<>'released')`, *run).Scan(&coder); err != nil {
			return value, reviewUnavailable(err)
		}
	}
	value.Reviewer, err = reviewSourceReviewer(ctx, q, value.ProjectID, reviewer, coder)
	if err != nil {
		return value, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return value, err
	}
	value.Evidence = []prreviews.Evidence{{SourceRunID: value.SourceRunID, WorkSessionID: &session, Kind: "completion_report", Text: string(raw), Limitation: "Accepted coding-agent claim only; the independent reviewer must verify it against the PR and run its own checks."}}
	return value, nil
}

func reviewSourceReviewer(ctx context.Context, q reviewQueryer, project, reviewer, coder uuid.UUID) (prreviews.ReviewerSnapshot, error) {
	var value prreviews.ReviewerSnapshot
	if reviewer == uuid.Nil {
		var selected *uuid.UUID
		if err := q.QueryRow(ctx, `SELECT reviewer_id FROM pr_review_settings WHERE project_id=$1`, project).Scan(&selected); err != nil {
			return value, reviewUnavailable(err)
		}
		if selected == nil {
			return value, prreviews.ErrReviewUnavailable
		}
		reviewer = *selected
	}
	if reviewer == coder {
		return value, fmt.Errorf("%w: choose an independent reviewer", prreviews.ErrReviewUnavailable)
	}
	var enabled bool
	err := q.QueryRow(ctx, `SELECT id,name,backend,instructions,backend_config,enabled FROM agents WHERE id=$1 AND project_id=$2`, reviewer, project).Scan(&value.AgentID, &value.Name, &value.Backend, &value.Instructions, &value.BackendConfig, &enabled)
	if err != nil {
		return value, reviewUnavailable(err)
	}
	if !enabled {
		return value, prreviews.ErrReviewUnavailable
	}
	return prreviews.NormalizeReviewer(value)
}

// reserveCompletionReview shares the Task lock with Done/reopen and admission.
func reserveCompletionReview(ctx context.Context, tx pgx.Tx, snapshot prreviews.LaunchSnapshot) (*uuid.UUID, error) {
	var session *uuid.UUID
	var frozen bool
	var stage string
	err := tx.QueryRow(ctx, `SELECT completion_session_id,completion_contract IS NOT NULL,status FROM tasks WHERE id=$1`, snapshot.TaskID).Scan(&session, &frozen, &stage)
	if err != nil {
		return nil, err
	}
	if !frozen && snapshot.Source == nil {
		return nil, nil
	}
	if session == nil || stage == "completed" || stage == "cancelled" {
		return nil, prreviews.ErrSourceInvalid
	}
	value, err := completionReviewSource(ctx, tx, *session, snapshot.Reviewer.AgentID)
	if err != nil {
		return nil, err
	}
	if value.ReviewSource().Key() != snapshot.ReviewSource().Key() || value.RepositoryID != snapshot.RepositoryID || value.ProjectID != snapshot.ProjectID || value.CloneURL != snapshot.CloneURL || value.PR.Number != snapshot.PR.Number || value.PR.GitHubRepositoryID != snapshot.PR.GitHubRepositoryID || value.PR.InstallationID != snapshot.PR.InstallationID || value.PR.RepositoryName != snapshot.PR.RepositoryName {
		return nil, prreviews.ErrSourceInvalid
	}
	want, _ := prreviews.Fingerprint(value.Evidence)
	got, _ := prreviews.Fingerprint(snapshot.Evidence)
	if want != got {
		return nil, prreviews.ErrSnapshotChanged
	}
	return session, nil
}

func ReservePRReviewPublication(ctx context.Context, tx pgx.Tx, review, run uuid.UUID) error {
	if err := ReserveExternalEffect(ctx, tx, run, "github_review"); err != nil {
		return err
	}
	var snapshot prreviews.LaunchSnapshot
	if err := tx.QueryRow(ctx, `SELECT snapshot FROM pr_reviews WHERE id=$1 AND run_id=$2`, review, run).Scan(&snapshot); err != nil {
		return err
	}
	var managed bool
	if err := tx.QueryRow(ctx, `SELECT completion_contract IS NOT NULL FROM tasks WHERE id=$1`, snapshot.TaskID).Scan(&managed); err != nil {
		return err
	}
	if snapshot.Source == nil && !managed {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, snapshot.ProjectID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, snapshot.TaskID); err != nil {
		return err
	}
	// Publication uses the sealed reviewer even if the Agent defaults later change.
	var allowed bool
	source := snapshot.ReviewSource()
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_sessions w WHERE w.id=t.completion_session_id AND w.task_id=t.id
 AND w.run_id IS NOT DISTINCT FROM $8::uuid AND ($2::uuid IS NULL OR w.id=$2) AND w.released_at IS NOT NULL AND w.outcome='succeeded' AND NOT w.needs_user_resolution)
 AND t.status NOT IN ('completed','cancelled')
 AND EXISTS(SELECT 1 FROM task_completion_pull_requests pr WHERE pr.session_id=t.completion_session_id AND pr.number=$9)
 AND repo.project_id=$3 AND repo.clone_url=$4 AND repo.external_refs->'github'->>'repository_id'=$5
 AND repo.external_refs->'github'->>'installation_id'=$6
 FROM tasks t JOIN repositories repo ON repo.id=t.repository_id WHERE t.id=$1 AND repo.id=$7 FOR SHARE OF repo`, snapshot.TaskID, source.WorkSessionID, snapshot.ProjectID, snapshot.CloneURL, snapshot.PR.GitHubRepositoryID, snapshot.PR.InstallationID, snapshot.RepositoryID, source.RunID, snapshot.PR.Number).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrExternalStopped
	}
	return nil
}
