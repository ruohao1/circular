package temporaleval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestWorkflowDoesNotRetryExecutionAndPreservesPrimaryFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, cleanups := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error {
		executions++
		return errors.New("original backend failure")
	}, activity.RegisterOptions{Name: "execute"})
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error {
		cleanups++
		return errors.New("cleanup unavailable")
	}, activity.RegisterOptions{Name: "reconcile"})
	env.ExecuteWorkflow(RunWorkflow, uuid.New())
	err := env.GetWorkflowError()
	if executions != 1 || cleanups != 3 || err == nil || !strings.Contains(err.Error(), "original backend failure") {
		t.Fatalf("executions=%d cleanups=%d error=%v", executions, cleanups, err)
	}
}
