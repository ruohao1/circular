package integrations

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (s *Service) taskReviewSource(ctx context.Context, task uuid.UUID) (prreviews.Source, error) {
	var source prreviews.Source
	err := s.pool.QueryRow(ctx, `SELECT w.run_id,CASE WHEN w.kind='external' THEN w.id END FROM tasks t JOIN work_sessions w ON w.id=t.completion_session_id AND w.task_id=t.id WHERE t.id=$1`, task).Scan(&source.RunID, &source.WorkSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = prreviews.ErrSourceInvalid
	}
	return source, err
}

func (s *Service) PrepareTaskPRReview(ctx context.Context, task, reviewer string) (prreviews.Preparation, error) {
	id, err := uuid.Parse(task)
	if err != nil {
		return prreviews.Preparation{}, ErrAccess
	}
	source, err := s.taskReviewSource(ctx, id)
	if err != nil {
		if reviewAvailabilityError(err) {
			return unavailableReview(err), nil
		}
		return prreviews.Preparation{}, err
	}
	return s.prepareReviewRequest(ctx, source, reviewer)
}
func (s *Service) LaunchTaskPRReview(ctx context.Context, task string, request prreviews.LaunchRequest) (prreviews.Review, error) {
	var zero prreviews.Review
	if err := request.Validate(); err != nil {
		return zero, err
	}
	id, err := uuid.Parse(task)
	if err != nil {
		return zero, ErrAccess
	}
	store := postgres.NewPRReviewStore(s.pool)
	if replay, err := store.ReplayTask(ctx, id, request); err != nil {
		return zero, err
	} else if replay != nil {
		return *replay, nil
	}
	source, err := s.taskReviewSource(ctx, id)
	if err != nil {
		return zero, err
	}
	snapshot, err := s.prepareReviewSource(ctx, source, request.ReviewerID)
	if err != nil {
		return zero, err
	}
	return store.Launch(ctx, postgres.ReviewLaunch{Snapshot: snapshot, Request: request})
}
func (s *Service) ListTaskPRReviews(ctx context.Context, task string, limit int, cursor string) (prreviews.Page, error) {
	return s.listPRReviews(ctx, task, limit, cursor, true)
}
