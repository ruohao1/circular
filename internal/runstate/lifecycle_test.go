package runstate_test

import (
	"testing"

	"github.com/ruohao1/circular/internal/runstate"
)

// This acceptance matrix is the existing Python RunLifecycle contract, not a
// value derived from the Go implementation under test.
func TestRunTransitionContract(t *testing.T) {
	want := map[runstate.Status][]runstate.Status{
		runstate.Queued:             {runstate.Provisioning, runstate.Cancelled},
		runstate.Provisioning:       {runstate.Running, runstate.Failed, runstate.Cancelled},
		runstate.Running:            {runstate.WaitingForApproval, runstate.WaitingForInput, runstate.Finalizing, runstate.Failed, runstate.Cancelled},
		runstate.WaitingForApproval: {runstate.Running, runstate.Failed, runstate.Cancelled},
		runstate.WaitingForInput:    {runstate.Running, runstate.Failed, runstate.Cancelled},
		runstate.Finalizing:         {runstate.Succeeded, runstate.Failed, runstate.Cancelled},
		runstate.Succeeded:          {},
		runstate.Failed:             {},
		runstate.Cancelled:          {},
	}
	for current, targets := range want {
		t.Run(string(current), func(t *testing.T) {
			if !current.Valid() {
				t.Fatalf("contract state %q was rejected", current)
			}
			if current.Terminal() != (len(targets) == 0) {
				t.Fatalf("terminal classification differs for %s", current)
			}
			for target := range want {
				allowed := false
				for _, expected := range targets {
					allowed = allowed || target == expected
				}
				if got := runstate.Validate(current, target) == nil; got != allowed {
					t.Errorf("transition %s -> %s: allowed=%t, want %t", current, target, got, allowed)
				}
			}
		})
	}
}

func TestUnknownStateFailsClosed(t *testing.T) {
	unknown := runstate.Status("future_state")
	if unknown.Valid() || unknown.Terminal() {
		t.Fatal("unknown state must not be classified as valid or safely terminal")
	}
	if runstate.Validate(unknown, runstate.Failed) == nil || runstate.Validate(runstate.Running, unknown) == nil {
		t.Fatal("unknown transition was accepted")
	}
}
