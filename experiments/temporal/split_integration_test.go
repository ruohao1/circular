package temporaleval

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestSplitRetentionAndReleaseAreSeparateAndIdempotent(t *testing.T) {
	f := newFixture(t, nil)
	a := SplitActivities{f.a}
	sql(t, f.a.Pool, `ALTER TABLE events ADD CONSTRAINT fault_artifact CHECK(type<>'artifact.created')`)
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("retention fault was not injected")
	}
	before := testsupport.Observe(t, f.a.Pool, f.id)
	waiting, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	if waiting.Wait <= 0 || waiting.Claim.Owner != "" {
		t.Fatal("live execution lease was stolen")
	}
	sql(t, f.a.Pool, `ALTER TABLE events DROP CONSTRAINT fault_artifact`)
	f.expire(t)
	admission, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	if admission.Claim.Owner == "" {
		t.Fatal("no cleanup claim")
	}
	must(t, a.Retain(t.Context(), admission.Claim))
	retained := testsupport.Observe(t, f.a.Pool, f.id)
	if len(retained.Artifacts) != 2 || retained.Workspace.Status == "released" || !exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String())) {
		t.Fatal("retention released the worktree")
	}
	must(t, a.Retain(t.Context(), admission.Claim))
	if len(testsupport.Observe(t, f.a.Pool, f.id).Events) != len(retained.Events) {
		t.Fatal("repeated retention added events")
	}
	must(t, a.Release(t.Context(), admission.Claim))
	f.assertReleased(t, "failed", true)
	released := testsupport.Observe(t, f.a.Pool, f.id)
	must(t, a.Release(t.Context(), admission.Claim))
	must(t, a.Retain(t.Context(), admission.Claim))
	if len(testsupport.Observe(t, f.a.Pool, f.id).Events) != len(released.Events) || f.starts(t) != 1 || before.Run.Error == nil || released.Run.Error == nil || *before.Run.Error != *released.Run.Error {
		t.Fatal("separate recovery repeated effects or changed the primary error")
	}
}

func TestSplitActivitiesFenceAnExpiredCleanupOwner(t *testing.T) {
	f := newFixture(t, map[string]any{"fake_workload": true, "remove_fails": true})
	a := SplitActivities{f.a}
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("cleanup fault was not injected")
	}
	f.setOptions(t, map[string]any{"fake_workload": true})
	f.expire(t)
	first, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	must(t, a.Retain(t.Context(), first.Claim))
	f.expire(t)
	second, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	if first.Claim.Owner == second.Claim.Owner {
		t.Fatal("recovery reused an expired owner")
	}
	if err = a.Retain(t.Context(), first.Claim); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale retention accepted: %v", err)
	}
	if err = a.Release(t.Context(), first.Claim); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale release accepted: %v", err)
	}
	must(t, a.Release(t.Context(), second.Claim))
	f.assertReleased(t, "succeeded", true)
	if f.starts(t) != 1 {
		t.Fatal("cleanup owner transfer repeated execution")
	}
}

func TestSplitRecoveryCapPreservesOutputForOperatorAttention(t *testing.T) {
	f := newFixture(t, map[string]any{"fake_workload": true, "remove_fails": true})
	a := SplitActivities{f.a}
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("cleanup fault was not injected")
	}
	for range postgres.MaxRecoveries {
		f.expire(t)
		admission, err := a.ClaimCleanup(t.Context(), f.id)
		must(t, err)
		if err = a.Retain(t.Context(), admission.Claim); err == nil {
			t.Fatal("failed container removal was hidden")
		}
	}
	f.expire(t)
	if _, err := a.ClaimCleanup(t.Context(), f.id); err == nil {
		t.Fatal("database recovery cap was bypassed")
	}
	s := testsupport.Observe(t, f.a.Pool, f.id)
	if s.Run.Status != "succeeded" || s.Run.WorkerID == nil || s.Workspace.Status == "released" || !exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String(), "circular-result-"+f.id.String()+".txt")) || f.starts(t) != 1 {
		t.Fatal("exhaustion discarded output or altered execution")
	}
	var attention bool
	must(t, f.a.Pool.QueryRow(t.Context(), `SELECT needs_user_resolution FROM work_sessions WHERE run_id=$1`, f.id).Scan(&attention))
	if !attention {
		t.Fatal("native Work session lost its attention flag")
	}
}

func TestSplitClaimResultLossKeepsTheExistingOwnerUntilExpiry(t *testing.T) {
	f := newFixture(t, map[string]any{"fake_workload": true, "remove_fails": true})
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("cleanup fault was not injected")
	}
	f.setOptions(t, map[string]any{"fake_workload": true})
	f.expire(t)
	a := SplitActivities{f.a}
	lost, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	// Discard the returned permit, as if its response never reached the Workflow.
	retry, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	if retry.Wait <= 0 || retry.Claim.Owner != "" {
		t.Fatal("lost response created a second live cleanup owner")
	}
	var owner string
	must(t, f.a.Pool.QueryRow(t.Context(), `SELECT worker_id FROM runs WHERE id=$1`, f.id).Scan(&owner))
	if owner != lost.Claim.Owner {
		t.Fatal("retry replaced a live owner")
	}
	f.expire(t)
	replacement, err := a.ClaimCleanup(t.Context(), f.id)
	must(t, err)
	if replacement.Claim.Owner == lost.Claim.Owner {
		t.Fatal("expired permit was reused")
	}
	if err = a.Retain(t.Context(), lost.Claim); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("lost permit remained usable: %v", err)
	}
	must(t, a.Retain(t.Context(), replacement.Claim))
	must(t, a.Release(t.Context(), replacement.Claim))
	f.assertReleased(t, "succeeded", true)
	if f.starts(t) != 1 {
		t.Fatal("claim response loss repeated coding")
	}
}
