package postgres_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/worker"
)

// Each test gets a new schema built by the actual Alembic migrations. Neither
// claiming nor cleanup can see unrelated Run rows in the supplied database.
func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for real PostgreSQL parity tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, postgres.DatabaseURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	schema := "circular_go_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("remove test-owned schema %s: %v", schema, err)
		}
		admin.Close()
	})
	runPython(t, dsn, schema, "-m", "alembic", "upgrade", "head")
	config, err := pgxpool.ParseConfig(postgres.DatabaseURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func runPython(t *testing.T, dsn, schema string, args ...string) []byte {
	t.Helper()
	parsed, err := url.Parse(postgres.DatabaseURL(dsn))
	if err != nil || parsed.Scheme == "" {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	parsed.Scheme = "postgresql+psycopg"
	query := parsed.Query()
	query.Del("options")
	parsed.RawQuery = query.Encode()
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	python := os.Getenv("CIRCULAR_EXECUTOR_PYTHON")
	if python == "" {
		python = filepath.Join(root, ".venv/bin/python")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, args...)
	command.Dir = root
	// libpq's environment setting keeps the test schema out of Alembic's
	// ConfigParser-interpolated URL while still using the real migrations.
	command.Env = append(os.Environ(), "DATABASE_URL="+parsed.String(), "PGOPTIONS=-csearch_path="+schema)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run Python against isolated schema: %v\n%s", err, output)
	}
	return output
}

func seed(t *testing.T, pool *pgxpool.Pool, count int) []uuid.UUID {
	t.Helper()
	ctx := t.Context()
	project, agent := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO projects (id, name) VALUES ($1, 'Go worker parity')`, project)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO agents
		(id, project_id, name, backend, instructions, backend_config, enabled)
		VALUES ($1, $2, 'Engineer', 'fake', '', '{}', true)`, agent, project)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uuid.UUID, 0, count)
	for i := range count {
		task, run := uuid.New(), uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO tasks
			(id, project_id, title, description, status, external_refs)
			VALUES ($1, $2, 'Execute fixture', '', 'open', '{}')`, task, project)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO runs
			(id, task_id, agent_id, backend, status, attempt, external_refs, created_at)
			VALUES ($1, $2, $3, 'fake', 'queued', 1, '{}', $4)`, run, task, agent,
			time.Date(2000, 1, 1, 0, 0, i, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run)
	}
	return ids
}

