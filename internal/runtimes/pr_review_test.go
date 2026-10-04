package runtimes_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestReviewPolicyFixesReadOnlyMountsAndInputIdentity(t *testing.T) {
	root := t.TempDir()
	d, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: filepath.Join(root, "worktrees"), ReviewContextRoot: filepath.Join(root, "review-contexts")})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	spec := runtimes.Spec{RunID: id, Image: "review-fixture:test", Worktree: filepath.Join(root, "worktrees", id.String()), CPULimit: 1, MemoryLimitMB: 256, TemporaryStorageMB: 128, Kind: runstate.PRReview, Review: &runtimes.ReviewMount{ContextSHA256: strings.Repeat("a", 64)}}
	first, err := d.Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !first.WorktreeReadOnly || !first.ReviewContextReadOnly || first.WorktreeDestination != "/workspace" || first.ReviewContextDestination != "/review-context" {
		t.Fatal("unsafe mounts", first)
	}
	spec.Review.ContextSHA256 = strings.Repeat("b", 64)
	changed, err := d.Resolve(spec)
	if err != nil || changed.PolicyDigest == first.PolicyDigest {
		t.Fatal("context not bound to identity", err)
	}
	spec.Kind = runstate.Coding
	if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatal("coding accepted review mount", err)
	}
	spec.Kind = runstate.PRReview
	spec.Review = nil
	if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatal("missing context accepted", err)
	}
	spec.Review = &runtimes.ReviewMount{ContextSHA256: strings.Repeat("a", 64)}
	if err := os.MkdirAll(filepath.Join(root, "review-contexts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "review-contexts", id.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatal("linked context accepted", err)
	}
	for _, reviewRoot := range []string{filepath.Join(root, "worktrees"), filepath.Join(root, "worktrees", "review-contexts"), root} {
		if _, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: filepath.Join(root, "worktrees"), ReviewContextRoot: reviewRoot}); err == nil {
			t.Fatal("overlap accepted", reviewRoot)
		}
	}
}

func TestReviewRecoveryRequiresExactReadOnlyContextIdentity(t *testing.T) {
	var config runtimes.DockerConfig
	d, spec, state := simulatedDocker(t, map[string]any{}, func(c *runtimes.DockerConfig) {
		c.ReviewContextRoot = filepath.Join(filepath.Dir(c.WorktreeRoot), "review-contexts")
		config = *c
	})
	spec.Kind = runstate.PRReview
	spec.Review = &runtimes.ReviewMount{ContextSHA256: strings.Repeat("a", 64)}
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := runtimes.NewDocker(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Release(t.Context(), spec.RunID, handle.ResourceID); !errors.Is(err, runtimes.ErrDiscard) {
		t.Fatal("coding recovery adopted review", err)
	}
	if err := recovered.ReleaseReview(t.Context(), spec.RunID, handle.ResourceID, strings.Repeat("b", 64)); !errors.Is(err, runtimes.ErrDiscard) {
		t.Fatal("changed context recovered", err)
	}
	if operationCount(t, state, "rm") != 0 {
		t.Fatal("mismatched recovery removed container")
	}
	if err := recovered.ReleaseReview(t.Context(), spec.RunID, handle.ResourceID, spec.Review.ContextSHA256); err != nil {
		t.Fatal(err)
	}
	if err := d.Discard(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
}
