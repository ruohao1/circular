// Package postgres implements the existing Circular persistence contracts.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/worker"
)

// LeaseDuration and MaxRecoveries preserve the established durable claim contract.
const LeaseDuration = 60 * time.Second
const MaxRecoveries = 3

var ErrLeaseLost = errors.New("worker no longer owns Run")

type Queue struct{ pool *pgxpool.Pool }

func NewQueue(pool *pgxpool.Pool) *Queue { return &Queue{pool: pool} }

// DatabaseURL accepts native PostgreSQL URLs and the historical driver prefix.
func DatabaseURL(value string) string {
	if strings.HasPrefix(value, "postgresql+psycopg://") {
		return "postgresql://" + strings.TrimPrefix(value, "postgresql+psycopg://")
	}
	return value
}

func (q *Queue) Acquire(ctx context.Context, workerID string) (*worker.Claim, error) {
	if err := worker.ValidateID(workerID); err != nil {
		return nil, err
	}
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer rollback(ctx, tx) // Commit makes this a no-op.
	now := time.Now().UTC()
	claim, err := recoverExpired(ctx, tx, workerID, now)
	if err != nil {
		return nil, err
	}
	if claim == nil {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM runs
			WHERE status = 'queued' ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		runID, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET status = 'provisioning', worker_id = $2,
			claimed_at = $3, lease_expires_at = $4, updated_at = CURRENT_TIMESTAMP
			WHERE id = $1`, runID, workerID, now, now.Add(LeaseDuration))
		if err != nil {
			return nil, err
		}
		claim = &worker.Claim{RunID: runID}
	}
	if err := tx.Commit(ctx); err != nil {
		// A lost commit acknowledgement must never start an executor. The
		// possibly committed claim remains eligible for expired-owner recovery.
		return nil, err
	}
	return claim, nil
}

func recoverExpired(ctx context.Context, tx pgx.Tx, owner string, now time.Time) (*worker.Claim, error) {
	var id string
	var status runstate.Status
	var attempts int
	err := tx.QueryRow(ctx, `SELECT id::text, status, recovery_attempts FROM runs
		WHERE status <> 'queued' AND worker_id IS NOT NULL AND recovery_attempts < $3
		AND (lease_expires_at <= $1 OR (lease_expires_at IS NULL AND claimed_at < $2))
		ORDER BY claimed_at, id FOR UPDATE SKIP LOCKED LIMIT 1`,
		now, now.Add(-LeaseDuration), MaxRecoveries).Scan(&id, &status, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !status.Valid() {
		return nil, fmt.Errorf("cannot recover a Run with an unknown lifecycle state")
	}
	runID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE runs SET worker_id = $2, lease_expires_at = $3,
		recovery_attempts = recovery_attempts + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1`, runID, owner, now.Add(LeaseDuration))
	if err != nil {
		return nil, err
	}
	if !status.Terminal() {
		if err := failRun(ctx, tx, runID, status, "worker lease expired", "worker-recovery",
			map[string]any{"error": "worker lease expired", "recovery_attempt": attempts + 1}, now); err != nil {
			return nil, err
		}
	}
	return &worker.Claim{RunID: runID, Recovery: true}, nil
}

// ReconcileExit preserves terminal decisions and leaves unreleased claims in
// place for bounded cleanup recovery. Only the resource cleaner releases claims.
func (q *Queue) ReconcileExit(ctx context.Context, runID uuid.UUID, owner string) error {
	tx, err := q.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	var status runstate.Status
	var workerID *string
	var expires *time.Time
	err = tx.QueryRow(ctx, `SELECT status, worker_id, lease_expires_at FROM runs
		WHERE id = $1 FOR UPDATE`, runID).Scan(&status, &workerID, &expires)
	if err != nil {
		return err
	}
	if workerID == nil {
		return nil // The execution module already released the claim.
	}
	now := time.Now().UTC()
	if *workerID != owner || expires == nil || !expires.After(now) {
		return ErrLeaseLost
	}
	if !status.Valid() {
		return fmt.Errorf("cannot reconcile a Run with an unknown lifecycle state")
	}
	if !status.Terminal() {
		const message = "executor process exited without a terminal outcome"
		if err := failRun(ctx, tx, runID, status, message, "worker",
			map[string]any{"error": message}, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// The caller holds the Run row lock through both state and event writes. Event
// sequence allocation therefore remains serialized with resource and API writers.
func failRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, current runstate.Status,
	message, source string, data map[string]any, now time.Time) error {
	if err := runstate.Validate(current, runstate.Failed); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2,
		finished_at = $3, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id, message, now); err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events
		(id, run_id, sequence, type, source, data, raw, occurred_at)
		SELECT $1, $2, COALESCE(MAX(sequence), 0) + 1, 'run.failed', $3, $4::json, NULL, $5
		FROM events WHERE run_id = $2`, uuid.New(), id, source, payload, now)
	return err
}

func rollback(ctx context.Context, tx pgx.Tx) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}
