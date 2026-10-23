package integrations

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"time"
)

func (s *Service) ProcessTaskCompletion(ctx context.Context) (bool, error) {
	var session uuid.UUID
	// Selection is non-locking; reconciliation rechecks after Project → Task locks.
	err := s.pool.QueryRow(ctx, `SELECT session_id FROM task_completions WHERE reconciliation_pending ORDER BY session_id LIMIT 1`).Scan(&session)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	worked, err := postgres.ReconcileTaskCompletion(ctx, tx, session, time.Now().UTC())
	if err != nil {
		return false, err
	}
	return worked, tx.Commit(ctx)
}

func (s *Service) InspectTaskCompletion(ctx context.Context, task uuid.UUID) (completion.View, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return completion.View{}, err
	}
	defer rollback(ctx, tx)
	v, err := postgres.InspectTaskCompletion(ctx, tx, task, time.Now().UTC())
	if err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}

func (s *Service) refreshTaskCompletion(ctx context.Context, task uuid.UUID, force bool) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(ctx, tx)
	snapshot, err := postgres.ClaimCompletionEvidence(ctx, tx, task, force, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if snapshot.Owner == uuid.Nil {
		return false, nil
	}
	operation, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	observation := completion.Observation{PR: snapshot.PR}
	checkErr := s.withGitHubRepository(operation, snapshot.ProjectID.String(), snapshot.RepositoryID.String(), GitHubCompletionRead, func(token string, actor PublisherIdentity) error {
		if snapshot.View.Identity == nil || actor.IdentityID != snapshot.View.Identity.ID.String() {
			return ErrIdentityConflict
		}
		var err error
		observation, err = s.collectCompletionGitHub(operation, token, snapshot.PR, snapshot.View.Contract.CheckConfig.Checks, snapshot.Accepted)
		return err
	})
	if checkErr != nil {
		observation = completion.Observation{PR: snapshot.PR, ErrorCode: "provider_unavailable"}
	}
	if r := snapshot.View.Review; r != nil && checkErr == nil {
		review, e := postgres.NewPRReviewStore(s.pool).Get(operation, r.ID)
		if e == nil && s.retainedReviewReport(operation, &review) == nil {
			observation.ReviewID, observation.ReviewerFingerprint, observation.ReviewReportSHA = r.ID, review.Snapshot.Reviewer.Fingerprint, r.ReportSHA
		}
	}
	record, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	write, err := s.pool.Begin(record)
	if err != nil {
		return true, err
	}
	defer rollback(record, write)
	_, err = postgres.RecordCompletionEvidence(record, write, snapshot, observation, time.Now().UTC(), providerRetryDelay(checkErr, time.Minute))
	if err != nil {
		return true, err
	}
	return true, write.Commit(record)
}

func (s *Service) RefreshTaskCompletion(ctx context.Context, task uuid.UUID) (completion.View, error) {
	if _, err := s.refreshTaskCompletion(ctx, task, true); err != nil {
		return completion.View{}, err
	}
	return s.InspectTaskCompletion(ctx, task)
}
func (s *Service) ProcessTaskCompletionFreshness(ctx context.Context) (bool, error) {
	var task uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT t.id FROM tasks t JOIN task_completion_evidence e ON e.session_id=t.completion_session_id JOIN task_completion_pull_requests pr ON pr.session_id=e.session_id JOIN work_sessions w ON w.id=e.session_id
 WHERE t.status='in_review' AND t.completion_contract->'policy'->>'mode'='review_required' AND pr.number IS NOT NULL AND w.released_at IS NOT NULL AND w.outcome='succeeded' AND NOT w.needs_user_resolution
 AND e.next_check_at<=now() AND (e.lease_until IS NULL OR e.lease_until<=now()) ORDER BY e.next_check_at,t.id LIMIT 1`).Scan(&task)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.refreshTaskCompletion(ctx, task, false)
}

func (s *Service) BindTaskPullRequest(ctx context.Context, task, session uuid.UUID, expectedRevision int64, number int) (completion.View, error) {
	if number < 1 || int64(number) > 2147483647 || session == uuid.Nil || expectedRevision < 1 || expectedRevision > 9007199254740991 {
		return completion.View{}, postgres.ErrTaskPlanningInput
	}
	view, err := s.InspectTaskCompletion(ctx, task)
	if err != nil {
		return view, err
	}
	if view.Identity == nil {
		return view, ErrIdentityUnavailable
	}
	var project, repo uuid.UUID
	var pr prreviews.PRIdentity
	err = s.pool.QueryRow(ctx, `SELECT t.project_id,r.id,r.name,r.external_refs->'github'->>'repository_id',r.external_refs->'github'->>'installation_id' FROM tasks t JOIN repositories r ON r.id=t.repository_id WHERE t.id=$1`, task).Scan(&project, &repo, &pr.RepositoryName, &pr.GitHubRepositoryID, &pr.InstallationID)
	if err != nil {
		return view, err
	}
	pr.Number = number
	operation, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	err = s.withGitHubRepository(operation, project.String(), repo.String(), GitHubCompletionRead, func(token string, actor PublisherIdentity) error {
		_, state, err := s.inspectReviewPR(operation, token, prreviews.LaunchSnapshot{PR: pr}, actor)
		if err == nil && state != "open" && state != "merged" {
			return prreviews.ErrSourceInvalid
		}
		return err
	})
	if err != nil {
		return view, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return view, err
	}
	defer rollback(ctx, tx)
	if err = postgres.BindCompletionPR(ctx, tx, task, session, expectedRevision, number, *view.Identity); err != nil {
		return view, err
	}
	if err = tx.Commit(ctx); err != nil {
		return view, err
	}
	return s.InspectTaskCompletion(ctx, task)
}
