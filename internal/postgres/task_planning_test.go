package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
	"reflect"
	"sync"
	"testing"
	"time"
)

type planningFixture struct {
	p                    *pgxpool.Pool
	project, agent, repo uuid.UUID
}

func planningSeed(t *testing.T) planningFixture {
	t.Helper()
	f := planningFixture{database(t), uuid.New(), uuid.New(), uuid.New()}
	for _, step := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO projects(id,name) VALUES($1,'Planning')`, []any{f.project}},
		{`INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Engineer','fake','','{}',true)`, []any{f.agent, f.project}},
		{`INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Repository','https://example.invalid/r','main','{}')`, []any{f.repo, f.project}},
	} {
		if _, e := f.p.Exec(t.Context(), step.q, step.args...); e != nil {
			t.Fatal(e)
		}
	}
	return f
}
func (f planningFixture) task(t *testing.T, title, status, refs string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, e := f.p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,$4,'original',$5,$6,'{"mode":"direct_completion","requirements":[]}')`, id, f.project, f.repo, title, status, refs)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func (f planningFixture) versions(t *testing.T, id uuid.UUID) (int64, int64) {
	t.Helper()
	var r, b int64
	e := f.p.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, id).Scan(&r, &b)
	if e != nil {
		t.Fatal(e)
	}
	return r, b
}
func (f planningFixture) patch(t *testing.T, id uuid.UUID, rev int64, patch postgres.TaskPlanningPatch) (postgres.TaskPlanningResult, error) {
	t.Helper()
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	out, e := postgres.UpdateTaskPlanning(t.Context(), tx, id, rev, patch)
	if e == nil {
		e = tx.Commit(t.Context())
	}
	return out, e
}
func (f planningFixture) move(t *testing.T, id uuid.UUID, status, placement string, anchor *uuid.UUID) (postgres.TaskPlanningResult, error) {
	t.Helper()
	r, b := f.versions(t, id)
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	out, e := postgres.MoveTask(t.Context(), tx, id, postgres.TaskMoveInput{ExpectedRevision: r, ExpectedBoardRevision: b, Status: status, Placement: placement, AnchorID: anchor})
	if e == nil {
		e = tx.Commit(t.Context())
	}
	return out, e
}
func (f planningFixture) order(t *testing.T, status string, want []string) {
	t.Helper()
	rows, e := f.p.Query(t.Context(), `SELECT title,board_position FROM tasks WHERE project_id=$1 AND status=$2 ORDER BY board_position,id`, f.project, status)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var title string
		var pos int
		if e = rows.Scan(&title, &pos); e != nil {
			t.Fatal(e)
		}
		if pos != len(got) {
			t.Fatal("non-dense positions", pos, len(got))
		}
		got = append(got, title)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
}
func strptr(s string) *string { return &s }

func TestTaskPlanningWorkOwnershipLocks(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "External", "ready", "{}")
	if _, err := claimWork(t, f, externalClaim(id)); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []postgres.TaskPlanningPatch{{Title: strptr("Edit")}, {Description: strptr("Edit")}, {RepositorySet: true}} {
		if _, err := f.patch(t, id, 2, patch); !errors.Is(err, postgres.ErrTaskPlanningConflict) {
			t.Fatal("current external owner did not protect inputs", err)
		}
	}
	caps, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.TaskEditCapabilities, error) {
		return postgres.TaskPlanningCapabilities(t.Context(), tx, id)
	})
	if err != nil || caps.ContentEditable || caps.RepositoryEditable || caps.DependenciesEditable ||
		caps.ContentLockReason == nil || *caps.ContentLockReason != "active_work" ||
		caps.RepositoryLockReason == nil || *caps.RepositoryLockReason != "has_work_sessions" {
		t.Fatal(caps, err)
	}
	if _, err = f.patch(t, id, 2, postgres.TaskPlanningPatch{Priority: strptr("urgent"), PreferredAgentSet: true, PreferredAgentID: &f.agent}); err != nil {
		t.Fatal("ownership blocked independent planning", err)
	}
}

func TestTaskPlanningCreationAndReplay(t *testing.T) {
	f := planningSeed(t)
	a := f.task(t, "Imported", "open", `{"linear":{"issue_id":"one"}}`)
	beforeR, beforeB := f.versions(t, a)
	_, e := f.p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Replacement','replacement','open','{"linear":{"issue_id":"one"}}') ON CONFLICT(project_id,(external_refs->'linear'->>'issue_id')) WHERE external_refs->'linear'->>'issue_id' IS NOT NULL DO UPDATE SET external_refs=tasks.external_refs`, uuid.New(), f.project)
	if e != nil {
		t.Fatal(e)
	}
	r, b := f.versions(t, a)
	if r != beforeR || b != beforeB {
		t.Fatal("replay advanced revisions", r, b)
	}
	f.order(t, "open", []string{"Imported"})
	errors := make(chan error, 12)
	for i := range 12 {
		go func() {
			_, e := f.p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,$3,'','open','{}')`, uuid.New(), f.project, fmt.Sprint(i))
			errors <- e
		}()
	}
	for range 12 {
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
	}
	var n, distinct int
	var min, max int64
	e = f.p.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT board_position),min(board_position),max(board_position) FROM tasks WHERE project_id=$1`, f.project).Scan(&n, &distinct, &min, &max)
	if e != nil || n != 13 || distinct != 13 || min != 0 || max != 12 {
		t.Fatal(n, distinct, min, max, e)
	}
	_, b = f.versions(t, a)
	if b != beforeB+12 {
		t.Fatal("inserts lost board revision", b)
	}
}

