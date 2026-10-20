package postgres_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func nativeWorkView(t *testing.T, f planningFixture, run uuid.UUID) postgres.WorkSessionView {
	t.Helper()
	var id uuid.UUID
	if err := f.p.QueryRow(t.Context(), `SELECT id FROM work_sessions WHERE run_id=$1`, run).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return inspectWork(t, f, id, time.Now().UTC())
}
func workResources(t *testing.T, f planningFixture, owner string) *postgres.Resources {
	t.Helper()
	r, err := postgres.NewResources(f.p, owner)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func startWork(t *testing.T, r *postgres.Resources, run uuid.UUID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), run.String())
	if err := r.WithRun(t.Context(), run, func(s *postgres.RunResources) error {
		if _, err := s.CreatePending(path); err != nil {
			return err
		}
		if _, err := s.RecordContainer("fixture-container"); err != nil {
			return err
		}
		_, err := s.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func releaseWork(t *testing.T, f planningFixture, in postgres.WorkAdminInput) (postgres.WorkChangeResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
		return postgres.ReleaseTaskWork(t.Context(), tx, in, time.Now().UTC())
	})
}

func TestNativeWorkLifecycleAndCleanup(t *testing.T) {
	f := planningSeed(t)
	task := f.task(t, "Native", "open", "{}")
	run, err := nativeLaunch(t, f, task, "work")
	if err != nil {
		t.Fatal(err)
	}
	initial := nativeWorkView(t, f, run.RunID)
	if initial.StartedAt != nil || initial.LastContactAt != nil || initial.ContactState != "queued" {
		t.Fatal(initial)
	}
	acquire(t, postgres.NewQueue(f.p), "owner")
	r := workResources(t, f, "owner")
	acquired := nativeWorkView(t, f, run.RunID)
	if acquired.LastContactAt == nil || acquired.StartedAt != nil {
		t.Fatal("acquisition contact missing", acquired)
	}
	startWork(t, r, run.RunID)
	started := nativeWorkView(t, f, run.RunID)
	var runStarted time.Time
	if err = f.p.QueryRow(t.Context(), `SELECT started_at FROM runs WHERE id=$1`, run.RunID).Scan(&runStarted); err != nil || started.StartedAt == nil || !started.StartedAt.Equal(runStarted) {
		t.Fatal(started, err)
	}
	if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error {
		if err := s.BeginFinalizing(); err != nil {
			return err
		}
		return s.Complete()
	}); err != nil {
		t.Fatal(err)
	}
	outcome := nativeWorkView(t, f, run.RunID)
	if outcome.Outcome == nil || *outcome.Outcome != "succeeded" || outcome.ReleasedAt != nil || outcome.ContactState != "cleanup_pending" {
		t.Fatal(outcome)
	}
	if _, err = r.Heartbeat(t.Context(), run.RunID); err != nil {
		t.Fatal(err)
	}
	after := nativeWorkView(t, f, run.RunID)
	if !reflect.DeepEqual(outcome.LastContactAt, after.LastContactAt) {
		t.Fatal("cleanup renewed coding contact")
	}
	if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error { return s.ReleaseClaim() }); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatal("released unclean workspace", err)
	}
	if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error { return s.RecordCleanupFailure("Retry cleanup") }); err != nil {
		t.Fatal(err)
	}
	if nativeWorkView(t, f, run.RunID).ReleasedAt != nil {
		t.Fatal("cleanup failure released owner")
	}
	if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error {
		if err := s.ReleaseWorkspace(); err != nil {
			return err
		}
		return s.ReleaseClaim()
	}); err != nil {
		t.Fatal(err)
	}
	after = nativeWorkView(t, f, run.RunID)
	if after.ReleasedAt == nil || after.ContactState != "released" || currentWork(t, f, task) != nil {
		t.Fatal(after)
	}
}

