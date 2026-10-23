package postgres_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
)

func finishWork(t *testing.T, f planningFixture, in postgres.ExternalWorkClaim, op uuid.UUID, outcome runstate.Status, now time.Time) (postgres.WorkChangeResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
		return postgres.FinishExternalWork(t.Context(), tx, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: in.SessionID, Secret: in.Secret}, OperationID: op, Outcome: outcome, Summary: "Tests passed", Report: &completion.Report{Result: "Tests passed"}}, now)
	})
}
func reassignWork(t *testing.T, f planningFixture, in postgres.WorkAdminInput, replacement postgres.ExternalWorkClaim) (postgres.WorkChangeResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
		return postgres.ReassignExternalWork(t.Context(), tx, in, replacement, workNow.Add(time.Minute))
	})
}
func currentWork(t *testing.T, f planningFixture, task uuid.UUID) *postgres.WorkSessionView {
	t.Helper()
	v, err := workTx(t, f.p, func(tx pgx.Tx) (*postgres.WorkSessionView, error) {
		return postgres.CurrentTaskWork(t.Context(), tx, task, workNow.Add(2*time.Minute))
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExternalWorkFinishIsIdempotent(t *testing.T) {
	f := planningSeed(t)
	for _, outcome := range []runstate.Status{"succeeded", "failed", "cancelled"} {
		in := externalClaim(f.task(t, string(outcome), "ready", "{}"))
		if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET planned_start_date='2026-10-01',planned_finish_date='2026-10-31' WHERE id=$1`, in.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err := claimWork(t, f, in); err != nil {
			t.Fatal(err)
		}
		op := uuid.New()
		first, err := finishWork(t, f, in, op, outcome, workNow.Add(-time.Minute))
		if err != nil || first.Session.Outcome == nil || *first.Session.Outcome != outcome || first.Session.ReleasedAt == nil || first.Session.FinishedAt.Before(workNow) {
			t.Fatal(first, err)
		}
		replay, err := finishWork(t, f, in, op, outcome, workNow.Add(time.Hour))
		if err != nil || !reflect.DeepEqual(first.Receipt, replay.Receipt) {
			t.Fatal("finish replay changed audit", replay, err)
		}
		if _, err = finishWork(t, f, in, op, "running", workNow); !errors.Is(err, postgres.ErrWorkInput) {
			t.Fatal(err)
		}
		if _, err = finishWork(t, f, in, uuid.New(), outcome, workNow); !errors.Is(err, postgres.ErrWorkRevoked) {
			t.Fatal("new finish after release", err)
		}
		changed := runstate.Status("failed")
		if outcome == changed {
			changed = "cancelled"
		}
		if _, err = finishWork(t, f, in, op, changed, workNow); !errors.Is(err, postgres.ErrWorkReplayConflict) {
			t.Fatal("changed finish replay", err)
		}
		_, err = workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkSessionView, error) {
			return postgres.HeartbeatWork(t.Context(), tx, postgres.WorkCredential{SessionID: in.SessionID, Secret: in.Secret}, 1, workNow)
		})
		if !errors.Is(err, postgres.ErrWorkRevoked) {
			t.Fatal("finish allowed contact", err)
		}
		var status, start, finish string
		var revision int64
		wantStatus, wantRevision := "in_progress", int64(2)
		if outcome == "succeeded" {
			wantStatus, wantRevision = "completed", 3
		}
		if err = f.p.QueryRow(t.Context(), `SELECT status,planned_start_date::text,planned_finish_date::text,revision FROM tasks WHERE id=$1`, in.TaskID).Scan(&status, &start, &finish, &revision); err != nil || status != wantStatus || start != "2026-10-01" || finish != "2026-10-31" || revision != wantRevision {
			t.Fatal(status, start, finish, revision, err)
		}
	}
}

func TestExternalWorkReassignmentFencesOldOwner(t *testing.T) {
	for _, race := range []string{"heartbeat", "finish"} {
		t.Run(race, func(t *testing.T) {
			f := planningSeed(t)
			old := externalClaim(f.task(t, "Work", "ready", "{}"))
			if _, err := claimWork(t, f, old); err != nil {
				t.Fatal(err)
			}
			replacement := externalClaim(old.TaskID)
			replacement.ExpectedRevision = 2
			replacement.ClientName = "Claude"
			admin := postgres.WorkAdminInput{TaskID: old.TaskID, ExpectedSessionID: old.SessionID, OperationID: uuid.New(), Reason: "User handed off"}
			start := make(chan struct{})
			ownerDone, adminDone := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				if race == "finish" {
					_, err := finishWork(t, f, old, uuid.New(), "succeeded", workNow.Add(time.Minute))
					ownerDone <- err
				} else {
					_, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkSessionView, error) {
						return postgres.HeartbeatWork(t.Context(), tx, postgres.WorkCredential{SessionID: old.SessionID, Secret: old.Secret}, 1, workNow.Add(time.Minute))
					})
					ownerDone <- err
				}
			}()
			go func() { <-start; _, err := reassignWork(t, f, admin, replacement); adminDone <- err }()
			close(start)
			ownerErr, adminErr := <-ownerDone, <-adminDone
			if adminErr != nil {
				if race != "finish" || !errors.Is(adminErr, postgres.ErrWorkRevoked) || ownerErr != nil || currentWork(t, f, old.TaskID) != nil {
					t.Fatal(ownerErr, adminErr)
				}
				return
			}
			if ownerErr != nil && !errors.Is(ownerErr, postgres.ErrWorkRevoked) {
				t.Fatal(ownerErr)
			}
			if current := currentWork(t, f, old.TaskID); current == nil || current.ID != replacement.SessionID {
				t.Fatal(current)
			}
			if _, err := finishWork(t, f, old, uuid.New(), "failed", workNow); !errors.Is(err, postgres.ErrWorkRevoked) {
				t.Fatal("revoked finish accepted", err)
			}
			_, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkSessionView, error) {
				return postgres.HeartbeatWork(t.Context(), tx, postgres.WorkCredential{SessionID: old.SessionID, Secret: old.Secret}, 2, workNow)
			})
			if !errors.Is(err, postgres.ErrWorkRevoked) {
				t.Fatal("revoked heartbeat accepted", err)
			}
			if _, err = workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
				admin.OperationID = uuid.New()
				return postgres.ReleaseTaskWork(t.Context(), tx, admin, workNow)
			}); !errors.Is(err, postgres.ErrWorkRevoked) {
				t.Fatal("stale admin released replacement", err)
			}
		})
	}
}

func TestWorkAdminReplayCannotReviveOwnership(t *testing.T) {
	f := planningSeed(t)
	old := externalClaim(f.task(t, "Work", "ready", "{}"))
	if _, err := claimWork(t, f, old); err != nil {
		t.Fatal(err)
	}
	admin := postgres.WorkAdminInput{TaskID: old.TaskID, ExpectedSessionID: old.SessionID, OperationID: uuid.New(), Reason: "Hand off"}
	replacement := externalClaim(old.TaskID)
	replacement.ExpectedRevision = 2
	first, err := reassignWork(t, f, admin, replacement)
	if err != nil || first.Receipt.ReplacementSessionID == nil || *first.Receipt.ReplacementSessionID != replacement.SessionID || first.Replacement == nil {
		t.Fatal(first, err)
	}
	if _, err = finishWork(t, f, replacement, uuid.New(), "succeeded", workNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	receipts := func() []postgres.WorkReceipt {
		v, err := workTx(t, f.p, func(tx pgx.Tx) ([]postgres.WorkReceipt, error) {
			return postgres.WorkSessionReceipts(t.Context(), tx, old.SessionID)
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := receipts()
	replay, err := reassignWork(t, f, admin, replacement)
	if err != nil || !reflect.DeepEqual(first.Receipt, replay.Receipt) || currentWork(t, f, old.TaskID) != nil || !reflect.DeepEqual(before, receipts()) || replay.Replacement.ReleasedAt == nil {
		t.Fatal("old admin replay changed ownership", replay, err)
	}
	changed := replacement
	changed.Secret = externalClaim(old.TaskID).Secret
	if _, err = reassignWork(t, f, admin, changed); !errors.Is(err, postgres.ErrWorkReplayConflict) {
		t.Fatal("changed replacement secret accepted", err)
	}
	admin.Reason = "Changed reason"
	if _, err = reassignWork(t, f, admin, replacement); !errors.Is(err, postgres.ErrWorkReplayConflict) {
		t.Fatal(err)
	}

	t.Run("invalid replacements leave owner", func(t *testing.T) {
		owner := externalClaim(f.task(t, "Validate", "ready", "{}"))
		if _, err := claimWork(t, f, owner); err != nil {
			t.Fatal(err)
		}
		input := postgres.WorkAdminInput{TaskID: owner.TaskID, ExpectedSessionID: owner.SessionID, OperationID: uuid.New(), Reason: "Replace"}
		next := externalClaim(owner.TaskID)
		next.ExpectedRevision = 2
		for _, change := range []func(*postgres.WorkAdminInput, *postgres.ExternalWorkClaim){
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { a.Reason = " " },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { a.Reason = strings.Repeat("x", 4001) },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { a.ExpectedSessionID = uuid.New() },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { a.OperationID = uuid.Nil },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { c.TaskID = uuid.New() },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { c.Secret = owner.Secret },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { c.SessionID = owner.SessionID },
			func(a *postgres.WorkAdminInput, c *postgres.ExternalWorkClaim) { c.ExpectedRevision = 1 },
		} {
			a, c := input, next
			change(&a, &c)
			if _, err := reassignWork(t, f, a, c); err == nil {
				t.Fatal("invalid reassignment accepted")
			}
			if current := currentWork(t, f, owner.TaskID); current == nil || current.ID != owner.SessionID {
				t.Fatal("failed validation revoked owner", current)
			}
		}
		for _, status := range []string{"completed", "in_review", "cancelled"} {
			// Seed legacy stage drift; public stage changes with an owner are rejected.
			if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET status=$2 WHERE id=$1`, owner.TaskID, status); err != nil {
				t.Fatal(err)
			}
			next.ExpectedRevision, _ = f.versions(t, owner.TaskID)
			var invalid *postgres.WorkIneligibleError
			if _, err := reassignWork(t, f, input, next); !errors.As(err, &invalid) {
				t.Fatal("closed task handed off", err)
			}
		}
		if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET status='in_progress' WHERE id=$1`, owner.TaskID); err != nil {
			t.Fatal(err)
		}
		blocker := f.task(t, "Reopened prerequisite", "open", "{}")
		if _, err := f.p.Exec(t.Context(), `INSERT INTO task_dependencies(project_id,task_id,prerequisite_task_id) VALUES($1,$2,$3)`, f.project, owner.TaskID, blocker); err != nil {
			t.Fatal(err)
		}
		next.ExpectedRevision, _ = f.versions(t, owner.TaskID)
		var invalid *postgres.WorkIneligibleError
		if _, err := reassignWork(t, f, input, next); !errors.As(err, &invalid) {
			t.Fatal("blocked handoff", err)
		}
		// A user can still release blocked work without creating a replacement.
		released, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
			return postgres.ReleaseTaskWork(t.Context(), tx, input, workNow)
		})
		if err != nil || released.Session.ReleasedAt == nil || currentWork(t, f, owner.TaskID) != nil {
			t.Fatal(released, err)
		}
		retry, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
			return postgres.ReleaseTaskWork(t.Context(), tx, input, workNow.Add(time.Hour))
		})
		if err != nil || !reflect.DeepEqual(released.Receipt, retry.Receipt) {
			t.Fatal(retry, err)
		}
	})
}
