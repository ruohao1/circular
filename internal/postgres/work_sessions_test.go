package postgres_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
)

var workNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func workTx[T any](t *testing.T, p *pgxpool.Pool, call func(pgx.Tx) (T, error)) (T, error) {
	t.Helper()
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	out, err := call(tx)
	if err == nil {
		err = tx.Commit(t.Context())
	}
	return out, err
}
func externalClaim(task uuid.UUID) postgres.ExternalWorkClaim {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return postgres.ExternalWorkClaim{SessionID: uuid.New(), TaskID: task, ExpectedRevision: 1, ClientName: "Codex", ClientVersion: "1", Secret: secret}
}
func claimWork(t *testing.T, f planningFixture, in postgres.ExternalWorkClaim) (postgres.WorkClaimResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkClaimResult, error) {
		return postgres.ClaimExternalWork(t.Context(), tx, in, workNow)
	})
}
func inspectWork(t *testing.T, f planningFixture, id uuid.UUID, now time.Time) postgres.WorkSessionView {
	t.Helper()
	v, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkSessionView, error) {
		return postgres.InspectWorkSession(t.Context(), tx, id, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExternalWorkClaimsSerialize(t *testing.T) {
	f := planningSeed(t)
	task := f.task(t, "Work", "ready", "{}")
	_, board := f.versions(t, task)
	type result struct {
		v   postgres.WorkClaimResult
		err error
	}
	start, done := make(chan struct{}), make(chan result, 2)
	for range 2 {
		in := externalClaim(task)
		go func() { <-start; v, err := claimWork(t, f, in); done <- result{v, err} }()
	}
	close(start)
	var winner uuid.UUID
	var conflict *postgres.WorkOwnershipConflict
	for range 2 {
		r := <-done
		if r.err == nil {
			if !r.v.Created || winner != uuid.Nil {
				t.Fatal("multiple owners")
			}
			winner = r.v.Session.ID
		} else if !errors.As(r.err, &conflict) {
			t.Fatal(r.err)
		}
	}
	if conflict == nil || conflict.Owner.ID != winner {
		t.Fatal("conflict did not identify owner")
	}
	var sessions, receipts int
	var status string
	if err := f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_sessions),(SELECT count(*) FROM work_session_receipts),status FROM tasks WHERE id=$1`, task).Scan(&sessions, &receipts, &status); err != nil {
		t.Fatal(err)
	}
	r, b := f.versions(t, task)
	if sessions != 1 || receipts != 1 || status != "in_progress" || r != 2 || b != board+1 {
		t.Fatal(sessions, receipts, status, r, b)
	}
}

func TestExternalWorkReplayAfterRestart(t *testing.T) {
	f := planningSeed(t)
	task := f.task(t, "Work", "ready", "{}")
	in := externalClaim(task)
	first, err := claimWork(t, f, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.move(t, task, "cancelled", "end", nil); err == nil {
		t.Fatal("active owner allowed manual cancellation")
	}
	if _, err = releaseWork(t, f, postgres.WorkAdminInput{TaskID: task, ExpectedSessionID: in.SessionID, OperationID: uuid.New(), Reason: "Release for cancellation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.move(t, task, "cancelled", "end", nil); err != nil {
		t.Fatal(err)
	}
	restarted, err := pgxpool.NewWithConfig(t.Context(), f.p.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	other := f
	other.p = restarted
	replay, err := claimWork(t, other, in)
	if err != nil || replay.Created || replay.Session.ID != first.Session.ID {
		t.Fatal(replay, err)
	}
	for _, change := range []func(*postgres.ExternalWorkClaim){
		func(c *postgres.ExternalWorkClaim) { c.TaskID = uuid.New() },
		func(c *postgres.ExternalWorkClaim) { c.ClientName = "Claude" },
		func(c *postgres.ExternalWorkClaim) { c.ClientVersion = "2" },
		func(c *postgres.ExternalWorkClaim) { c.ExpectedRevision++ },
	} {
		changed := in
		change(&changed)
		if _, err = claimWork(t, f, changed); !errors.Is(err, postgres.ErrWorkReplayConflict) {
			t.Fatal("changed replay accepted", err)
		}
	}
	bad := in
	bad.Secret = bytes.Repeat([]byte{0}, 32)
	if _, err = claimWork(t, f, bad); !errors.Is(err, postgres.ErrWorkCredential) {
		t.Fatal(err)
	}
	raw, err := json.Marshal(replay.Session)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(in.Secret)
	if bytes.Contains(raw, in.Secret) || bytes.Contains(raw, []byte(hex.EncodeToString(hash[:]))) || bytes.Contains(raw, []byte("digest")) || bytes.Contains(raw, []byte("secret")) {
		t.Fatal("safe view contains credential")
	}

	t.Run("same ID races across projects", func(t *testing.T) {
		secondProject, secondTask := uuid.New(), uuid.New()
		if _, err = f.p.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Other')`, secondProject); err != nil {
			t.Fatal(err)
		}
		repo := uuid.New()
		if _, err = f.p.Exec(t.Context(), `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Other','/fixture','main','{}')`, repo, secondProject); err != nil {
			t.Fatal(err)
		}
		if _, err = f.p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,'Other','Ready work','ready','{}','{"mode":"direct_completion","requirements":[]}')`, secondTask, secondProject, repo); err != nil {
			t.Fatal(err)
		}
		a := externalClaim(f.task(t, "First", "ready", "{}"))
		b := a
		b.TaskID = secondTask
		start, done := make(chan struct{}), make(chan error, 2)
		for _, input := range []postgres.ExternalWorkClaim{a, b} {
			go func() { <-start; _, e := claimWork(t, f, input); done <- e }()
		}
		close(start)
		accepted := 0
		for range 2 {
			e := <-done
			if e == nil {
				accepted++
			} else if !errors.Is(e, postgres.ErrWorkReplayConflict) {
				t.Fatal(e)
			}
		}
		var progressed int
		if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM tasks WHERE id=ANY($1::uuid[]) AND status='in_progress'`, []uuid.UUID{a.TaskID, b.TaskID}).Scan(&progressed); err != nil || accepted != 1 || progressed != 1 {
			t.Fatal(accepted, progressed, err)
		}
	})
}

