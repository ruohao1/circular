// Package worker consumes durable claims without owning backend reasoning loops.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Claim is committed before an Executor can allocate any Run resources.
type Claim struct {
	RunID    uuid.UUID
	Recovery bool
}

type Queue interface {
	Acquire(context.Context, string) (*Claim, error)
	ReconcileExit(context.Context, uuid.UUID, string) error
}

type Executor interface {
	Execute(context.Context, Claim, string) error
}

// Run processes one Run at a time, matching the existing Python worker. Scale
// with distinct workers; PostgreSQL's row lock is the only claim authority.
func Run(ctx context.Context, queue Queue, executor Executor, workerID string, poll time.Duration) error {
	if err := ValidateID(workerID); err != nil {
		return err
	}
	if poll <= 0 {
		return fmt.Errorf("worker ID and positive polling interval are required")
	}
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		claim, err := queue.Acquire(claimCtx, workerID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("acquire Run: %w", err)
		}
		if claim == nil {
			timer := time.NewTimer(poll)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		slog.Info("Run claimed", "run_id", claim.RunID, "recovery", claim.Recovery)
		if err := executor.Execute(ctx, *claim, workerID); err != nil {
			// Do not expose subprocess arguments, environment values, or arbitrary
			// backend output in a persisted error. Execution logs remain separate.
			slog.Warn("Run executor exited unsuccessfully", "run_id", claim.RunID)
		}
		// A child crash must not leave an active Run ownerless. This bounded,
		// cancellation-independent write retains the claim for normal recovery;
		// it never releases resources or replaces an existing terminal outcome.
		settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = queue.ReconcileExit(settleCtx, claim.RunID, workerID)
		settleCancel()
		if err != nil {
			slog.Warn("Run exit reconciliation deferred to lease recovery", "run_id", claim.RunID)
		}
	}
	return nil
}
