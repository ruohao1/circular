package migrate_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func legacyDatabase(t *testing.T, revision int) *pgxpool.Pool {
	t.Helper()
	pool := testsupport.EmptyDatabase(t)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	for version := 1; version <= revision; version++ {
		sql, err := os.ReadFile(fmt.Sprintf("%04d.sql", version))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
		if version == 5 {
			if err := agents.BackfillDiscovery(t.Context(), tx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(t.Context(), `CREATE TABLE alembic_version(version_num VARCHAR(32) PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO alembic_version VALUES($1)`, fmt.Sprintf("%04d", revision)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestReviewLegacyUpgradeKeepsSnapshot(t *testing.T) {
	pool := legacyDatabase(t, 20)
	snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	review, run := uuid.New(), uuid.New()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,parent_run_id,backend,status,attempt,external_refs,kind) VALUES($1,$2,$3,$4,'codex','succeeded',2,'{}','pr_review')`, run, snapshot.TaskID, snapshot.Reviewer.AgentID, snapshot.SourceRunID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO pr_reviews(id,project_id,repository_id,source_run_id,run_id,reviewer_id,identity_key,attempt,snapshot) VALUES($1,$2,$3,$4,$5,$6,'legacy',1,$7)`, review, snapshot.ProjectID, snapshot.RepositoryID, snapshot.SourceRunID, run, snapshot.Reviewer.AgentID, raw); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var saved prreviews.LaunchSnapshot
	var native *uuid.UUID
	var external *uuid.UUID
	if err = pool.QueryRow(t.Context(), `SELECT snapshot,source_run_id,source_work_session_id FROM pr_reviews WHERE id=$1`, review).Scan(&saved, &native, &external); err != nil {
		t.Fatal(err)
	}
	sealed, err := prreviews.SealSnapshot(saved)
	if err != nil || sealed.InputFingerprint != snapshot.InputFingerprint || external != nil || native == nil || *native != snapshot.SourceRunID {
		t.Fatal("legacy identity changed", sealed, err)
	}
}

func TestReviewMigrationPreservesIdentityAndDefaultsOff(t *testing.T) {
	pool := legacyDatabase(t, 11)
	project, agent, task, run, custom := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Existing review project') RETURNING id),
	 a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2,id,'Coder','codex','Keep coder','{}',true FROM p RETURNING id),
	 t AS (INSERT INTO tasks(id,project_id,title,description,status,external_refs) SELECT $3,id,'Task','Keep requirements','open','{}' FROM p RETURNING id)
	 INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) SELECT $4,t.id,a.id,'codex','succeeded',1,'{}' FROM t,a`, project, agent, task, run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'PR reviewer','codex','Keep custom role','{"model":"gpt-5.6-terra"}',false)`, custom, project)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var kind, name, original string
	var automatic bool
	var count int
	err = pool.QueryRow(t.Context(), `SELECT r.kind,s.automatic,a.name,(SELECT instructions FROM agents WHERE id=$3),(SELECT count(*) FROM runs) FROM runs r JOIN pr_review_settings s ON s.project_id=$2 JOIN agents a ON a.id=s.reviewer_id WHERE r.id=$1`, run, project, custom).Scan(&kind, &automatic, &name, &original, &count)
	if err != nil || kind != "coding" || automatic || name != "PR reviewer (2)" || original != "Keep custom role" || count != 1 {
		t.Fatal("upgrade lost state or started model work", kind, automatic, name, original, count, err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs,kind) VALUES($1,$2,$3,'codex','queued',2,'{}','pr_review')`, uuid.New(), task, agent)
	if err == nil {
		t.Fatal("accepted a review run without its review record")
	}
}
