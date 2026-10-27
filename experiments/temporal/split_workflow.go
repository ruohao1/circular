package temporaleval

import (
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

type CleanupClaim struct {
	RunID uuid.UUID
	Owner string
}
type CleanupAdmission struct {
	Claim CleanupClaim
	Wait  time.Duration
	Done  bool
}

// SplitWorkflow changes cleanup recovery only; initial execution is identical to RunWorkflow.
func SplitWorkflow(ctx workflow.Context, id uuid.UUID) error {
	execute := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 2 * time.Second,
		WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	primary := workflow.ExecuteActivity(execute, "execute", id).Get(ctx, nil)
	cleanup, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	secondary := CleanupWorkflow(cleanup, id)
	if primary != nil {
		return primary
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return secondary
}

// CleanupWorkflow uses durable timers between fresh, lease-fenced cleanup attempts.
func CleanupWorkflow(ctx workflow.Context, id uuid.UUID) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute, HeartbeatTimeout: 2 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	claimCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second, HeartbeatTimeout: 2 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second}})
	deadline := workflow.Now(ctx).Add(6 * time.Minute)
	var last error
	for attempts := 0; attempts < postgres.MaxRecoveries; {
		if !workflow.Now(ctx).Before(deadline) {
			return temporal.NewNonRetryableApplicationError("cleanup wait expired; operator attention required", "cleanup-budget", last)
		}
		var admission CleanupAdmission
		if err := workflow.ExecuteActivity(claimCtx, "claim-cleanup", id).Get(ctx, &admission); err != nil {
			return err
		}
		if admission.Done {
			return nil
		}
		if admission.Wait > 0 {
			wait := min(admission.Wait, deadline.Sub(workflow.Now(ctx)))
			if err := workflow.Sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		attempts++
		last = workflow.ExecuteActivity(ctx, "retain", admission.Claim).Get(ctx, nil)
		if last == nil {
			last = workflow.ExecuteActivity(ctx, "release", admission.Claim).Get(ctx, nil)
		}
		if last == nil {
			return nil
		}
	}
	return last
}
