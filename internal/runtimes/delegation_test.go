package runtimes_test

import (
	"errors"
	"github.com/ruohao1/circular/internal/runtimes"
	"strings"
	"testing"
)

func TestDelegationRuntimeRequiresExactContext(t *testing.T) {
	var config runtimes.DockerConfig
	d, spec, state := simulatedDocker(t, map[string]any{}, func(c *runtimes.DockerConfig) { config = *c })
	spec.Delegation = &runtimes.DelegationMount{ContextSHA256: strings.Repeat("a", 64)}
	plan, err := d.Resolve(spec)
	if err != nil || plan.WorktreeReadOnly || !plan.DelegationContextReadOnly || plan.DelegationContextDestination != "/delegation-context" {
		t.Fatal(plan, err)
	}
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := runtimes.NewDocker(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovered.Release(t.Context(), spec.RunID, handle.ResourceID); !errors.Is(err, runtimes.ErrDiscard) {
		t.Fatal("ordinary cleanup adopted continuation", err)
	}
	if err = recovered.ReleaseDelegation(t.Context(), spec.RunID, handle.ResourceID, strings.Repeat("b", 64)); !errors.Is(err, runtimes.ErrDiscard) {
		t.Fatal("changed context adopted", err)
	}
	if err = recovered.ReleaseDelegation(t.Context(), spec.RunID, strings.Repeat("c", 64), spec.Delegation.ContextSHA256); !errors.Is(err, runtimes.ErrDiscard) {
		t.Fatal("replacement adopted", err)
	}
	if operationCount(t, state, "rm") != 0 {
		t.Fatal("mismatched cleanup removed container")
	}
	if err = recovered.ReleaseDelegation(t.Context(), spec.RunID, handle.ResourceID, spec.Delegation.ContextSHA256); err != nil {
		t.Fatal(err)
	}
	if err = d.Discard(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
}
