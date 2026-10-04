package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

type ReviewLaunch struct {
	Snapshot              prreviews.LaunchSnapshot
	Request               prreviews.LaunchRequest
	Automatic             bool
	IntentID, IntentOwner uuid.UUID
}

type PRReviewStore struct{ pool *pgxpool.Pool }

func NewPRReviewStore(pool *pgxpool.Pool) *PRReviewStore { return &PRReviewStore{pool: pool} }

type reviewQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Replay resolves an acknowledged or lost launch result before mutable/provider reads.
func (s *PRReviewStore) Replay(ctx context.Context, source uuid.UUID, request prreviews.LaunchRequest) (*prreviews.Review, error) {
	fingerprint, err := prreviews.Fingerprint(request)
	if err != nil {
		return nil, err
	}
	var stored string
	var id *uuid.UUID
	err = s.pool.QueryRow(ctx, `SELECT parameters_sha256,review_id FROM pr_review_launch_intents WHERE source_run_id=$1 AND request_key=$2 AND NOT automatic`, source, request.RequestKey).Scan(&stored, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if stored != fingerprint {
		return nil, prreviews.ErrRequestConflict
	}
	if id == nil {
		return nil, prreviews.ErrReviewUnavailable
	}
	v, err := s.Get(ctx, *id)
	return &v, err
}

func (s *PRReviewStore) Launch(ctx context.Context, command ReviewLaunch) (prreviews.Review, error) {
	var zero prreviews.Review
	request, snapshot := command.Request, command.Snapshot
	if err := request.Validate(); err != nil {
		return zero, err
	}
	if !command.Automatic {
		if replay, err := s.Replay(ctx, snapshot.SourceRunID, request); err != nil {
			return zero, err
		} else if replay != nil {
			return *replay, nil
		}
	}
	parameters, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	parameterHash, err := prreviews.Fingerprint(request)
	if err != nil {
		return zero, err
	}
	sealed, err := prreviews.SealSnapshot(snapshot)
	if err != nil {
		return zero, err
	}
	if sealed.InputFingerprint != snapshot.InputFingerprint || sealed.InputFingerprint != request.ExpectedInputFingerprint {
		return zero, prreviews.ErrSnapshotChanged
	}
	if request.ReviewerID != uuid.Nil && request.ReviewerID != snapshot.Reviewer.AgentID {
		return zero, prreviews.ErrRequestConflict
	}
	if !prreviews.CommitPattern.MatchString(snapshot.PR.HeadSHA) || !prreviews.CommitPattern.MatchString(snapshot.PR.BaseSHA) {
		return zero, prreviews.ErrSourceInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer rollback(ctx, tx)
	if err := ReserveExternalEffect(ctx, tx, snapshot.SourceRunID, "review_launch"); err != nil {
		return zero, err
	}
	var automatic bool
	var selected *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT automatic,reviewer_id FROM pr_review_settings WHERE project_id=$1 FOR UPDATE`, snapshot.ProjectID).Scan(&automatic, &selected)
	if err != nil {
		return zero, reviewUnavailable(err)
	}
	// The settings lock serializes replay/disable against launch for this project.
	var priorID *uuid.UUID
	var priorHash string
	err = tx.QueryRow(ctx, `SELECT review_id,parameters_sha256 FROM pr_review_launch_intents WHERE source_run_id=$1 AND request_key=$2 AND NOT automatic`, snapshot.SourceRunID, request.RequestKey).Scan(&priorID, &priorHash)
	if err == nil && !command.Automatic {
		if priorHash != parameterHash {
			return zero, prreviews.ErrRequestConflict
		}
		if priorID == nil {
			return zero, prreviews.ErrReviewUnavailable
		}
		if err := tx.Commit(ctx); err != nil {
			return zero, err
		}
		return s.Get(ctx, *priorID)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if command.Automatic {
		if !automatic || selected == nil || *selected != snapshot.Reviewer.AgentID {
			return zero, prreviews.ErrReviewUnavailable
		}
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT status='pending' AND automatic AND lease_owner=$2 AND lease_until>now() AND source_run_id=$3 FROM pr_review_launch_intents WHERE id=$1 FOR UPDATE`, command.IntentID, command.IntentOwner, snapshot.SourceRunID).Scan(&allowed)
		if err != nil {
			return zero, reviewUnavailable(err)
		}
		if !allowed {
			return zero, prreviews.ErrReviewUnavailable
		}
	}
	var title, description string
	var refs json.RawMessage
	var project, repository uuid.UUID
	err = tx.QueryRow(ctx, `SELECT project_id,repository_id,title,description,external_refs FROM tasks WHERE id=$1 FOR UPDATE`, snapshot.TaskID).Scan(&project, &repository, &title, &description, &refs)
	if err != nil {
		return zero, reviewUnavailable(err)
	}
	if project != snapshot.ProjectID || repository != snapshot.RepositoryID || title != snapshot.TaskTitle || description != snapshot.TaskDescription {
		return zero, prreviews.ErrSnapshotChanged
	}
	refHash, _ := prreviews.Fingerprint(refs)
	savedRefHash, _ := prreviews.Fingerprint(snapshot.TaskExternalRefs)
	if refHash != savedRefHash {
		return zero, prreviews.ErrSnapshotChanged
	}
	var coder uuid.UUID
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT r.agent_id,r.kind='coding' AND r.status='succeeded' AND r.task_id=$2 AND d.status='delivered'
	 AND d.project_id=$3 AND d.repository_id=$4 AND repo.project_id=$3 AND repo.clone_url=$5
	 AND d.github_repository_id=$6 AND d.installation_id=$7 AND d.pull_request_number=$8 AND d.repository_name=$9
	 AND repo.external_refs->'github'->>'repository_id'=$6 AND repo.external_refs->'github'->>'installation_id'=$7
	 FROM runs r JOIN github_run_deliveries d ON d.run_id=r.id JOIN repositories repo ON repo.id=d.repository_id WHERE r.id=$1`, snapshot.SourceRunID, snapshot.TaskID, snapshot.ProjectID, snapshot.RepositoryID, snapshot.CloneURL, snapshot.PR.GitHubRepositoryID, snapshot.PR.InstallationID, snapshot.PR.Number, snapshot.PR.RepositoryName).Scan(&coder, &eligible)
	if err != nil {
		return zero, reviewUnavailable(err)
	}
	if !eligible || coder == snapshot.Reviewer.AgentID {
		return zero, prreviews.ErrReviewUnavailable
	}
	var current prreviews.ReviewerSnapshot
	var enabled bool
	var agentProject uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id,project_id,name,backend,instructions,backend_config,enabled FROM agents WHERE id=$1 FOR SHARE`, snapshot.Reviewer.AgentID).Scan(&current.AgentID, &agentProject, &current.Name, &current.Backend, &current.Instructions, &current.BackendConfig, &enabled)
	if err != nil {
		return zero, reviewUnavailable(err)
	}
	if !enabled || agentProject != snapshot.ProjectID {
		return zero, prreviews.ErrReviewUnavailable
	}
	current, err = prreviews.NormalizeReviewer(current)
	if err != nil {
		return zero, err
	}
	if current.Fingerprint != snapshot.Reviewer.Fingerprint {
		return zero, prreviews.ErrSnapshotChanged
	}
	identity, err := prreviews.ReviewIdentity(snapshot)
	if err != nil {
		return zero, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, identity); err != nil {
		return zero, err
	}
	var reviewID uuid.UUID
	var attempt int
	err = tx.QueryRow(ctx, `SELECT id,attempt FROM pr_reviews WHERE identity_key=$1 ORDER BY attempt DESC LIMIT 1 FOR UPDATE`, identity).Scan(&reviewID, &attempt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if request.Mode == "again" {
		var same bool
		err = tx.QueryRow(ctx, `SELECT source_run_id=$2 AND snapshot->'pr'->>'github_repository_id'=$3 AND (snapshot->'pr'->>'number')::integer=$4 FROM pr_reviews WHERE id=$1`, request.PreviousReviewID, snapshot.SourceRunID, snapshot.PR.GitHubRepositoryID, snapshot.PR.Number).Scan(&same)
		if err != nil {
			return zero, reviewUnavailable(err)
		}
		if !same {
			return zero, prreviews.ErrRequestConflict
		}
	}
	if reviewID == uuid.Nil || request.Mode == "again" {
		reviewID = uuid.New()
		runID := uuid.New()
		var runAttempt int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt),0)+1 FROM runs WHERE task_id=$1`, snapshot.TaskID).Scan(&runAttempt); err != nil {
			return zero, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO runs(id,task_id,agent_id,parent_run_id,backend,status,attempt,external_refs,kind) VALUES($1,$2,$3,$4,$5,'queued',$6,'{}','pr_review')`, runID, snapshot.TaskID, snapshot.Reviewer.AgentID, snapshot.SourceRunID, snapshot.Reviewer.Backend, runAttempt)
		if err != nil {
			return zero, err
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return zero, err
		}
		var previous *uuid.UUID
		if request.Mode == "again" {
			previous = &request.PreviousReviewID
		}
		_, err = tx.Exec(ctx, `INSERT INTO pr_reviews(id,project_id,repository_id,source_run_id,run_id,reviewer_id,previous_review_id,identity_key,attempt,automatic,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, reviewID, snapshot.ProjectID, snapshot.RepositoryID, snapshot.SourceRunID, runID, snapshot.Reviewer.AgentID, previous, identity, attempt+1, command.Automatic, raw)
		if err != nil {
			return zero, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) VALUES($1,$2,1,'pr_review.queued','circular',json_build_object('review_id',$3::text),now())`, uuid.New(), runID, reviewID)
		if err != nil {
			return zero, err
		}
	}
	if command.Automatic {
		_, err = tx.Exec(ctx, `UPDATE pr_review_launch_intents SET status='launched',review_id=$2,parameters=$3,parameters_sha256=$4,lease_owner=NULL,lease_until=NULL,last_error='' WHERE id=$1`, command.IntentID, reviewID, parameters, parameterHash)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO pr_review_launch_intents(id,project_id,source_run_id,request_key,parameters,parameters_sha256,status,review_id) VALUES($1,$2,$3,$4,$5,$6,'launched',$7)`, uuid.New(), snapshot.ProjectID, snapshot.SourceRunID, request.RequestKey, parameters, parameterHash, reviewID)
	}
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pr_review_freshness(repository_id,github_repository_id,pull_request_number) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, snapshot.RepositoryID, snapshot.PR.GitHubRepositoryID, snapshot.PR.Number)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return s.Get(ctx, reviewID)
}

func reviewUnavailable(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return prreviews.ErrReviewUnavailable
	}
	return err
}

func (s *PRReviewStore) Get(ctx context.Context, id uuid.UUID) (prreviews.Review, error) {
	var result prreviews.Review
	var snapshot, report []byte
	err := s.pool.QueryRow(ctx, `SELECT p.id,p.run_id,p.previous_review_id,p.attempt,p.automatic,p.snapshot,
	 COALESCE(p.context->>'merge_base_sha',''),r.status,p.assessment,
	 CASE WHEN p.report_artifact_id IS NOT NULL THEN p.candidate_report ELSE NULL END,p.report_error,p.created_at
	 FROM pr_reviews p JOIN runs r ON r.id=p.run_id WHERE p.id=$1`, id).Scan(&result.ID, &result.RunID, &result.PreviousReviewID, &result.Attempt, &result.Automatic, &snapshot, &result.MergeBaseSHA, &result.RunStatus, &result.Assessment, &report, &result.ReportError, &result.CreatedAt)
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(snapshot, &result.Snapshot); err != nil {
		return result, prreviews.ErrSourceInvalid
	}
	if len(report) > 0 {
		var value prreviews.Report
		if err = json.Unmarshal(report, &value); err != nil {
			return result, prreviews.ErrSourceInvalid
		}
		result.Report = &value
	}
	result.Freshness.Status = "unknown"
	var state, base, head, safeError string
	var checked *time.Time
	err = s.pool.QueryRow(ctx, `SELECT state,observed_base_sha,observed_head_sha,checked_at,last_error FROM pr_review_freshness WHERE repository_id=$1 AND github_repository_id=$2 AND pull_request_number=$3`, result.Snapshot.RepositoryID, result.Snapshot.PR.GitHubRepositoryID, result.Snapshot.PR.Number).Scan(&state, &base, &head, &checked, &safeError)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	result.Freshness = prreviews.ProjectFreshness(result.Snapshot.PR, base, head, state, checked, safeError)
	result.GitHub.Status = "pending"
	result.Linear.Status = "skipped"
	if result.RunStatus == runstate.Failed || result.RunStatus == runstate.Cancelled || result.RunStatus == runstate.Succeeded && result.Report == nil {
		result.GitHub.Status = "skipped"
	}
	err = s.pool.QueryRow(ctx, `SELECT status,github_review_url,last_error,status IN ('retrying','failed','uncertain'),publisher FROM pr_review_publications WHERE review_id=$1`, id).Scan(&result.GitHub.Status, &result.GitHub.URL, &result.GitHub.Error, &result.GitHub.Retryable, &result.GitHub.Publisher)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	err = s.pool.QueryRow(ctx, `SELECT status,last_error,status IN ('failed','pending') FROM linear_run_updates WHERE run_id=$1 AND phase='pr_review'`, result.RunID).Scan(&result.Linear.Status, &result.Linear.Error, &result.Linear.Retryable)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	var native bool
	if e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_session_runs WHERE run_id=$1)`, result.RunID).Scan(&native); e != nil {
		return result, e
	}
	if native {
		result.Linear.Status = "pending"
		result.Linear.Error = ""
		result.Linear.Retryable = false
		e := s.pool.QueryRow(ctx, `SELECT a.status,a.last_error FROM linear_agent_activities a JOIN external_session_runs s ON s.request_id=a.request_id WHERE s.run_id=$1 AND a.status<>'cancelled' ORDER BY CASE a.status WHEN 'uncertain' THEN 0 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,a.updated_at DESC,a.id DESC LIMIT 1`, result.RunID).Scan(&result.Linear.Status, &result.Linear.Error)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return result, e
		}
	}

	if result.RunStatus.Terminal() && result.RunStatus != runstate.Succeeded {
		result.Assessment = prreviews.AssessmentIncomplete
	}
	return result, nil
}

