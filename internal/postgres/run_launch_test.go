package postgres_test

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"testing"
)

func nativeLaunch(t *testing.T, f planningFixture, task uuid.UUID, key string) (postgres.RunLaunchResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.RunLaunchResult, error) {
		return postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: task, AgentID: f.agent, RequestKey: key})
	})
}
func TestNativeAndExternalClaimsShareOneOwner(t *testing.T) {
	f := planningSeed(t)
	task := f.task(t, "Shared", "ready", "{}")
	start, done := make(chan struct{}), make(chan error, 2)
	go func() { <-start; _, err := nativeLaunch(t, f, task, "native"); done <- err }()
	go func() { <-start; _, err := claimWork(t, f, externalClaim(task)); done <- err }()
	close(start)
	accepted := 0
	for range 2 {
		err := <-done
		var conflict *postgres.WorkOwnershipConflict
		if err == nil {
			accepted++
		} else if !errors.As(err, &conflict) {
			t.Fatal(err)
		}
	}
	var sessions, orphanRuns int
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_sessions WHERE released_at IS NULL),
		(SELECT count(*) FROM runs r WHERE NOT EXISTS(SELECT 1 FROM work_sessions s WHERE s.run_id=r.id))`).Scan(&sessions, &orphanRuns); err != nil || accepted != 1 || sessions != 1 || orphanRuns != 0 {
		t.Fatal(accepted, sessions, orphanRuns, err)
	}
}

func TestNativeLaunchReplayPrecedesEligibility(t *testing.T) {
	f := planningSeed(t)
	task := f.task(t, "Native", "open", "{}")
	r, b := f.versions(t, task)
	first, err := nativeLaunch(t, f, task, "first")
	if err != nil {
		t.Fatal(err)
	}
	afterR, afterB := f.versions(t, task)
	if afterR != r+1 || afterB != b+1 {
		t.Fatal("claim did not update planning", afterR, afterB)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err = f.move(t, task, "completed", "end", nil); err != nil {
		t.Fatal(err)
	}
	retry, err := nativeLaunch(t, f, task, "first")
	if err != nil || retry.RunID != first.RunID || retry.Created {
		t.Fatal(retry, err)
	}
	var conflict *postgres.WorkOwnershipConflict
	if _, err = nativeLaunch(t, f, task, "new"); !errors.As(err, &conflict) {
		t.Fatal("new key bypassed owner", err)
	}
	// Seed completed, cleaned history; execution cleanup itself is tested separately.
	if _, err = f.p.Exec(t.Context(), `UPDATE runs SET status='succeeded',finished_at=now() WHERE id=$1`, first.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE work_sessions SET outcome='succeeded',finished_at=now(),released_at=now(),release_reason='completed fixture' WHERE run_id=$1`, first.RunID); err != nil {
		t.Fatal(err)
	}
	retry, err = nativeLaunch(t, f, task, "first")
	if err != nil || retry.RunID != first.RunID || retry.Created {
		t.Fatal("released replay forked", retry, err)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE agents SET enabled=true WHERE id=$1`, f.agent); err != nil {
		t.Fatal(err)
	}
	var invalid *postgres.WorkIneligibleError
	if _, err = nativeLaunch(t, f, task, "new"); !errors.As(err, &invalid) {
		t.Fatal("closed work reopened", err)
	}
	for _, mutation := range []string{`description=''`, `description='   '`, `repository_id=NULL`} {
		id := f.task(t, "Incomplete", "open", "{}")
		if _, err = f.p.Exec(t.Context(), `UPDATE tasks SET `+mutation+` WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err = nativeLaunch(t, f, id, "first"); !errors.As(err, &invalid) {
			t.Fatal(mutation, err)
		}
	}
	dependent := f.task(t, "Blocked", "open", "{}")
	blocker := f.task(t, "Blocker", "cancelled", "{}")
	if _, err = f.patch(t, dependent, 1, postgres.TaskPlanningPatch{PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{blocker}}); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeLaunch(t, f, dependent, "first"); !errors.As(err, &invalid) {
		t.Fatal("blocked native work admitted", err)
	}
}

func TestRunLaunchKeyReplayBeforeMutableAgentChecks(t *testing.T) {
	f := planningSeed(t)
	p, task, agent := f.p, f.task(t, "Replay", "open", "{}"), f.agent
	launch := func(key string, refs json.RawMessage) (postgres.RunLaunchResult, error) {
		tx, e := p.Begin(t.Context())
		if e != nil {
			return postgres.RunLaunchResult{}, e
		}
		defer tx.Rollback(t.Context())
		r, e := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: task, AgentID: agent, RequestKey: key, ExternalRefs: refs})
		if e == nil {
			e = tx.Commit(t.Context())
		}
		return r, e
	}
	first, e := launch("one", json.RawMessage(`{"from":"test"}`))
	if e != nil || !first.Created {
		t.Fatal(first, e)
	}
	if _, e = p.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, agent); e != nil {
		t.Fatal(e)
	}
	again, e := launch("one", json.RawMessage(`{"from":"test"}`))
	if e != nil || again.Created || first.RunID != again.RunID {
		t.Fatal(again, e)
	}
	if _, e = launch("one", json.RawMessage(`{"from":"changed"}`)); !errors.Is(e, postgres.ErrRunLaunchConflict) {
		t.Fatal(e)
	}
	var conflict *postgres.WorkOwnershipConflict
	if _, e = launch("new", json.RawMessage(`{}`)); !errors.As(e, &conflict) {
		t.Fatal(e)
	}
}