func TestWorkHeartbeatDeadlineAndSequence(t *testing.T) {
	f := planningSeed(t)
	in := externalClaim(f.task(t, "Work", "ready", "{}"))
	if _, err := claimWork(t, f, in); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		seconds int
		state   string
	}{{119, "current"}, {120, "needs_attention"}} {
		v := inspectWork(t, f, in.SessionID, workNow.Add(time.Duration(c.seconds)*time.Second))
		if v.ContactState != c.state || v.ReleasedAt != nil {
			t.Fatal(v)
		}
	}
	credential := postgres.WorkCredential{SessionID: in.SessionID, Secret: in.Secret}
	beat := func(sequence int64, at time.Time) (postgres.WorkSessionView, error) {
		return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkSessionView, error) {
			return postgres.HeartbeatWork(t.Context(), tx, credential, sequence, at)
		})
	}
	first, err := beat(2, workNow.Add(150*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []int64{1, 2} {
		v, e := beat(sequence, workNow.Add(300*time.Second))
		if e != nil || !v.LastContactAt.Equal(*first.LastContactAt) || v.ContactState != "needs_attention" {
			t.Fatal(v, e)
		}
	}
	v, err := beat(3, workNow.Add(100*time.Second))
	if err != nil || !v.LastContactAt.Equal(*first.LastContactAt) || v.HeartbeatSequence != 3 {
		t.Fatal("backward clock", v, err)
	}
	v, err = beat(4, workNow.Add(310*time.Second))
	if err != nil || v.ContactState != "current" {
		t.Fatal("reconnect", v, err)
	}
	for _, seq := range []int64{0, -1, 9007199254740992} {
		if _, err := beat(seq, workNow); !errors.Is(err, postgres.ErrWorkInput) {
			t.Fatal(seq, err)
		}
	}
	credential.Secret = make([]byte, 32)
	if _, err := beat(5, workNow); !errors.Is(err, postgres.ErrWorkCredential) {
		t.Fatal("wrong credential", err)
	}
}

func TestExternalWorkEligibilityAndEditRace(t *testing.T) {
	f := planningSeed(t)
	for _, status := range []string{"open", "in_progress", "in_review", "completed", "cancelled"} {
		_, err := claimWork(t, f, externalClaim(f.task(t, status, status, "{}")))
		var ineligible *postgres.WorkIneligibleError
		if !errors.As(err, &ineligible) {
			t.Fatal(status, err)
		}
	}
	for _, mutation := range []string{`title='  '`, `description=E' \n '`, `repository_id=NULL`} {
		id := f.task(t, "Incomplete", "ready", "{}")
		if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET `+mutation+` WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		var ineligible *postgres.WorkIneligibleError
		if _, err := claimWork(t, f, externalClaim(id)); !errors.As(err, &ineligible) {
			t.Fatal(mutation, err)
		}
	}
	for _, status := range []string{"open", "cancelled"} {
		id := f.task(t, "Blocked", "ready", "{}")
		prerequisite := f.task(t, "Blocker", status, "{}")
		out, err := f.patch(t, id, 1, postgres.TaskPlanningPatch{PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{prerequisite}})
		if err != nil {
			t.Fatal(err)
		}
		in := externalClaim(id)
		in.ExpectedRevision = out.Revision
		var ineligible *postgres.WorkIneligibleError
		if _, err = claimWork(t, f, in); !errors.As(err, &ineligible) || len(ineligible.Blockers) != 1 || ineligible.Blockers[0] != prerequisite {
			t.Fatal(ineligible, err)
		}
	}
	t.Run("foreign repository", func(t *testing.T) {
		project, repo := uuid.New(), uuid.New()
		if _, err := f.p.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Foreign repo')`, project); err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.Exec(t.Context(), `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Foreign','/fixture','main','{}')`, repo, project); err != nil {
			t.Fatal(err)
		}
		id := f.task(t, "Wrong repository", "ready", "{}")
		if _, err := f.p.Exec(t.Context(), `UPDATE tasks SET repository_id=$2 WHERE id=$1`, id, repo); err != nil {
			t.Fatal(err)
		}
		var invalid *postgres.WorkIneligibleError
		if _, err := claimWork(t, f, externalClaim(id)); !errors.As(err, &invalid) {
			t.Fatal("foreign repository admitted", err)
		}
	})
	t.Run("dependency edit race", func(t *testing.T) {
		id := f.task(t, "Dependency race", "ready", "{}")
		dependency := f.task(t, "New blocker", "open", "{}")
		start, done := make(chan struct{}), make(chan error, 2)
		go func() { <-start; _, err := claimWork(t, f, externalClaim(id)); done <- err }()
		go func() {
			<-start
			_, err := f.patch(t, id, 1, postgres.TaskPlanningPatch{PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{dependency}})
			done <- err
		}()
		close(start)
		accepted := 0
		for range 2 {
			err := <-done
			if err == nil {
				accepted++
			} else if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
				t.Fatal(err)
			}
		}
		if accepted != 1 {
			t.Fatal("claim and conflicting dependency edit both succeeded")
		}
	})
	id := f.task(t, "Race", "ready", "{}")
	in := externalClaim(id)
	start, done := make(chan struct{}), make(chan error, 2)
	go func() { <-start; _, e := claimWork(t, f, in); done <- e }()
	go func() {
		<-start
		_, e := f.patch(t, id, 1, postgres.TaskPlanningPatch{Title: strptr("Edited")})
		done <- e
	}()
	close(start)
	accepted := 0
	for range 2 {
		err := <-done
		if err == nil {
			accepted++
		} else if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("claim used a stale revision", accepted)
	}
	rollbackTask := f.task(t, "Rollback", "ready", "{}")
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.ClaimExternalWork(t.Context(), tx, externalClaim(rollbackTask), workNow); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	var count int
	if err = f.p.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM work_sessions WHERE task_id=$1) FROM tasks WHERE id=$1`, rollbackTask).Scan(&status, &count); err != nil || status != "ready" || count != 0 {
		t.Fatal(status, count, err)
	}
	for _, change := range []func(*postgres.ExternalWorkClaim){
		func(c *postgres.ExternalWorkClaim) { c.ClientName = " " },
		func(c *postgres.ExternalWorkClaim) { c.ClientName = strings.Repeat("界", 81) },
		func(c *postgres.ExternalWorkClaim) { c.ClientVersion = "\x00" },
		func(c *postgres.ExternalWorkClaim) { c.ClientName = "\xff" },
		func(c *postgres.ExternalWorkClaim) { c.Secret = []byte("short") },
		func(c *postgres.ExternalWorkClaim) { c.ExpectedRevision = 0 },
		func(c *postgres.ExternalWorkClaim) { c.SessionID = uuid.Nil },
	} {
		bad := externalClaim(rollbackTask)
		change(&bad)
		if _, err = claimWork(t, f, bad); !errors.Is(err, postgres.ErrWorkInput) {
			t.Fatal("invalid claim input", err)
		}
	}
}
