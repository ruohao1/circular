package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/prreviews"
)

func reviewFreshness(pr prreviews.PRIdentity, base, head, state string, checked *time.Time, message string) prreviews.Freshness {
	return prreviews.ProjectFreshness(pr, base, head, state, checked, message)
}

// One lease per repository/PR coalesces all attempts and all API processes.
// Publication may bypass the one-minute cache, but never an active lease.
func (s *Service) claimReviewFreshness(ctx context.Context, r prreviews.Review, force bool) (uuid.UUID, bool, error) {
	owner := uuid.New()
	row, err := s.pool.Exec(ctx, `UPDATE pr_review_freshness SET lease_owner=$4,lease_until=now()+interval '30 seconds',next_check_at=now()+interval '1 minute' WHERE repository_id=$1 AND github_repository_id=$2 AND pull_request_number=$3 AND (lease_until IS NULL OR lease_until<now()) AND (($5 AND state<>'unavailable') OR next_check_at<=now())`, r.Snapshot.RepositoryID, r.Snapshot.PR.GitHubRepositoryID, r.Snapshot.PR.Number, owner, force)
	if err != nil {
		return owner, false, err
	}
	return owner, row.RowsAffected() == 1, nil
}
func (s *Service) recordReviewFreshness(ctx context.Context, r prreviews.Review, owner uuid.UUID, pr prreviews.PRIdentity, state string, checkErr error) error {
	message := ""
	if checkErr != nil {
		state = "unavailable"
		message = "GitHub could not verify the current PR. Check the connection and access."
	}
	command, err := s.pool.Exec(ctx, `UPDATE pr_review_freshness SET next_check_at=GREATEST(next_check_at,now()+$9::interval),state=$5,observed_base_sha=CASE WHEN $5='unavailable' THEN observed_base_sha ELSE $6 END,observed_head_sha=CASE WHEN $5='unavailable' THEN observed_head_sha ELSE $7 END,checked_at=CASE WHEN $5='unavailable' THEN checked_at ELSE now() END,last_error=$8,lease_owner=NULL,lease_until=NULL WHERE repository_id=$1 AND github_repository_id=$2 AND pull_request_number=$3 AND lease_owner=$4 AND lease_until>now()`, r.Snapshot.RepositoryID, r.Snapshot.PR.GitHubRepositoryID, r.Snapshot.PR.Number, owner, state, pr.BaseSHA, pr.HeadSHA, message, fmt.Sprintf("%f seconds", providerRetryDelay(checkErr, time.Minute).Seconds()))
	if err == nil && command.RowsAffected() != 1 {
		return prreviews.ErrReviewUnavailable
	}
	return err
}
func (s *Service) refreshReview(ctx context.Context, r prreviews.Review) (bool, error) {
	owner, claimed, err := s.claimReviewFreshness(ctx, r, false)
	if err != nil || !claimed {
		return false, err
	}
	operation, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var live prreviews.PRIdentity
	var state string
	checkErr := s.withGitHubRepository(operation, r.Snapshot.ProjectID.String(), r.Snapshot.RepositoryID.String(), GitHubReviewRead, func(token string, actor PublisherIdentity) error {
		var err error
		live, state, err = s.inspectReviewPR(operation, token, r.Snapshot, actor)
		return err
	})
	record, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	return true, s.recordReviewFreshness(record, r, owner, live, state, checkErr)
}
func (s *Service) RefreshPRReview(ctx context.Context, id string) (prreviews.Review, error) {
	r, err := s.PRReview(ctx, id)
	if err != nil {
		return r, err
	}
	if _, err = s.refreshReview(ctx, r); err != nil {
		return r, err
	}
	return s.PRReview(ctx, id)
}
func (s *Service) ProcessPRReviewFreshness(ctx context.Context) (bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT p.id FROM pr_review_freshness f JOIN pr_reviews p ON p.repository_id=f.repository_id AND p.snapshot->'pr'->>'github_repository_id'=f.github_repository_id AND (p.snapshot->'pr'->>'number')::int=f.pull_request_number WHERE f.state NOT IN ('closed','merged') AND f.next_check_at<=now() AND (f.lease_until IS NULL OR f.lease_until<now()) ORDER BY f.next_check_at LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r, err := s.PRReview(ctx, id)
	if err != nil {
		return false, err
	}
	return s.refreshReview(ctx, r)
}