func TestTaskPlanningPatchPresenceAndScope(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "A", "open", "{}")
	r, b := f.versions(t, id)
	out, e := f.patch(t, id, r, postgres.TaskPlanningPatch{Priority: strptr("high"), PreferredAgentSet: true, PreferredAgentID: &f.agent})
	if e != nil || out.Revision != r+1 || out.BoardRevision != b+1 || !out.Changed {
		t.Fatal(out, e)
	}
	out, e = f.patch(t, id, out.Revision, postgres.TaskPlanningPatch{})
	if e != nil || out.Changed {
		t.Fatal(out, e)
	}
	if _, e = f.p.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, f.agent); e != nil {
		t.Fatal(e)
	}
	out, e = f.patch(t, id, out.Revision, postgres.TaskPlanningPatch{PreferredAgentSet: true, PreferredAgentID: &f.agent})
	if e != nil || out.Changed {
		t.Fatal("existing disabled preference", out, e)
	}
	out, e = f.patch(t, id, out.Revision, postgres.TaskPlanningPatch{RepositorySet: true, PreferredAgentSet: true})
	if e != nil {
		t.Fatal(e)
	}
	var repo, agent *uuid.UUID
	var title, desc string
	e = f.p.QueryRow(t.Context(), `SELECT repository_id,preferred_agent_id,title,description FROM tasks WHERE id=$1`, id).Scan(&repo, &agent, &title, &desc)
	if e != nil || repo != nil || agent != nil || title != "A" || desc != "original" {
		t.Fatal(repo, agent, title, desc, e)
	}
	if _, e = f.patch(t, id, out.Revision, postgres.TaskPlanningPatch{PreferredAgentSet: true, PreferredAgentID: &f.agent}); !errors.Is(e, postgres.ErrTaskPlanningInput) {
		t.Fatal("new disabled choice", e)
	}
	other := planningSeed(t) // different database: ensure unknown IDs do not pass membership validation
	for _, patch := range []postgres.TaskPlanningPatch{{PreferredAgentSet: true, PreferredAgentID: &other.agent}, {RepositorySet: true, RepositoryID: &other.repo}, {Title: strptr(" ")}, {Priority: strptr("invalid")}} {
		if _, e = f.patch(t, id, out.Revision, patch); e == nil {
			t.Fatal("accepted invalid patch", patch)
		}
	}
	if _, e = f.patch(t, id, r, postgres.TaskPlanningPatch{Title: strptr("stale")}); !errors.Is(e, postgres.ErrTaskPlanningConflict) {
		t.Fatal(e)
	}
}