func acquire(t *testing.T, q *postgres.Queue, owner string) *worker.Claim {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	claim, err := q.Acquire(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func expire(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE runs SET lease_expires_at = $2 WHERE id = $1`,
		id, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseURLOnlyRewritesTheDriverPrefix(t *testing.T) {
	if postgres.DatabaseURL("postgresql+psycopg://localhost/db") != "postgresql://localhost/db" {
		t.Fatal("SQLAlchemy URL was not normalized")
	}
	original := "postgresql://localhost/db?application_name=postgresql+psycopg://preserve"
	if postgres.DatabaseURL(original) != original {
		t.Fatal("a URL value was rewritten instead of its driver prefix")
	}
}

func TestConcurrentWorkersClaimDistinctRuns(t *testing.T) {
	pool := database(t)
	ids := seed(t, pool, 2)
	q := postgres.NewQueue(pool)
	type result struct {
		claim *worker.Claim
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, owner := range []string{"go-one", "go-two"} {
		go func() {
			<-start
			claim, err := q.Acquire(t.Context(), owner)
			results <- result{claim, err}
		}()
	}
	close(start)
	seen := map[uuid.UUID]bool{}
	for range 2 {
		r := <-results
		if r.err != nil || r.claim == nil || r.claim.Recovery {
			t.Fatalf("claim failed: %+v", r)
		}
		seen[r.claim.RunID] = true
	}
	if len(seen) != 2 || !seen[ids[0]] || !seen[ids[1]] {
		t.Fatal("workers did not own distinct fixture Runs")
	}
	if claim := acquire(t, q, "third"); claim != nil {
		t.Fatal("a live lease was stolen")
	}
}

func TestLockedRunIsSkippedAndRollbackMakesItClaimable(t *testing.T) {
	pool := database(t)
	ids := seed(t, pool, 2)
	q := postgres.NewQueue(pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SELECT id FROM runs WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if claim := acquire(t, q, "other"); claim == nil || claim.RunID != ids[1] {
		t.Fatal("claiming did not skip the locked Run")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if claim := acquire(t, q, "after-rollback"); claim == nil || claim.RunID != ids[0] {
		t.Fatal("released row lock did not make the original Run claimable")
	}
}

func TestPythonAndGoClaimersShareRowLocksAndRecoveryFencing(t *testing.T) {
	pool := database(t)
	ids := seed(t, pool, 3)
	q := postgres.NewQueue(pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), `SELECT id FROM runs WHERE id = $1 FOR UPDATE`, ids[0]); err != nil {
		t.Fatal(err)
	}
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	output := runPython(t, os.Getenv("TEST_DATABASE_URL"), schema, "-c", `
import asyncio, os
from circular.storage import RunStore, create_engine, create_session_factory
async def main():
    engine = create_engine(os.environ["DATABASE_URL"])
    try:
        async with create_session_factory(engine).begin() as session:
            run = await RunStore().claim_next(session, "python-worker")
            print(run.id)
    finally:
        await engine.dispose()
asyncio.run(main())
`)
	if strings.TrimSpace(string(output)) != ids[1].String() {
		t.Fatalf("Python did not skip the Go-held row lock: %s", output)
	}
	if claim := acquire(t, q, "go-worker"); claim == nil || claim.RunID != ids[2] {
		t.Fatal("Go did not honor both the row lock and Python's committed claim")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if claim := acquire(t, q, "go-worker"); claim == nil || claim.RunID != ids[0] {
		t.Fatal("rolled-back row was not claimable")
	}
	expire(t, pool, ids[1])
	if claim := acquire(t, q, "go-recovery"); claim == nil || !claim.Recovery || claim.RunID != ids[1] {
		t.Fatal("Go could not recover the expired Python claim")
	}
	runPython(t, os.Getenv("TEST_DATABASE_URL"), schema, "-c", `
import asyncio, os, sys
from uuid import UUID
from circular.storage import RunStore, create_engine, create_session_factory
from circular.storage.repositories import RunLeaseLostError
async def main():
    engine = create_engine(os.environ["DATABASE_URL"])
    sessions = create_session_factory(engine)
    sessions.configure(info={"worker_id": "python-worker"})
    try:
        try:
            async with sessions.begin() as session:
                await RunStore().lock_for_execution(session, UUID(sys.argv[1]))
        except RunLeaseLostError:
            pass
        else:
            raise AssertionError("stale Python owner bypassed the Go recovery fence")
    finally:
        await engine.dispose()
asyncio.run(main())
`, ids[1].String())
}

func TestQueuedCancellationAllocatesNoClaim(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	if _, err := pool.Exec(t.Context(), `UPDATE runs SET status = 'cancelled' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if claim := acquire(t, postgres.NewQueue(pool), "worker"); claim != nil {
		t.Fatal("cancelled queued Run was claimed")
	}
}

func TestExpiredOwnerIsRecoveredOnceAndFenced(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	q := postgres.NewQueue(pool)
	acquire(t, q, "old-owner")
	expire(t, pool, id)
	claim := acquire(t, q, "replacement")
	if claim == nil || !claim.Recovery || claim.RunID != id {
		t.Fatal("expired Run was not handed to cleanup recovery")
	}
	if claim := acquire(t, q, "competitor"); claim != nil {
		t.Fatal("live recovery lease was stolen")
	}
	if err := q.ReconcileExit(t.Context(), id, "old-owner"); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale owner was not fenced: %v", err)
	}
	var status, owner, message, source string
	var attempts, sequence, events int
	if err := pool.QueryRow(t.Context(), `SELECT status, worker_id, error, recovery_attempts FROM runs WHERE id = $1`,
		id).Scan(&status, &owner, &message, &attempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*), MAX(sequence), MAX(source) FROM events WHERE run_id = $1`,
		id).Scan(&events, &sequence, &source); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || owner != "replacement" || message != "worker lease expired" || attempts != 1 ||
		events != 1 || sequence != 1 || source != "worker-recovery" {
		t.Fatal("recovery did not preserve the existing persisted lifecycle/event contract")
	}
}

func TestRecoveryIsCappedAndDoesNotDuplicateFailure(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	q := postgres.NewQueue(pool)
	acquire(t, q, "original")
	for range 3 {
		expire(t, pool, id)
		if claim := acquire(t, q, "recovery"); claim == nil || !claim.Recovery {
			t.Fatal("expected bounded cleanup recovery")
		}
	}
	expire(t, pool, id)
	if claim := acquire(t, q, "fourth"); claim != nil {
		t.Fatal("recovery exceeded the existing three-attempt cap")
	}
	var events int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM events WHERE run_id = $1 AND type = 'run.failed'`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatal("recovery duplicated the terminal failure event")
	}
}

