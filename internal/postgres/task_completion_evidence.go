package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/prreviews"
)

type CompletionRefresh struct {
	View         completion.View
	ProjectID    uuid.UUID
	RepositoryID uuid.UUID
	PR           prreviews.PRIdentity
	Owner        uuid.UUID
	Accepted     *completion.Observation
}

func inspectCompletionEvidence(ctx context.Context, tx pgx.Tx, v *completion.View) error {
	var number *int
	var revision int64
	var observation []byte
	err := tx.QueryRow(ctx, `SELECT p.number,p.binding_revision,e.observation,c.review_revision FROM task_completions c JOIN task_completion_evidence e ON e.session_id=c.session_id JOIN task_completion_pull_requests p ON p.session_id=c.session_id WHERE c.session_id=$1`, v.SessionID).Scan(&number, &revision, &observation, &v.ReviewRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if number != nil {
		v.Binding = &completion.Binding{Number: *number, Revision: revision}
	}
	if len(observation) > 0 {
		if err = json.Unmarshal(observation, &v.Observation); err != nil {
			return err
		}
	}
	var identity completion.ProviderIdentity
	err = tx.QueryRow(ctx, `SELECT i.id,i.generation,i.account_id,repo.external_refs->'github'->>'repository_id'
 FROM tasks t JOIN repositories repo ON repo.id=t.repository_id JOIN integration_identity_bindings b ON b.project_id=t.project_id AND b.provider='github'
 JOIN provider_identities i ON i.id=b.identity_id WHERE t.id=$1 AND b.enabled AND i.enabled AND i.status='available'
 AND i.account_id=repo.external_refs->'github'->>'installation_id'`, v.TaskID).Scan(&identity.ID, &identity.Generation, &identity.InstallationID, &identity.RepositoryID)
	if err == nil {
		v.Identity = &identity
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var review completion.ReviewEvidence
	var report []byte
	err = tx.QueryRow(ctx, `SELECT p.id,p.run_id,r.status,p.snapshot->'pr'->>'base_sha',p.snapshot->'pr'->>'head_sha',p.snapshot->'reviewer'->>'fingerprint',COALESCE(p.report_sha256,''),CASE WHEN p.report_artifact_id IS NOT NULL THEN p.candidate_report END,
 p.report_artifact_id IS NOT NULL AND p.report_error='' AND EXISTS(SELECT 1 FROM artifacts a WHERE a.id=p.report_artifact_id AND a.run_id=p.run_id AND a.kind='pr_review_report' AND a.metadata->>'sha256'=p.report_sha256 AND a.metadata->>'review_id'=p.id::text)
 FROM task_completions c JOIN pr_reviews p ON p.id=c.latest_review_id JOIN runs r ON r.id=p.run_id WHERE c.session_id=$1`, v.SessionID).Scan(&review.ID, &review.RunID, &review.State, &review.BaseSHA, &review.HeadSHA, &review.ReviewerFingerprint, &review.ReportSHA, &report, &review.Retained)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(report) > 0 {
		var parsed prreviews.Report
		if err = json.Unmarshal(report, &parsed); err != nil {
			return err
		}
		review.Coverage, review.Findings = parsed.Coverage, parsed.Findings
	}
	v.Review = &review
	return nil
}

// Identity → binding → Project → Task matches provider settings and planning writers.
func lockCompletionTask(ctx context.Context, tx pgx.Tx, task uuid.UUID) error {
	var project uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id=$1`, task).Scan(&project); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT i.id FROM provider_identities i JOIN integration_identity_bindings b ON b.identity_id=i.id WHERE b.project_id=$1 AND b.provider='github' FOR SHARE OF i`, project); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT identity_id FROM integration_identity_bindings WHERE project_id=$1 AND provider='github' FOR SHARE`, project); err != nil {
		return err
	}
	_, err := lockPlanningTask(ctx, tx, task)
	return err
}

func ClaimCompletionEvidence(ctx context.Context, tx pgx.Tx, task uuid.UUID, force bool, now time.Time) (CompletionRefresh, error) {
	var result CompletionRefresh
	if err := lockCompletionTask(ctx, tx, task); err != nil {
		return result, err
	}
	view, err := InspectTaskCompletion(ctx, tx, task, now)
	if err != nil {
		return result, err
	}
	result.View = view
	if view.Contract == nil || view.Contract.Policy.Mode != "review_required" || view.Binding == nil || view.Verdict.NextStage != "in_review" || view.Stage == "completed" || view.Stage == "cancelled" {
		return result, nil
	}
	err = tx.QueryRow(ctx, `SELECT t.project_id,repo.id,repo.name,repo.external_refs->'github'->>'repository_id',repo.external_refs->'github'->>'installation_id' FROM tasks t JOIN repositories repo ON repo.id=t.repository_id WHERE t.id=$1`, task).Scan(&result.ProjectID, &result.RepositoryID, &result.PR.RepositoryName, &result.PR.GitHubRepositoryID, &result.PR.InstallationID)
	if err != nil {
		return result, err
	}
	result.PR.Number = view.Binding.Number
	owner := uuid.New()
	var accepted []byte
	err = tx.QueryRow(ctx, `UPDATE task_completion_evidence SET lease_owner=$2,lease_until=$3::timestamptz+interval '30 seconds',next_check_at=$3::timestamptz+interval '1 minute',observation=NULL
 WHERE session_id=$1 AND (lease_until IS NULL OR lease_until<=$3) AND ($4 OR next_check_at<=$3) RETURNING accepted_observation`, view.SessionID, owner, now, force).Scan(&accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Owner = owner
	if len(accepted) > 0 {
		err = json.Unmarshal(accepted, &result.Accepted)
	}
	return result, err
}

func sameCompletionEvidence(a, b completion.View) bool {
	if a.Contract == nil || b.Contract == nil || a.Binding == nil || b.Binding == nil {
		return false
	}
	if a.SessionID != b.SessionID || a.CurrentSessionID != b.CurrentSessionID || a.SessionID != a.CurrentSessionID || a.Contract.Fingerprint != b.Contract.Fingerprint || *a.Binding != *b.Binding || a.ReviewRevision != b.ReviewRevision {
		return false
	}
	if (a.Identity == nil) != (b.Identity == nil) || a.Identity != nil && *a.Identity != *b.Identity {
		return false
	}
	if (a.Review == nil) != (b.Review == nil) || a.Review != nil && (a.Review.ID != b.Review.ID || a.Review.ReportSHA != b.Review.ReportSHA || a.Review.State != b.Review.State) {
		return false
	}
	return b.Released && !b.OwnerActive && !b.CleanupPending && !b.NeedsUserResolution && b.Outcome == "succeeded" && b.Stage != "completed" && b.Stage != "cancelled"
}

func RecordCompletionEvidence(ctx context.Context, tx pgx.Tx, snapshot CompletionRefresh, o completion.Observation, now time.Time, retry time.Duration) (bool, error) {
	if snapshot.Owner == uuid.Nil {
		return false, nil
	}
	if err := lockCompletionTask(ctx, tx, snapshot.View.TaskID); err != nil {
		return false, err
	}
	current, err := InspectTaskCompletion(ctx, tx, snapshot.View.TaskID, now)
	if err != nil {
		return false, err
	}
	if !sameCompletionEvidence(snapshot.View, current) {
		return false, nil
	}
	var repo uuid.UUID
	var name string
	if err = tx.QueryRow(ctx, `SELECT repo.id,repo.name FROM tasks t JOIN repositories repo ON repo.id=t.repository_id WHERE t.id=$1 FOR SHARE OF repo`, snapshot.View.TaskID).Scan(&repo, &name); err != nil {
		return false, err
	}
	if repo != snapshot.RepositoryID || name != snapshot.PR.RepositoryName {
		return false, nil
	}
	o.SessionID, o.BindingRevision, o.ReviewRevision, o.ContractFingerprint, o.CheckedAt = current.SessionID, current.Binding.Revision, current.ReviewRevision, current.Contract.Fingerprint, now.UTC()
	if current.Identity != nil {
		o.Identity = *current.Identity
	}
	if o.PR.Number != current.Binding.Number || o.PR.RepositoryName != snapshot.PR.RepositoryName || o.PR.GitHubRepositoryID != snapshot.PR.GitHubRepositoryID || o.PR.InstallationID != snapshot.PR.InstallationID {
		return false, ErrTaskPlanningConflict
	}
	current.Observation = &o
	verdict := completion.Evaluate(current.Input, now)
	raw, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	// Only an accepted open-PR revision can later prove the merged result.
	accepted := verdict.MergeReadiness == "ready" && o.PRState == "open"
	tag, err := tx.Exec(ctx, `UPDATE task_completion_evidence SET observation=$3,accepted_observation=CASE WHEN $4 THEN $3 ELSE accepted_observation END,
 lease_owner=NULL,lease_until=NULL,next_check_at=$5 WHERE session_id=$1 AND lease_owner=$2 AND lease_until>$6`, current.SessionID, snapshot.Owner, raw, accepted, now.Add(max(time.Minute, retry)), now)
	return tag.RowsAffected() == 1, err
}

func BindCompletionPR(ctx context.Context, tx pgx.Tx, task, session uuid.UUID, expected int64, number int, identity completion.ProviderIdentity) error {
	if number < 1 || int64(number) > 2147483647 || expected < 1 || expected > maxPlanningRevision || session == uuid.Nil {
		return ErrTaskPlanningInput
	}
	if err := lockCompletionTask(ctx, tx, task); err != nil {
		return err
	}
	v, err := InspectTaskCompletion(ctx, tx, task, time.Now().UTC())
	if err != nil {
		return err
	}
	if v.Revision != expected || v.CurrentSessionID != session || v.Identity == nil || *v.Identity != identity || v.Contract == nil || v.Contract.Policy.Mode != "review_required" || v.OwnerActive || !v.Released || v.CleanupPending || v.NeedsUserResolution || v.Outcome != "succeeded" || v.Report == nil || v.Stage == "completed" || v.Stage == "cancelled" {
		return ErrTaskPlanningConflict
	}
	if v.Binding != nil && v.Binding.Number == number {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE task_completion_evidence SET pull_request_number=$2,binding_revision=binding_revision+1,observation=NULL,accepted_observation=NULL,lease_owner=NULL,lease_until=NULL,next_check_at=now() WHERE session_id=$1`, session, number); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_completions SET latest_review_id=NULL,review_revision=review_revision+1 WHERE session_id=$1`, session); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET revision=revision+1,updated_at=now() WHERE id=$1`, task); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=(SELECT project_id FROM tasks WHERE id=$1)`, task)
	return err
}
