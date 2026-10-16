package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func cancelDelegationRun(t *testing.T, f admissionFixture, id uuid.UUID) error {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(t.Context())
	if err = postgres.CancelRun(t.Context(), tx, id, "api"); err != nil {
		return err
	}
	return tx.Commit(t.Context())
}

func TestDelegationCancelRaces(t *testing.T) {
	t.Run("before-admission", func(t *testing.T) {
		f := delegationAdmissionFixture(t)
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		if err = postgres.CancelRun(t.Context(), tx, f.id, "api"); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
			done <- err
		}()
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = <-done; err == nil {
			t.Fatal("cancelled parent admitted child")
		}
	})
	t.Run("parent-cascades-only-active-child", func(t *testing.T) {
		f, child := parkedDelegation(t)
		if err := cancelDelegationRun(t, f, f.id); err != nil {
			t.Fatal(err)
		}
		if err := cancelDelegationRun(t, f, f.id); err != nil {
			t.Fatal(err)
		}
		var parentStatus, childStatus, delegationStatus string
		if err := f.pool.QueryRow(t.Context(), `SELECT p.status,c.status,d.status FROM delegations d JOIN runs p ON p.id=d.parent_run_id JOIN runs c ON c.id=d.child_run_id WHERE p.id=$1`, f.id).Scan(&parentStatus, &childStatus, &delegationStatus); err != nil || parentStatus != "cancelled" || childStatus != "cancelled" || delegationStatus != "cancelled" {
			t.Fatal(parentStatus, childStatus, delegationStatus, err)
		}
		if got := testsupport.Observe(t, f.pool, child); got.Count("run.cancelled") != 1 {
			t.Fatal(got.Events)
		}
	})
	t.Run("child-only", func(t *testing.T) {
		f, child := parkedDelegation(t)
		if err := cancelDelegationRun(t, f, child); err != nil {
			t.Fatal(err)
		}
		claim := acquire(t, postgres.NewQueue(f.pool), "resume-child-cancel")
		if claim == nil || claim.RunID != f.id || !claim.Continuation {
			t.Fatal(claim)
		}
	})
	t.Run("continuation-race", func(t *testing.T) {
		f, child := parkedDelegation(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, child); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cancelDelegationRun(t, f, f.id) }()
		claim, err := postgres.NewQueue(f.pool).Acquire(t.Context(), "race-owner")
		if err != nil {
			t.Fatal(err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if claim != nil {
			s, err := execution.NewSupervisor(f.pool, "race-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Execute(t.Context(), *claim, "race-owner"); err != nil {
				t.Fatal(err)
			}
		}
		got := testsupport.Observe(t, f.pool, f.id)
		if got.Run.Status != "cancelled" || got.Count("run.continued") != 0 {
			t.Fatal(got.Run, got.Events)
		}
		if got := testsupport.Observe(t, f.pool, child); got.Run.Status != "succeeded" {
			t.Fatal("completed history changed")
		}
	})
	t.Run("unrelated-pr-review", func(t *testing.T) {
		pool := database(t)
		snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
		review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(t.Context(), `UPDATE runs SET status='running' WHERE id=$1`, snapshot.SourceRunID); err != nil {
			t.Fatal(err)
		}
		tx, _ := pool.Begin(t.Context())
		defer tx.Rollback(t.Context())
		if err = postgres.CancelRun(t.Context(), tx, snapshot.SourceRunID, "api"); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := testsupport.Observe(t, pool, review.RunID); got.Run.Status != "queued" {
			t.Fatal("cancelled unrelated PR review", got.Run)
		}
	})
}

