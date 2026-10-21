package migrate_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/testsupport"
)

func workHistory(t *testing.T, p *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	project, task, agent, run := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects(id,name) VALUES($1,'Work history')`, []any{project}},
		{`INSERT INTO tasks(id,project_id,title,description,status,external_refs,planned_start_date,planned_finish_date) VALUES($1,$2,'Keep task','Keep requirements','completed','{}','2026-10-01','2026-10-03')`, []any{task, project}},
		{`INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Coder','fake','Keep instructions','{}',true)`, []any{agent, project}},
		{`INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs,started_at,finished_at) VALUES($1,$2,$3,'fake','succeeded',1,'{}','2026-10-01','2026-10-02')`, []any{run, task, agent}},
		{`INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) VALUES($1,$2,1,'run.succeeded','worker','{"keep":true}','2026-10-02')`, []any{uuid.New(), run}},
		{`INSERT INTO workspaces(id,run_id,worktree_path,status) VALUES($1,$2,'/historical/workspace','released')`, []any{uuid.New(), run}},
		{`INSERT INTO delegations(id,parent_run_id,target_agent_id,objective,depth,status) VALUES($1,$2,$3,'Keep objective',1,'completed')`, []any{uuid.New(), run, agent}},
	} {
		if _, err := p.Exec(t.Context(), s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	return project, task, run
}

func workHistoryJSON(t *testing.T, p *pgxpool.Pool) []byte {
	t.Helper()
	var data []byte
	// Removing the new nullable column lets us compare every pre-existing value.
	err := p.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'tasks',(SELECT jsonb_agg(to_jsonb(t)-'parent_task_id'-'completion_policy'-'completion_contract'-'completion_session_id') FROM tasks t),
		'runs',(SELECT jsonb_agg(to_jsonb(r)) FROM runs r),
		'events',(SELECT jsonb_agg(to_jsonb(e)) FROM events e),
		'workspaces',(SELECT jsonb_agg(to_jsonb(w)) FROM workspaces w),
		'delegations',(SELECT jsonb_agg(to_jsonb(d)) FROM delegations d))`).Scan(&data)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestWorkSessionMigrationPreservesHistory(t *testing.T) {
	p := legacyDatabase(t, 18)
	workHistory(t, p)
	before := workHistoryJSON(t, p)
	for range 2 {
		if err := migrate.Up(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, workHistoryJSON(t, p)) {
		t.Fatal("migration changed historical data")
	}
	var sessions, receipts int
	if err := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_sessions),(SELECT count(*) FROM work_session_receipts)`).Scan(&sessions, &receipts); err != nil || sessions != 0 || receipts != 0 {
		t.Fatal("migration invented ownership or contact", sessions, receipts, err)
	}
}

func TestWorkSessionMigrationRequiresDrainedCoding(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE runs SET status='queued',finished_at=NULL`,
		`UPDATE runs SET status='running',finished_at=NULL`,
		`UPDATE runs SET worker_id='old-worker',lease_expires_at=now()+interval '1 minute'`,
		`UPDATE workspaces SET status='ready'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			p := legacyDatabase(t, 18)
			workHistory(t, p)
			if _, err := p.Exec(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			before := workHistoryJSON(t, p)
			if err := migrate.Up(t.Context(), p); err == nil {
				t.Fatal("upgrade accepted undrained coding work")
			}
			var version string
			var exists bool
			if err := p.QueryRow(t.Context(), `SELECT version_num,to_regclass(current_schema()||'.work_sessions') IS NOT NULL FROM alembic_version`).Scan(&version, &exists); err != nil || version != "0018" || exists {
				t.Fatal("rejected migration partially applied", version, exists, err)
			}
			if !bytes.Equal(before, workHistoryJSON(t, p)) {
				t.Fatal("rejected migration changed work")
			}
		})
	}
	t.Run("active PR review is exempt", func(t *testing.T) {
		p := legacyDatabase(t, 18)
		snapshot := testsupport.SeedPRReviewSource(t, p, uuid.Nil)
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		review, run := uuid.New(), uuid.New()
		tx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		// Seed the historical schema without calling the current-schema launcher.
		if _, err = tx.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,parent_run_id,backend,status,attempt,external_refs,kind) VALUES($1,$2,$3,$4,'codex','queued',2,'{}','pr_review')`, run, snapshot.TaskID, snapshot.Reviewer.AgentID, snapshot.SourceRunID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO pr_reviews(id,project_id,repository_id,source_run_id,run_id,reviewer_id,identity_key,attempt,snapshot) VALUES($1,$2,$3,$4,$5,$6,'legacy',1,$7)`, review, snapshot.ProjectID, snapshot.RepositoryID, snapshot.SourceRunID, run, snapshot.Reviewer.AgentID, raw); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		var status string
		if err = p.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, run).Scan(&status); err != nil || status != "queued" {
			t.Fatal("upgrade changed the active review", status, err)
		}
	})
}

func TestWorkSessionConstraints(t *testing.T) {
	p := testsupport.Database(t)
	project, task, run := workHistory(t, p)
	otherProject, otherTask, _ := workHistory(t, p)
	insert := func(project, task uuid.UUID, kind string, run *uuid.UUID, secret []byte) error {
		_, err := p.Exec(t.Context(), `INSERT INTO work_sessions(id,project_id,task_id,kind,run_id,secret_digest,admission_digest,client_name,client_version,admitted_revision,admitted_prerequisites,claimed_at) VALUES($1,$2,$3,$4,$5,$6,$7,'Client','1',1,'[]',now())`,
			uuid.New(), project, task, kind, run, secret, bytes.Repeat([]byte{1}, 32))
		return err
	}
	digest := bytes.Repeat([]byte{2}, 32)
	for _, bad := range []struct {
		name          string
		project, task uuid.UUID
		kind          string
		run           *uuid.UUID
		secret        []byte
	}{
		{"foreign task", otherProject, task, "external", nil, digest},
		{"native task mismatch", otherProject, otherTask, "native", &run, nil},
		{"external missing credential", project, task, "external", nil, nil},
		{"external short credential", project, task, "external", nil, []byte("short")},
		{"external run", project, task, "external", &run, digest},
		{"native credential", project, task, "native", &run, digest},
		{"native missing run", project, task, "native", nil, nil},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if err := insert(bad.project, bad.task, bad.kind, bad.run, bad.secret); err == nil {
				t.Fatal("invalid session accepted")
			}
		})
	}
	if err := insert(project, task, "external", nil, digest); err != nil {
		t.Fatal(err)
	}
	if err := insert(project, task, "native", &run, nil); err == nil {
		t.Fatal("two current owners")
	}
	for _, sql := range []string{
		`UPDATE work_sessions SET outcome='succeeded'`,
		`UPDATE work_sessions SET heartbeat_sequence=-1`,
		`UPDATE work_sessions SET last_contact_at=claimed_at-interval '1 second'`,
		`UPDATE tasks SET parent_task_id=id`,
	} {
		if _, err := p.Exec(t.Context(), sql); err == nil {
			t.Fatal("invalid shape accepted", sql)
		}
	}
	if _, err := p.Exec(t.Context(), `UPDATE work_sessions SET released_at=now(),release_reason='User released'`); err != nil {
		t.Fatal(err)
	}
	if err := insert(project, task, "native", &run, nil); err != nil {
		t.Fatal("could not claim released task", err)
	}
}
