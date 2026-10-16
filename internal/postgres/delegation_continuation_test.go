package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/worker"
)

func parkedDelegation(t *testing.T) (admissionFixture, uuid.UUID) {
	t.Helper()
	f := delegationAdmissionFixture(t)
	record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return f, *record.ChildRunID
}

func TestDelegationContinuationExactlyOnce(t *testing.T) {
	f, child := parkedDelegation(t)
	var before time.Time
	var attempt int
	if err := f.pool.QueryRow(t.Context(), `SELECT started_at,attempt FROM runs WHERE id=$1`, f.id).Scan(&before, &attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded',worker_id='child-owner',lease_expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO workspaces(id,run_id,worktree_path,status) VALUES($1,$2,$3,'ready')`, postgres.WorkspaceID(child), child, filepath.Join(f.config.Git.WorktreeRoot, child.String())); err != nil {
		t.Fatal(err)
	}
	q := postgres.NewQueue(f.pool)
	if claim, err := q.Acquire(t.Context(), "parent-worker"); err != nil || claim != nil {
		t.Fatal("resumed before cleanup", claim, err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET worker_id=NULL,lease_expires_at=NULL WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if claim, err := q.Acquire(t.Context(), "parent-worker"); err != nil || claim != nil {
		t.Fatal("resumed with live workspace", claim, err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE workspaces SET status='released' WHERE run_id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET instructions='Changed live instructions',backend_config='{"changed":true}' WHERE id=$1`, f.parent); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		claim *worker.Claim
		err   error
		owner string
	}
	answers := make(chan answer, 2)
	for _, owner := range []string{"parent-worker", "other-worker"} {
		go func() { c, e := q.Acquire(t.Context(), owner); answers <- answer{c, e, owner} }()
	}
	var got answer
	for range 2 {
		a := <-answers
		if a.err != nil {
			t.Fatal(a.err)
		}
		if a.claim != nil {
			if got.claim != nil {
				t.Fatal("duplicate continuation")
			}
			got = a
		}
	}
	if got.claim == nil || got.claim.RunID != f.id || got.claim.Invocation != 2 || !got.claim.Continuation || got.claim.Recovery {
		t.Fatal(got)
	}
	store, _ := postgres.NewResources(f.pool, got.owner)
	store, _ = store.ForInvocation(2)
	if err := store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
		input, checkpoint, result, err := r.ContinuationContext()
		if err != nil {
			return err
		}
		if input.Invocation != 2 || input.Instructions == "Changed live instructions" || len(input.DelegationTargets) != 0 || input.DelegationContextSHA256 == "" || checkpoint.SourceCommit != f.checkpoint.SourceCommit || result.ChildRunID != child || result.Status != "succeeded" {
			t.Fatal(input, checkpoint, result)
		}
		if err = r.ResumeWorkspace(); err != nil {
			return err
		}
		if _, err = r.RecordContainer("second-container"); err != nil {
			return err
		}
		return r.MarkContinued()
	}); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	var newAttempt, starts, continued int
	if err := f.pool.QueryRow(t.Context(), `SELECT started_at,attempt,(SELECT count(*) FROM events WHERE run_id=$1 AND type='run.started'),(SELECT count(*) FROM events WHERE run_id=$1 AND type='run.continued') FROM runs WHERE id=$1`, f.id).Scan(&after, &newAttempt, &starts, &continued); err != nil || !before.Equal(after) || attempt != newAttempt || starts != 1 || continued != 1 {
		t.Fatal(after, newAttempt, starts, continued, err)
	}
}

func TestDelegationContinuationFailureResult(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f, child := parkedDelegation(t)
			if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status=$2,error='Child stopped' WHERE id=$1`, child, status); err != nil {
				t.Fatal(err)
			}
			claim := acquire(t, postgres.NewQueue(f.pool), "resume-owner")
			if claim.RunID != f.id || claim.Invocation != 2 || !claim.Continuation {
				t.Fatal(claim)
			}
			store, _ := postgres.NewResources(f.pool, "resume-owner")
			store, _ = store.ForInvocation(2)
			if err := store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
				_, _, result, err := r.ContinuationContext()
				if err == nil && (string(result.Status) != status || result.Error != "Child stopped" || result.Report != nil || result.Patch != nil) {
					t.Fatal(result)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDelegationContinuationFencesOldInvocation(t *testing.T) {
	f, child := parkedDelegation(t)
	old, err := f.store.ForInvocation(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ForInvocation(0); err == nil {
		t.Fatal("zero invocation accepted")
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE runs SET status='failed' WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	acquire(t, postgres.NewQueue(f.pool), "delegation-owner")
	if _, err = old.Heartbeat(t.Context(), f.id); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatal("stale heartbeat", err)
	}
	if err = old.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error { return r.RecordFailure("stale invocation", nil) }); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatal("accepted stale output", err)
	}
}

func TestDelegationUsageIdentityAndReplay(t *testing.T) {
	f := delegationAdmissionFixture(t)
	data := map[string]any{"input_tokens": 10, "output_tokens": 20, "invocation": 99}
	raw := map[string]any{"data": data}
	for range 2 {
		if err := f.store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
			return r.AppendBackendEvent("fixture", "usage.updated", data, raw)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var stored, original []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE run_id=$1 AND type='usage.updated'`, f.id).Scan(&count); err != nil || count != 1 {
		t.Fatal("usage replay duplicated", count, err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT data,raw FROM events WHERE run_id=$1 AND type='usage.updated'`, f.id).Scan(&stored, &original); err != nil {
		t.Fatal(err)
	}
	var got struct{ Invocation int }
	if err := json.Unmarshal(stored, &got); err != nil || got.Invocation != 1 {
		t.Fatal(string(stored), err)
	}
	if !bytes.Contains(original, []byte(`"invocation":99`)) && !bytes.Contains(original, []byte(`"invocation": 99`)) {
		t.Fatal("changed backend raw data", string(original))
	}
	if data["invocation"] != 99 {
		t.Fatal("mutated caller data")
	}
}