// Source reads the task and reviewer; provider verification happens before SealSnapshot.
func (s *PRReviewStore) Source(ctx context.Context, source, reviewer uuid.UUID) (prreviews.LaunchSnapshot, error) {
	var value prreviews.LaunchSnapshot
	var coder uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT r.id,r.task_id,t.project_id,repo.id,repo.clone_url,t.title,t.description,t.external_refs,r.agent_id,
	 d.installation_id,d.github_repository_id,d.repository_name,d.pull_request_number,d.pull_request_url,d.base_branch,d.branch,d.base_commit,d.commit_sha
	 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id
	 JOIN github_run_deliveries d ON d.run_id=r.id AND d.repository_id=repo.id
	 WHERE r.id=$1 AND r.kind='coding' AND r.status='succeeded' AND d.status='delivered'
	 AND repo.external_refs->'github'->>'repository_id'=d.github_repository_id AND repo.external_refs->'github'->>'installation_id'=d.installation_id`, source).Scan(&value.SourceRunID, &value.TaskID, &value.ProjectID, &value.RepositoryID, &value.CloneURL, &value.TaskTitle, &value.TaskDescription, &value.TaskExternalRefs, &coder, &value.PR.InstallationID, &value.PR.GitHubRepositoryID, &value.PR.RepositoryName, &value.PR.Number, &value.PR.URL, &value.PR.BaseRef, &value.PR.HeadRef, &value.PR.BaseSHA, &value.PR.HeadSHA)
	if err != nil {
		return value, reviewUnavailable(err)
	}
	if reviewer == uuid.Nil {
		var selected *uuid.UUID
		err = s.pool.QueryRow(ctx, `SELECT reviewer_id FROM pr_review_settings WHERE project_id=$1`, value.ProjectID).Scan(&selected)
		if err != nil {
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
	err = s.pool.QueryRow(ctx, `SELECT id,name,backend,instructions,backend_config,enabled FROM agents WHERE id=$1 AND project_id=$2`, reviewer, value.ProjectID).Scan(&value.Reviewer.AgentID, &value.Reviewer.Name, &value.Reviewer.Backend, &value.Reviewer.Instructions, &value.Reviewer.BackendConfig, &enabled)
	if err != nil {
		return value, reviewUnavailable(err)
	}
	if !enabled {
		return value, fmt.Errorf("%w: selected reviewer is disabled", prreviews.ErrReviewUnavailable)
	}
	value.Reviewer, err = prreviews.NormalizeReviewer(value.Reviewer)
	if err != nil {
		return value, err
	}
	value.Evidence = []prreviews.Evidence{}
	rows, err := s.pool.Query(ctx, `SELECT id,kind,metadata->>'sha256' FROM artifacts WHERE run_id=$1 AND kind='diff' ORDER BY id`, source)
	if err != nil {
		return value, err
	}
	for rows.Next() {
		e := prreviews.Evidence{SourceRunID: source}
		if err := rows.Scan(&e.ArtifactID, &e.Kind, &e.SHA256); err != nil {
			rows.Close()
			return value, err
		}
		value.Evidence = append(value.Evidence, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return value, err
	}
	var sequence int64
	var text string
	err = s.pool.QueryRow(ctx, `SELECT sequence,left(data->>'content',16000) FROM events WHERE run_id=$1 AND type='agent.message.completed' ORDER BY sequence DESC LIMIT 1`, source).Scan(&sequence, &text)
	if err == nil {
		value.Evidence = append(value.Evidence, prreviews.Evidence{SourceRunID: source, EventSequence: sequence, Kind: "coding_summary", Text: text, Limitation: "Prior coding-agent evidence; checks have not been rerun by the reviewer."})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return value, err
	}
	return value, nil
}

func (r *RunResources) PersistReviewContext(value prreviews.Context, digest string) error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.kind != runstate.PRReview || r.status != runstate.Provisioning || value.RunID != r.id {
		return ErrResourceState
	}
	if err := prreviews.ValidateContext(value); err != nil {
		return ErrResourceConflict
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > prreviews.MaxContextBytes {
		return ErrResourceConflict
	}
	expected, err := prreviews.Fingerprint(value)
	if err != nil || expected != digest {
		return ErrResourceConflict
	}
	snapshot, err := json.Marshal(value.Snapshot)
	if err != nil {
		return err
	}
	result, err := r.tx.Exec(r.ctx, `UPDATE pr_reviews SET context=$2::jsonb,context_sha256=$3,updated_at=now() WHERE run_id=$1 AND id=$4 AND snapshot=$5::jsonb AND (context IS NULL OR (context=$2::jsonb AND context_sha256=$3))`, r.id, raw, digest, value.ReviewID, snapshot)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrResourceConflict
	}
	return nil
}
func (r *RunResources) ReviewContext() (prreviews.Context, error) {
	var value prreviews.Context
	if err := r.guard(); err != nil {
		return value, err
	}
	if r.kind != runstate.PRReview {
		return value, ErrResourceState
	}
	var raw []byte
	var digest string
	if err := r.tx.QueryRow(r.ctx, `SELECT context,context_sha256 FROM pr_reviews WHERE run_id=$1`, r.id).Scan(&raw, &digest); err != nil {
		return value, err
	}
	if len(raw) > prreviews.MaxContextBytes || json.Unmarshal(raw, &value) != nil || prreviews.ValidateContext(value) != nil {
		return value, ErrResourceConflict
	}
	expected, err := prreviews.Fingerprint(value)
	if err != nil || expected != digest || value.RunID != r.id {
		return value, ErrResourceConflict
	}
	return value, nil
}

// ReviewEvidenceRecords only returns artifacts referenced by this immutable
// snapshot. The execution layer verifies each original file before reading it.
func (r *RunResources) ReviewEvidenceRecords() ([]artifacts.Record, error) {
	inputs, err := r.ProvisioningContext()
	if err != nil {
		return nil, err
	}
	if inputs.Review == nil {
		return nil, ErrResourceState
	}
	result := []artifacts.Record{}
	for _, e := range inputs.Review.Evidence {
		if e.ArtifactID == uuid.Nil {
			continue
		}
		if e.SourceRunID != inputs.Review.SourceRunID {
			return nil, ErrResourceConflict
		}
		var a artifacts.Record
		if err := r.tx.QueryRow(r.ctx, `SELECT id,run_id,kind,uri,metadata FROM artifacts WHERE id=$1 AND run_id=$2`, e.ArtifactID, e.SourceRunID).Scan(&a.ID, &a.RunID, &a.Kind, &a.URI, &a.Metadata); err != nil {
			return nil, ErrResourceConflict
		}
		if a.Kind != e.Kind || a.Metadata["sha256"] != e.SHA256 {
			return nil, ErrResourceConflict
		}
		result = append(result, a)
	}
	return result, nil
}