func TestDelegationOrphanedSuspensionCleanup(t *testing.T) {
	f, child := parkedDelegation(t)
	if err := cancelDelegationRun(t, f, f.id); err != nil {
		t.Fatal(err)
	}
	q := postgres.NewQueue(f.pool)
	claim := acquire(t, q, "cleanup-owner")
	if claim == nil || claim.RunID != f.id || !claim.Recovery || claim.Continuation || claim.Invocation != 1 {
		t.Fatal("stranded suspension", claim)
	}
	s, err := execution.NewSupervisor(f.pool, "cleanup-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Execute(t.Context(), *claim, "cleanup-owner"); err != nil {
		t.Fatal(err)
	}
	got := testsupport.Observe(t, f.pool, f.id)
	if got.Run.Status != "cancelled" || got.Run.WorkerID != nil || got.Workspace.Status != "released" || got.Count("run.started") != 1 || got.Count("run.continued") != 0 {
		t.Fatal(got)
	}
	if got := testsupport.Observe(t, f.pool, child); got.Run.Status != "cancelled" {
		t.Fatal(got.Run)
	}
	if claim := acquire(t, q, "nothing-left"); claim != nil {
		t.Fatal(claim)
	}
}

func TestDelegationRecoveryAtHandoffBoundaries(t *testing.T) {
	t.Run("admission-rollback", func(t *testing.T) {
		f := delegationAdmissionFixture(t)
		if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION fail_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='waiting_for_input' THEN RAISE EXCEPTION 'fixture commit failure'; END IF; RETURN NEW; END $$;
		CREATE CONSTRAINT TRIGGER fail_handoff AFTER UPDATE ON runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_handoff()`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint); err == nil {
			t.Fatal("commit fault ignored")
		}
		var children int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&children); err != nil || children != 0 {
			t.Fatal("partial admission", children, err)
		}
		if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER fail_handoff ON runs`); err != nil {
			t.Fatal(err)
		}
		first, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
		if err != nil || first.ID != replay.ID || first.ChildRunID == nil {
			t.Fatal("acknowledgement replay", replay, err)
		}
	})
	t.Run("safe-park-and-ambiguous-continuation", func(t *testing.T) {
		f, child := parkedDelegation(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='running',worker_id='child-owner',claimed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '5 minutes' WHERE id=$1`, child); err != nil {
			t.Fatal(err)
		}
		q := postgres.NewQueue(f.pool)
		if claim := acquire(t, q, "restart"); claim != nil {
			t.Fatal("replayed parked parent", claim)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='failed',worker_id=NULL,lease_expires_at=NULL WHERE id=$1`, child); err != nil {
			t.Fatal(err)
		}
		claim := acquire(t, q, "resume-owner")
		if claim == nil || claim.RunID != f.id || claim.Invocation != 2 {
			t.Fatal(claim)
		}
		store, _ := postgres.NewResources(f.pool, "resume-owner")
		store, _ = store.ForInvocation(2)
		if err := store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error { _, _, _, err := r.ContinuationContext(); return err }); err != nil {
			t.Fatal(err)
		}
		expire(t, f.pool, f.id)
		recovery := acquire(t, q, "recovery-owner")
		if recovery == nil || !recovery.Recovery || recovery.Continuation || recovery.Invocation != 2 {
			t.Fatal("replayed active invocation", recovery)
		}
		if _, err := store.Heartbeat(t.Context(), f.id); !errors.Is(err, postgres.ErrLeaseLost) {
			t.Fatal("old owner wrote", err)
		}
		var lifecycle string
		if err := f.pool.QueryRow(t.Context(), `SELECT status FROM delegations WHERE parent_run_id=$1`, f.id).Scan(&lifecycle); err != nil || lifecycle != "completed" {
			t.Fatal(lifecycle, err)
		}
		for range postgres.MaxRecoveries - 1 {
			expire(t, f.pool, f.id)
			if c := acquire(t, q, "bounded-recovery"); c == nil || !c.Recovery {
				t.Fatal(c)
			}
		}
		expire(t, f.pool, f.id)
		if c := acquire(t, q, "exhausted"); c != nil {
			t.Fatal("unbounded recovery", c)
		}
	})
	// A cancellation transaction that rolls back cannot stop admitted work.
	t.Run("cancel-rollback", func(t *testing.T) {
		f, _ := parkedDelegation(t)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		tx, _ := f.pool.Begin(ctx)
		if err := postgres.CancelRun(ctx, tx, f.id, "api"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if got := testsupport.Observe(t, f.pool, f.id); got.Run.Status != "waiting_for_input" {
			t.Fatal(got.Run)
		}
	})
}
