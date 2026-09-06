package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/worker"
)

type testQueue struct {
	claim      *worker.Claim
	settled    []uuid.UUID
	acquireErr error
}

func (q *testQueue) Acquire(context.Context, string) (*worker.Claim, error) {
	claim := q.claim
	q.claim = nil
	return claim, q.acquireErr
}

func (q *testQueue) ReconcileExit(ctx context.Context, id uuid.UUID, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.settled = append(q.settled, id)
	return nil
}

type executorFunc func(context.Context, worker.Claim, string) error

func (f executorFunc) Execute(ctx context.Context, claim worker.Claim, id string) error {
	return f(ctx, claim, id)
}

func TestExecutorFailureAndShutdownStillSettleTheClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := uuid.New()
	queue := &testQueue{claim: &worker.Claim{RunID: id, Recovery: true}}
	executor := executorFunc(func(_ context.Context, claim worker.Claim, owner string) error {
		if claim.RunID != id || !claim.Recovery || owner != "worker-one" {
			t.Fatal("committed claim was not passed intact to executor")
		}
		cancel()
		return errors.New("injected executor crash")
	})
	if err := worker.Run(ctx, queue, executor, "worker-one", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(queue.settled) != 1 || queue.settled[0] != id {
		t.Fatal("shutdown skipped exit reconciliation")
	}
}

func TestFailedClaimNeverStartsExecutor(t *testing.T) {
	queue := &testQueue{acquireErr: errors.New("claim commit uncertain")}
	executor := executorFunc(func(context.Context, worker.Claim, string) error {
		t.Fatal("executor started without a confirmed claim")
		return nil
	})
	if err := worker.Run(context.Background(), queue, executor, "worker", time.Millisecond); err == nil {
		t.Fatal("claim error was ignored")
	}
}

func TestIdlePollingCanBeStoppedPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := worker.Run(ctx, &testQueue{}, executorFunc(func(context.Context, worker.Claim, string) error {
		t.Fatal("idle worker must not execute a Run")
		return nil
	}), "worker", time.Hour); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited for the entire polling interval")
	}
}