func TestNativeRecoveryKeepsTaskOwnership(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "exhausted"}[exhaust], func(t *testing.T) {
			f := planningSeed(t)
			task := f.task(t, "Recovery", "open", "{}")
			run, err := nativeLaunch(t, f, task, "work")
			if err != nil {
				t.Fatal(err)
			}
			q := postgres.NewQueue(f.p)
			acquire(t, q, "old")
			expire(t, f.p, run.RunID)
			if before := nativeWorkView(t, f, run.RunID); before.ContactState != "needs_attention" || before.ReleasedAt != nil {
				t.Fatal("expiry not projected", before)
			}
			claim := acquire(t, q, "recovery")
			if claim == nil || !claim.Recovery {
				t.Fatal(claim)
			}
			recovered := nativeWorkView(t, f, run.RunID)
			if recovered.ReleasedAt != nil || !recovered.NeedsUserResolution || recovered.Outcome == nil || *recovered.Outcome != "failed" {
				t.Fatal(recovered)
			}
			if _, err = workResources(t, f, "old").Heartbeat(t.Context(), run.RunID); !errors.Is(err, postgres.ErrLeaseLost) {
				t.Fatal(err)
			}
			if exhaust {
				for i := 1; i < postgres.MaxRecoveries; i++ {
					expire(t, f.p, run.RunID)
					if acquire(t, q, "recovery") == nil {
						t.Fatal("early exhaustion")
					}
				}
				expire(t, f.p, run.RunID)
				if acquire(t, q, "exhausted") != nil || nativeWorkView(t, f, run.RunID).ReleasedAt != nil {
					t.Fatal("exhaustion released ownership")
				}
				return
			}
			if err = workResources(t, f, "recovery").WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error { return s.ReleaseClaim() }); err != nil {
				t.Fatal(err)
			}
			held := nativeWorkView(t, f, run.RunID)
			if held.ReleasedAt != nil || !held.NeedsUserResolution || currentWork(t, f, task) == nil {
				t.Fatal("recovery cleanup freed task", held)
			}
			result, err := releaseWork(t, f, postgres.WorkAdminInput{TaskID: task, ExpectedSessionID: held.ID, OperationID: uuid.New(), Reason: "Investigated abandoned work"})
			if err != nil || result.PendingCleanup || result.Session.ReleasedAt == nil {
				t.Fatal(result, err)
			}
		})
	}
}

func TestNativeTaskReleaseWaitsForCleanup(t *testing.T) {
	for _, mode := range []string{"queued", "active", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := planningSeed(t)
			task := f.task(t, "Release", "ready", "{}")
			run, err := nativeLaunch(t, f, task, "work")
			if err != nil {
				t.Fatal(err)
			}
			r := workResources(t, f, "owner")
			if mode != "queued" {
				acquire(t, postgres.NewQueue(f.p), "owner")
				startWork(t, r, run.RunID)
			}
			if mode == "terminal" {
				if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error { return s.RecordFailure("Execution failed", nil) }); err != nil {
					t.Fatal(err)
				}
			}
			view := nativeWorkView(t, f, run.RunID)
			admin := postgres.WorkAdminInput{TaskID: task, ExpectedSessionID: view.ID, OperationID: uuid.New(), Reason: "Stop and release"}
			result, err := releaseWork(t, f, admin)
			if err != nil || result.PendingCleanup != (mode != "queued") {
				t.Fatal(result, err)
			}
			if mode != "queued" {
				if result.Session.ReleasedAt != nil {
					t.Fatal("released before cleanup")
				}
				if err = r.WithRun(t.Context(), run.RunID, func(s *postgres.RunResources) error {
					if err := s.ReleaseWorkspace(); err != nil {
						return err
					}
					return s.ReleaseClaim()
				}); err != nil {
					t.Fatal(err)
				}
			}
			replay, err := releaseWork(t, f, admin)
			if err != nil || replay.Receipt.ID != result.Receipt.ID || replay.PendingCleanup || replay.Session.ReleasedAt == nil {
				t.Fatal(replay, err)
			}
		})
	}
}

