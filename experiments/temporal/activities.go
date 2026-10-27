package temporaleval

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

type Activities struct {
	Pool   *pgxpool.Pool
	Config execution.Config
}

// Execute's durable receipt is deliberately fail-closed, including uncertain commits.
func (a Activities) Execute(ctx context.Context, id uuid.UUID) error {
	stop := heartbeat(ctx)
	defer stop()
	tag, err := a.Pool.Exec(ctx, `UPDATE temporal_eval_admissions e SET execution_started=true
  FROM runs r WHERE e.run_id=$1 AND r.id=e.run_id AND r.backend='fake' AND NOT e.execution_started`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return temporal.NewNonRetryableApplicationError("agent invocation is already admitted or is not fake", "execution-receipt", nil)
	}
	owner := "temporal-execute:" + id.String()
	claim, err := postgres.NewQueue(a.Pool).Acquire(ctx, owner)
	if err != nil {
		return err
	}
	if claim == nil || claim.RunID != id || claim.Recovery {
		return errors.New("evaluation did not acquire its fresh Run")
	}
	supervisor, err := execution.NewSupervisor(a.Pool, owner, a.Config)
	if err != nil {
		return err
	}
	return supervisor.Execute(ctx, *claim, owner)
}

// Reconcile never starts an agent. A live lease must expire before cleanup can take over.
func (a Activities) Reconcile(ctx context.Context, id uuid.UUID) error {
	stop := heartbeat(ctx)
	defer stop()
	for {
		var status runstate.Status
		var owner *string
		var expires *time.Time
		if err := a.Pool.QueryRow(ctx, `SELECT status,worker_id,lease_expires_at FROM runs WHERE id=$1`, id).Scan(&status, &owner, &expires); err != nil {
			return err
		}
		if owner == nil && status.Terminal() {
			return nil
		}
		if owner == nil || expires == nil || !expires.After(time.Now()) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	owner := "temporal-cleanup:" + uuid.NewString()
	claim, err := postgres.NewQueue(a.Pool).Acquire(ctx, owner)
	if err != nil {
		return err
	}
	if claim == nil || claim.RunID != id {
		return errors.New("cleanup recovery unavailable; operator attention required")
	}
	if !claim.Recovery {
		// Loss after the receipt but before claiming must fail this attempt, never execute it.
		store, err := postgres.NewResources(a.Pool, owner)
		if err != nil {
			return err
		}
		if err = store.WithRun(ctx, id, func(r *postgres.RunResources) error {
			return r.RecordFailure("Temporal execution stopped before claiming the Run", nil)
		}); err != nil {
			return err
		}
		claim.Recovery = true
	}
	supervisor, err := execution.NewSupervisor(a.Pool, owner, a.Config)
	if err != nil {
		return err
	}
	return supervisor.Execute(ctx, *claim, owner)
}

func heartbeat(ctx context.Context) func() {
	done := make(chan struct{})
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	activity.RecordHeartbeat(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { close(done) }
}
