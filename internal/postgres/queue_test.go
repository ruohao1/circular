package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"github.com/ruohao1/circular/internal/worker"
)

// Each test gets a new schema built by the production Go migrations. Neither
// claiming nor cleanup can see unrelated Run rows in the supplied database.
func database(t *testing.T) *pgxpool.Pool { t.Helper(); return testsupport.Database(t) }

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

func TestIndependentPoolsShareRowLocksAndRecoveryFencing(t *testing.T) {
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
	otherPool, err := pgxpool.NewWithConfig(t.Context(), pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	other := postgres.NewQueue(otherPool)
	if claim := acquire(t, other, "other-worker"); claim == nil || claim.RunID != ids[1] {
		t.Fatal("independent pool did not skip the held row lock")
	}
	if claim := acquire(t, q, "go-worker"); claim == nil || claim.RunID != ids[2] {
		t.Fatal("Go did not honor both the row lock and the other pool's committed claim")
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if claim := acquire(t, q, "go-worker"); claim == nil || claim.RunID != ids[0] {
		t.Fatal("rolled-back row was not claimable")
	}
	expire(t, pool, ids[1])
	if claim := acquire(t, q, "go-recovery"); claim == nil || !claim.Recovery || claim.RunID != ids[1] {
		t.Fatal("Go could not recover the expired claim")
	}
	if err := other.ReconcileExit(t.Context(), ids[1], "other-worker"); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale pool owner bypassed recovery fence: %v", err)
	}
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