func TestUnownedCodingRunDoesNotBlockValidQueue(t *testing.T) {
	p := database(t)
	ids := seed(t, p, 2)
	if _, err := p.Exec(t.Context(), `DELETE FROM work_sessions WHERE run_id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	q := postgres.NewQueue(p)
	claim := acquire(t, q, "valid")
	if claim == nil || claim.RunID != ids[1] {
		t.Fatal("unowned coding Run acquired", claim)
	}
	snapshot := testsupport.SeedPRReviewSource(t, p, uuid.Nil)
	review, err := postgres.NewPRReviewStore(p).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if claim = acquire(t, q, "review"); claim == nil || claim.RunID != review.RunID {
		t.Fatal("unowned coding Run blocked review", claim)
	}
	if _, err = p.Exec(t.Context(), `UPDATE runs SET status='provisioning',worker_id='forged',lease_expires_at=now()+interval '1 minute' WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = workResources(t, planningFixture{p: p}, "forged").WithRun(t.Context(), ids[0], func(s *postgres.RunResources) error { return s.RenewLease() }); !errors.Is(err, postgres.ErrWorkRevoked) {
		t.Fatal("missing ownership bypassed resource fence", err)
	}
}

func TestDelegationRetainsParentWorkSession(t *testing.T) {
	f := delegationAdmissionFixture(t)
	planning := planningFixture{p: f.pool}
	parent := nativeWorkView(t, planning, f.id)
	record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	parked := nativeWorkView(t, planning, f.id)
	child := nativeWorkView(t, planning, *record.ChildRunID)
	var parentTask uuid.UUID
	if err = f.pool.QueryRow(t.Context(), `SELECT parent_task_id FROM tasks WHERE id=$1`, *record.ChildTaskID).Scan(&parentTask); err != nil || parentTask != parent.TaskID {
		t.Fatal(parentTask, err)
	}
	if parked.ID != parent.ID || parked.ContactState != "waiting_for_child" || parked.ReleasedAt != nil || child.ID == parent.ID || !reflect.DeepEqual(parked.LastContactAt, parent.LastContactAt) {
		t.Fatal(parked, child)
	}
	q := postgres.NewQueue(f.pool)
	acquire(t, q, "child")
	childResources := workResources(t, planning, "child")
	if err = childResources.WithRun(t.Context(), *record.ChildRunID, func(s *postgres.RunResources) error {
		if err := s.RecordFailure("Child complete with failure", nil); err != nil {
			return err
		}
		return s.ReleaseClaim()
	}); err != nil {
		t.Fatal(err)
	}
	resume := acquire(t, q, "continuation")
	if resume == nil || !resume.Continuation || resume.RunID != f.id {
		t.Fatal(resume)
	}
	continued := nativeWorkView(t, planning, f.id)
	if continued.ID != parent.ID || !reflect.DeepEqual(continued.StartedAt, parent.StartedAt) || continued.ReleasedAt != nil {
		t.Fatal("continuation replaced session", continued)
	}
}

func TestNativeTaskReleaseRacesWorker(t *testing.T) {
	for _, action := range []string{"heartbeat", "delegation"} {
		t.Run(action, func(t *testing.T) {
			f := delegationAdmissionFixture(t)
			planning := planningFixture{p: f.pool}
			before := nativeWorkView(t, planning, f.id)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			start := make(chan struct{})
			workerDone := make(chan error, 1)
			go func() {
				<-start
				var err error
				if action == "heartbeat" {
					_, err = f.store.Heartbeat(ctx, f.id)
				} else {
					_, err = f.store.SuspendForDelegation(ctx, f.id, f.request, f.checkpoint)
				}
				workerDone <- err
			}()
			close(start)
			result, err := workTx(t, f.pool, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
				return postgres.ReleaseTaskWork(ctx, tx, postgres.WorkAdminInput{TaskID: before.TaskID, ExpectedSessionID: before.ID, OperationID: uuid.New(), Reason: "Stop work"}, time.Now().UTC())
			})
			if err != nil || !result.PendingCleanup || result.Session.ReleasedAt != nil || result.Session.Outcome == nil || *result.Session.Outcome != "cancelled" {
				t.Fatal(result, err)
			}
			err = <-workerDone
			if action == "heartbeat" && err != nil {
				t.Fatal(err)
			}
			if action == "delegation" && err != nil && !errors.Is(err, postgres.ErrResourceState) && !errors.Is(err, postgres.ErrLeaseLost) {
				t.Fatal(err)
			}
			var children, released int
			if err = f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE s.outcome='cancelled' AND s.released_at IS NOT NULL) FROM runs r JOIN work_sessions s ON s.run_id=r.id WHERE r.parent_run_id=$1`, f.id).Scan(&children, &released); err != nil || children != released {
				t.Fatal("queued child retained an unnecessary claim", children, released, err)
			}
		})
	}
}

func TestDelegationCancellationRetainsBothCleanupClaims(t *testing.T) {
	f, child := parkedDelegation(t)
	planning := planningFixture{p: f.pool}
	if claim := acquire(t, postgres.NewQueue(f.pool), "child"); claim == nil || claim.RunID != child {
		t.Fatal(claim)
	}
	startWork(t, workResources(t, planning, "child"), child)
	parent := nativeWorkView(t, planning, f.id)
	result, err := releaseWork(t, planning, postgres.WorkAdminInput{TaskID: parent.TaskID, ExpectedSessionID: parent.ID, OperationID: uuid.New(), Reason: "Cancel delegated work"})
	if err != nil || !result.PendingCleanup {
		t.Fatal(result, err)
	}
	for _, id := range []uuid.UUID{f.id, child} {
		view := nativeWorkView(t, planning, id)
		if view.ReleasedAt != nil || view.Outcome == nil || *view.Outcome != "cancelled" {
			t.Fatal("cancellation bypassed resource cleanup", view)
		}
	}
}
