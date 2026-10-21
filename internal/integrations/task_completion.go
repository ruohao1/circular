package integrations

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
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