func TestTaskPlanningContentLocks(t *testing.T) {
	f := planningSeed(t)
	for _, refs := range []string{`{"linear":{"issue_id":"managed"}}`, `{"circular":{"kind":"repository-discovery"}}`} {
		id := f.task(t, "Managed", "open", refs)
		for _, patch := range []postgres.TaskPlanningPatch{{Title: strptr("edit")}, {Description: strptr("edit")}, {RepositorySet: true}} {
			if _, e := f.patch(t, id, 1, patch); !errors.Is(e, postgres.ErrTaskPlanningConflict) {
				t.Fatal("managed content edited", e)
			}
		}
		if _, e := f.patch(t, id, 1, postgres.TaskPlanningPatch{Priority: strptr("urgent")}); e != nil {
			t.Fatal(e)
		}
	}
	for _, status := range []string{"queued", "provisioning", "running", "waiting_for_approval", "waiting_for_input", "finalizing", "succeeded", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			id := f.task(t, status, "open", `{"arbitrary":true}`)
			_, e := f.p.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake',$4,1,'{}')`, uuid.New(), id, f.agent, status)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.patch(t, id, 1, postgres.TaskPlanningPatch{RepositorySet: true}); !errors.Is(e, postgres.ErrTaskPlanningConflict) {
				t.Fatal("repository changed after run", e)
			}
			_, e = f.patch(t, id, 1, postgres.TaskPlanningPatch{Description: strptr("edit")})
			terminal := status == "succeeded" || status == "failed" || status == "cancelled"
			if terminal && e != nil || !terminal && !errors.Is(e, postgres.ErrTaskPlanningConflict) {
				t.Fatal("content lock", e)
			}
			r, _ := f.versions(t, id)
			if _, e = f.patch(t, id, r, postgres.TaskPlanningPatch{Priority: strptr("low")}); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestTaskPlanningExternalRequestCapabilities(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "External", "open", "{}")
	run, identity, request := uuid.New(), uuid.New(), uuid.New()
	for _, step := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','succeeded',1,'{}')`, []any{run, id, f.agent}},
		{`INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'linear','test','test','test','test','test','available')`, []any{identity}},
		{`INSERT INTO external_requests(id,identity_id,session_id,input_fingerprint) VALUES($1,$2,$3,'fixture')`, []any{request, identity, uuid.New()}},
		{`INSERT INTO external_session_runs(run_id,request_id) VALUES($1,$2)`, []any{run, request}},
	} {
		if _, e := f.p.Exec(t.Context(), step.q, step.args...); e != nil {
			t.Fatal(e)
		}
	}
	tx, e := f.p.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	caps, e := postgres.TaskPlanningCapabilities(t.Context(), tx, id)
	if e != nil || caps.ContentEditable || caps.RepositoryEditable || caps.ContentLockReason == nil || *caps.ContentLockReason != "source_managed" {
		t.Fatal(caps, e)
	}
	if _, e = f.patch(t, id, 1, postgres.TaskPlanningPatch{Description: strptr("overwrite external input")}); !errors.Is(e, postgres.ErrTaskPlanningConflict) {
		t.Fatal(e)
	}
}

func TestTaskPlanningPreferenceForeignKeyAndProjectCascade(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "A", "open", "{}")
	foreignProject, foreignAgent := uuid.New(), uuid.New()
	if _, e := f.p.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Other')`, foreignProject); e != nil {
		t.Fatal(e)
	}
	if _, e := f.p.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Other','fake','','{}',true)`, foreignAgent, foreignProject); e != nil {
		t.Fatal(e)
	}
	if _, e := f.patch(t, id, 1, postgres.TaskPlanningPatch{PreferredAgentSet: true, PreferredAgentID: &foreignAgent}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("foreign preference exposed", e)
	}
	if _, e := f.p.Exec(t.Context(), `UPDATE tasks SET preferred_agent_id=$2 WHERE id=$1`, id, foreignAgent); e == nil {
		t.Fatal("DB accepted cross-Project preference")
	}
	if _, e := f.patch(t, id, 1, postgres.TaskPlanningPatch{PreferredAgentSet: true, PreferredAgentID: &f.agent}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.p.Exec(t.Context(), `DELETE FROM projects WHERE id=$1`, f.project); e != nil {
		t.Fatal("preference blocked Project cascade", e)
	}
}

func TestTaskPlanningCanonicalMoves(t *testing.T) {
	f := planningSeed(t)
	a := f.task(t, "A", "open", "{}")
	f.task(t, "H", "open", "{}")
	b := f.task(t, "B", "open", "{}")
	x := f.task(t, "X", "ready", "{}")
	if _, e := f.move(t, x, "open", "before", &b); e != nil {
		t.Fatal(e)
	}
	f.order(t, "open", []string{"A", "H", "X", "B"})
	f.order(t, "ready", []string{})
	for _, step := range []struct {
		status, placement string
		anchor            *uuid.UUID
		want              []string
	}{{"open", "up", nil, []string{"A", "X", "H", "B"}}, {"open", "down", nil, []string{"A", "H", "X", "B"}}, {"open", "start", nil, []string{"X", "A", "H", "B"}}, {"open", "end", nil, []string{"A", "H", "B", "X"}}, {"open", "after", &a, []string{"A", "X", "H", "B"}}, {"cancelled", "end", nil, []string{"X"}}, {"open", "end", nil, []string{"A", "H", "B", "X"}}} {
		if _, e := f.move(t, x, step.status, step.placement, step.anchor); e != nil {
			t.Fatal(e)
		}
		f.order(t, step.status, step.want)
	}
}

