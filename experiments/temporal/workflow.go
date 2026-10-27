// Package temporaleval is a disposable orchestration spike, not a production scheduler.
package temporaleval

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func RunWorkflow(ctx workflow.Context, id uuid.UUID) error {
	execute := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, HeartbeatTimeout: 2 * time.Second,
		WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	primary := workflow.ExecuteActivity(execute, "execute", id).Get(ctx, nil)
	cleanup, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	cleanup = workflow.WithActivityOptions(cleanup, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute, HeartbeatTimeout: 2 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3},
	})
	secondary := workflow.ExecuteActivity(cleanup, "reconcile", id).Get(cleanup, nil)
	if primary != nil {
		return primary
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return secondary
}