func TestEventFailureRollsBackRecoveryOwnership(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	q := postgres.NewQueue(pool)
	acquire(t, q, "original")
	expire(t, pool, id)
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_failure CHECK (type <> 'run.failed')`); err != nil {
		t.Fatal(err)
	}
	if claim, err := q.Acquire(t.Context(), "replacement"); err == nil || claim != nil {
		t.Fatal("recovery succeeded without its durable event")
	}
	var status, owner string
	var attempts int
	if err := pool.QueryRow(t.Context(), `SELECT status, worker_id, recovery_attempts FROM runs WHERE id = $1`, id).
		Scan(&status, &owner, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "provisioning" || owner != "original" || attempts != 0 {
		t.Fatal("failed event write partially committed recovery")
	}
}

func TestExecutorCrashFailsRunButRetainsCleanupOwnership(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	q := postgres.NewQueue(pool)
	acquire(t, q, "worker")
	for range 2 {
		if err := q.ReconcileExit(t.Context(), id, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	var status, owner, message string
	var events int
	if err := pool.QueryRow(t.Context(), `SELECT status, worker_id, error FROM runs WHERE id = $1`, id).
		Scan(&status, &owner, &message); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM events WHERE run_id = $1`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || owner != "worker" || message != "executor process exited without a terminal outcome" || events != 1 {
		t.Fatal("executor crash lost recovery ownership or duplicated failure")
	}
	expire(t, pool, id)
	if claim := acquire(t, q, "replacement"); claim == nil || !claim.Recovery {
		t.Fatal("executor crash could not be reconciled by a replacement")
	}
}

func TestTerminalOutcomeSurvivesExitAndRecovery(t *testing.T) {
	for _, status := range []string{"succeeded", "cancelled", "failed"} {
		t.Run(status, func(t *testing.T) {
			pool := database(t)
			id := seed(t, pool, 1)[0]
			q := postgres.NewQueue(pool)
			acquire(t, q, "original")
			if _, err := pool.Exec(t.Context(), `UPDATE runs SET status = $2, error = 'primary outcome' WHERE id = $1`, id, status); err != nil {
				t.Fatal(err)
			}
			if err := q.ReconcileExit(t.Context(), id, "original"); err != nil {
				t.Fatal(err)
			}
			expire(t, pool, id)
			if claim := acquire(t, q, "replacement"); claim == nil || !claim.Recovery {
				t.Fatal("terminal cleanup was not recoverable")
			}
			var got, message string
			var events int
			if err := pool.QueryRow(t.Context(), `SELECT status, error FROM runs WHERE id = $1`, id).Scan(&got, &message); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM events WHERE run_id = $1`, id).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if got != status || message != "primary outcome" || events != 0 {
				t.Fatal("cleanup replaced an existing terminal outcome")
			}
		})
	}
}
