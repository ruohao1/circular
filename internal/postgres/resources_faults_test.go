package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestResourceTransactionsFenceTakeoverAndRejectClosedOrStaleCallbacks(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	queue := postgres.NewQueue(pool)
	acquire(t, queue, "first-owner")
	store, err := postgres.NewResources(pool, "first-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE runs SET lease_expires_at=$2 WHERE id=$1`, id, time.Now().Add(250*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var transaction *postgres.RunResources
	go func() {
		done <- store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { transaction = r; close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first owner could not enter: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Run lock timed out")
	}
	<-time.After(300 * time.Millisecond)
	if claim := acquire(t, queue, "replacement"); claim != nil {
		t.Fatal("recovery stole a Run while its resource transaction held the lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.State(); !errors.Is(err, postgres.ErrTransactionClosed) {
		t.Fatalf("escaped transaction remains usable: %v", err)
	}
	claim := acquire(t, queue, "replacement")
	if claim == nil || !claim.Recovery || claim.RunID != id {
		t.Fatal("expired owner was not recoverable after releasing its lock")
	}
	called := false
	if err := store.WithRun(t.Context(), id, func(*postgres.RunResources) error { called = true; return nil }); !errors.Is(err, postgres.ErrLeaseLost) || called {
		t.Fatal("stale owner reached a resource callback")
	}
}

func TestWorkspaceIdentityConflictsAndFinalEventFailureCannotPartiallyCommit(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "owner")
	store, err := postgres.NewResources(pool, "owner")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id.String())
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.CreatePending(path); return err }); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.CreatePending(path + "-foreign"); return err }); !errors.Is(err, postgres.ErrResourceConflict) {
		t.Fatal("Workspace path was replaced")
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.RecordContainer("first-container"); return err }); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.RecordContainer("replacement-container"); return err }); !errors.Is(err, postgres.ErrResourceConflict) {
		t.Fatal("immutable container identity was replaced")
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_run_started CHECK (type <> 'run.started')`); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.MarkRunning(); return err }); err == nil {
		t.Fatal("handoff ignored an event persistence failure")
	}
	state, err := store.Read(t.Context(), id)
	if err != nil || state.Status != "provisioning" || state.Workspace.Status != "pending" || *state.Workspace.ContainerID != "first-container" {
		t.Fatal("partial handoff became visible")
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT reject_run_started`); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.MarkRunning(); return err }); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { return r.ReleaseWorkspace() }); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatal("nonterminal Run released its Workspace")
	}
}

func TestDiffEventFailureRollsBackArtifactAndConcurrentRetryPreservesReplay(t *testing.T) {
	f := retentionFixture(t)
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_diff_event CHECK (type <> 'git.diff.updated')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.retention.Finalize(t.Context(), f.worktree.RunID); err == nil {
		t.Fatal("diff event failure was ignored")
	}
	state, err := f.store.Read(t.Context(), f.worktree.RunID)
	if err != nil || len(state.Artifacts) != 0 {
		t.Fatal("failed Event transaction leaked an Artifact")
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT reject_diff_event`); err != nil {
		t.Fatal(err)
	}
	uri, _ := artifacts.URI(f.worktree.RunID, "git-diff.patch")
	data, err := f.content.Read(t.Context(), f.worktree.RunID, uri)
	if err != nil || len(data) == 0 {
		t.Fatal("durable bytes were not available for a persistence retry")
	}
	content, err := f.content.Write(t.Context(), f.worktree.RunID, "git-diff.patch", data)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 8)
	for range cap(done) {
		go func() {
			done <- f.store.WithRun(t.Context(), f.worktree.RunID, func(r *postgres.RunResources) error {
				_, err := r.PersistDiff(f.worktree.Path, content, 1, false)
				return err
			})
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	conflict := content
	conflict.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := f.store.WithRun(t.Context(), f.worktree.RunID, func(r *postgres.RunResources) error {
		_, err := r.PersistDiff(f.worktree.Path, conflict, 1, false)
		return err
	}); !errors.Is(err, postgres.ErrResourceConflict) {
		t.Fatal("conflicting final diff metadata was overwritten")
	}
	s := testsupport.Observe(t, f.pool, f.worktree.RunID)
	s.AssertReplay(t)
	if len(s.Events) != 4 || len(s.Artifacts) != 1 || s.Events[2].Type != "artifact.created" || s.Events[3].Type != "git.diff.updated" {
		t.Fatal(s)
	}
}

func TestStaleRetentionAndCleanupCannotTouchResources(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	expire(t, f.pool, f.worktree.RunID)
	claim := acquire(t, postgres.NewQueue(f.pool), "replacement-owner")
	if claim == nil || claim.RunID != f.worktree.RunID {
		t.Fatal("fixture takeover failed")
	}
	if err := f.retention.Retain(t.Context(), f.worktree.RunID); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale retention accepted: %v", err)
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale cleanup accepted: %v", err)
	}
	if _, err := os.Stat(f.worktree.Path); err != nil {
		t.Fatal("stale owner removed current resources")
	}
	newOwner, err := postgres.NewResources(f.pool, "replacement-owner")
	if err != nil {
		t.Fatal(err)
	}
	state, err := newOwner.Read(t.Context(), f.worktree.RunID)
	if err != nil || state.Workspace.Status != "pending" || len(state.Artifacts) != 0 {
		t.Fatal("stale owner altered the replacement's audit state")
	}
}

func TestCancelledResourceTransactionNeverPublishesWorkspace(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "owner")
	store, err := postgres.NewResources(pool, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	path := filepath.Join(t.TempDir(), id.String())
	err = store.WithRun(ctx, id, func(r *postgres.RunResources) error {
		if _, err := r.CreatePending(path); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
	state, err := store.Read(t.Context(), id)
	if err != nil || state.Workspace != nil {
		t.Fatal("cancelled transaction published its Workspace")
	}
	if err := store.WithRun(t.Context(), uuid.New(), func(*postgres.RunResources) error { t.Fatal("missing Run reached callback"); return nil }); !errors.Is(err, postgres.ErrRunUnavailable) {
		t.Fatalf("missing Run was not rejected: %v", err)
	}
}
