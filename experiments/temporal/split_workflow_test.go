package temporaleval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestSplitWorkflowUsesDurableLeaseWaitAndRetriesOnlyCleanup(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	start := env.Now()
	executions, claims, retentions, releases := 0, 0, 0, 0
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error { executions++; return errors.New("original backend failure") }, activity.RegisterOptions{Name: "execute"})
	env.RegisterActivityWithOptions(func(_ context.Context, id uuid.UUID) (CleanupAdmission, error) {
		claims++
		if claims == 1 {
			return CleanupAdmission{Wait: time.Minute}, nil
		}
		return CleanupAdmission{Claim: CleanupClaim{RunID: id, Owner: uuid.NewString()}}, nil
	}, activity.RegisterOptions{Name: "claim-cleanup"})
	env.RegisterActivityWithOptions(func(context.Context, CleanupClaim) error {
		retentions++
		if retentions == 1 {
			return errors.New("retain unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: "retain"})
	env.RegisterActivityWithOptions(func(context.Context, CleanupClaim) error {
		releases++
		if releases == 1 {
			return errors.New("release unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: "release"})
	env.ExecuteWorkflow(SplitWorkflow, uuid.New())
	err := env.GetWorkflowError()
	if executions != 1 || claims != 4 || retentions != 3 || releases != 2 || err == nil || !strings.Contains(err.Error(), "original backend failure") {
		t.Fatalf("execute=%d claims=%d retain=%d release=%d error=%v", executions, claims, retentions, releases, err)
	}
	if env.Now().Sub(start) < time.Minute {
		t.Fatal("live lease was polled without a durable wait")
	}
}

func TestSplitWorkflowBoundsRecoveryAfterRetentionFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, claims, retentions := 0, 0, 0
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error { executions++; return nil }, activity.RegisterOptions{Name: "execute"})
	env.RegisterActivityWithOptions(func(_ context.Context, id uuid.UUID) (CleanupAdmission, error) {
		claims++
		return CleanupAdmission{Claim: CleanupClaim{RunID: id, Owner: "owner"}}, nil
	}, activity.RegisterOptions{Name: "claim-cleanup"})
	env.RegisterActivityWithOptions(func(context.Context, CleanupClaim) error { retentions++; return errors.New("retention failure") }, activity.RegisterOptions{Name: "retain"})
	env.RegisterActivityWithOptions(func(context.Context, CleanupClaim) error {
		t.Error("release ran before retention succeeded")
		return nil
	}, activity.RegisterOptions{Name: "release"})
	env.ExecuteWorkflow(SplitWorkflow, uuid.New())
	err := env.GetWorkflowError()
	if executions != 1 || claims != 3 || retentions != 3 || err == nil || !strings.Contains(err.Error(), "retention failure") {
		t.Fatalf("execute=%d claim=%d retain=%d error=%v", executions, claims, retentions, err)
	}
}

func TestSplitWorkflowBoundsWaitingForAnUnavailableOwner(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	claims := 0
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error { return nil }, activity.RegisterOptions{Name: "execute"})
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) (CleanupAdmission, error) {
		claims++
		return CleanupAdmission{Wait: time.Minute}, nil
	}, activity.RegisterOptions{Name: "claim-cleanup"})
	env.ExecuteWorkflow(SplitWorkflow, uuid.New())
	if err := env.GetWorkflowError(); err == nil || claims < 2 || claims > 7 {
		t.Fatalf("lease wait was not bounded: calls=%d error=%v", claims, err)
	}
}

func TestSplitWorkflowRetriesClaimDeliveryWithoutRepeatingCoding(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	executions, claims := 0, 0
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) error { executions++; return nil }, activity.RegisterOptions{Name: "execute"})
	env.RegisterActivityWithOptions(func(context.Context, uuid.UUID) (CleanupAdmission, error) {
		claims++
		if claims < 3 {
			return CleanupAdmission{}, errors.New("claim response unavailable")
		}
		return CleanupAdmission{Done: true}, nil
	}, activity.RegisterOptions{Name: "claim-cleanup"})
	env.ExecuteWorkflow(SplitWorkflow, uuid.New())
	if err := env.GetWorkflowError(); err != nil || executions != 1 || claims != 3 {
		t.Fatalf("execute=%d claims=%d error=%v", executions, claims, err)
	}
}