func TestTaskPlanningMoveConflictAndNoop(t *testing.T) {
	f := planningSeed(t)
	a := f.task(t, "A", "open", "{}")
	b := f.task(t, "B", "open", "{}")
	other := f.task(t, "Other", "ready", "{}")
	r, board := f.versions(t, a)
	var before time.Time
	if e := f.p.QueryRow(t.Context(), `SELECT updated_at FROM tasks WHERE id=$1`, b).Scan(&before); e != nil {
		t.Fatal(e)
	}
	out, e := f.move(t, a, "open", "start", nil)
	if e != nil || out.Changed || out.Revision != r || out.BoardRevision != board {
		t.Fatal(out, e)
	}
	out, e = f.move(t, a, "open", "end", nil)
	if e != nil || !out.Changed || out.Revision != r || out.BoardRevision != board+1 {
		t.Fatal(out, e)
	}
	var after time.Time
	var neighborRev int64
	if e = f.p.QueryRow(t.Context(), `SELECT updated_at,revision FROM tasks WHERE id=$1`, b).Scan(&after, &neighborRev); e != nil || !before.Equal(after) || neighborRev != 1 {
		t.Fatal("neighbor mutated", e)
	}
	for _, anchor := range []*uuid.UUID{&a, &other} {
		if _, e = f.move(t, a, "open", "before", anchor); !errors.Is(e, postgres.ErrTaskPlanningInput) {
			t.Fatal("bad anchor", e)
		}
	}
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	if _, e = postgres.MoveTask(t.Context(), tx, a, postgres.TaskMoveInput{ExpectedRevision: r, ExpectedBoardRevision: board, Status: "completed", Placement: "end"}); !errors.Is(e, postgres.ErrTaskPlanningConflict) {
		t.Fatal(e)
	}
}

func TestTaskPlanningLaunchRace(t *testing.T) {
	for _, launchFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(launchFirst), func(t *testing.T) {
			f := planningSeed(t)
			id := f.task(t, "A", "open", "{}")
			first, e := f.p.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer first.Rollback(t.Context())
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			launch := func(tx pgx.Tx) error {
				_, e := postgres.CreateRun(ctx, tx, postgres.RunLaunchInput{TaskID: id, AgentID: f.agent})
				return e
			}
			edit := func(tx pgx.Tx) error {
				_, e := postgres.UpdateTaskPlanning(ctx, tx, id, 1, postgres.TaskPlanningPatch{Title: strptr("new inputs")})
				return e
			}
			before, after := edit, launch
			if launchFirst {
				before, after = launch, edit
			}
			if e = before(first); e != nil {
				t.Fatal(e)
			}
			entered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				tx, e := f.p.Begin(ctx)
				if e != nil {
					done <- e
					return
				}
				defer tx.Rollback(ctx)
				close(entered)
				e = after(tx)
				if e == nil {
					e = tx.Commit(ctx)
				}
				done <- e
			}()
			<-entered
			// The second transaction must remain blocked until the first releases its Task lock.
			select {
			case e := <-done:
				t.Fatalf("raced past lock: %v", e)
			case <-time.After(30 * time.Millisecond):
			}
			if e = first.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if launchFirst && !errors.Is(e, postgres.ErrTaskPlanningConflict) || !launchFirst && e != nil {
				t.Fatal(e)
			}
			var title string
			if e = f.p.QueryRow(ctx, `SELECT title FROM tasks WHERE id=$1`, id).Scan(&title); e != nil {
				t.Fatal(e)
			}
			want := "new inputs"
			if launchFirst {
				want = "A"
			}
			if title != want {
				t.Fatal(title, want)
			}
		})
	}
}

func TestTaskPlanningConcurrentIntents(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "A", "open", "{}")
	r, b := f.versions(t, id)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, status := range []string{"completed", "cancelled"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, e := f.p.Begin(t.Context())
			if e != nil {
				results <- e
				return
			}
			defer tx.Rollback(t.Context())
			_, e = postgres.MoveTask(t.Context(), tx, id, postgres.TaskMoveInput{ExpectedRevision: r, ExpectedBoardRevision: b, Status: status, Placement: "end"})
			if e == nil {
				e = tx.Commit(t.Context())
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else if errors.Is(e, postgres.ErrTaskPlanningConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	r, b = f.versions(t, id)
	tx, e := f.p.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = postgres.MoveTask(t.Context(), tx, id, postgres.TaskMoveInput{ExpectedRevision: r, ExpectedBoardRevision: b, Status: "ready", Placement: "end"}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	r2, b2 := f.versions(t, id)
	if r != r2 || b != b2 {
		t.Fatal("rollback leaked", r2, b2)
	}
	var count int
	if e = f.p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs)+(SELECT count(*) FROM events)`).Scan(&count); e != nil || count != 0 {
		t.Fatal("planning executed work", count, e)
	}
}
