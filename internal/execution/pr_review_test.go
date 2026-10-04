package execution

import (
	"context"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func TestReviewDeadlineUsesOriginalDurableStart(t *testing.T) {
	started := time.Now().Add(-prreviews.ExecutionLimit - time.Second)
	ctx, stop, err := reviewExecutionContext(t.Context(), postgres.ResourceState{Kind: runstate.PRReview, StartedAt: &started})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("expired review received a new execution budget")
	}
	if _, _, err := reviewExecutionContext(t.Context(), postgres.ResourceState{Kind: runstate.PRReview}); err == nil {
		t.Fatal("missing start silently reset review clock")
	}
	coding, done, err := reviewExecutionContext(t.Context(), postgres.ResourceState{Kind: runstate.Coding})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if coding != t.Context() {
		t.Fatal("coding deadline changed")
	}
}
