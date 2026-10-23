package integrations_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestGitHubReconnectConcurrentCompletionRefresh(t *testing.T) {
	f := githubAppFixture(t)
	source := testsupport.SeedExternalPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(source.PR, "open")
	identity, err := f.service.SaveGitHubIdentity(t.Context(), f.project, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	gate, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.WithoutCancel(ctx))
	var gatePID int
	if err := gate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}
	// Pause reconnect at app settings and refresh at planning after their identity/binding locks.
	if _, err := gate.Exec(ctx, `SELECT provider FROM integration_apps WHERE provider='github' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, f.project); err != nil {
		t.Fatal(err)
	}
	waitForLock := func(query string, pid int) {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if err := f.pool.QueryRow(ctx, query, pid).Scan(&blocked); err != nil {
				t.Fatal("waiting for staged database lock:", err)
			}
			if blocked {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("operation did not reach staged database lock:", ctx.Err())
			case <-ticker.C:
			}
		}
	}
	reconnected := make(chan error, 1)
	wg.Go(func() {
		_, err := f.service.SaveGitHubIdentity(ctx, f.project, nil)
		reconnected <- err
	})
	waitForLock(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid)))`, gatePID)
	refresh, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var refreshPID int
	if err := refresh.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&refreshPID); err != nil {
		_ = refresh.Rollback(context.WithoutCancel(ctx))
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	wg.Go(func() {
		_, err := postgres.ClaimCompletionEvidence(ctx, refresh, source.TaskID, true, time.Now().UTC())
		_ = refresh.Rollback(context.WithoutCancel(ctx))
		refreshed <- err
	})
	waitForLock(`SELECT cardinality(pg_blocking_pids($1::int)) > 0`, refreshPID)
	if err := gate.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	reconnectErr, refreshErr := <-reconnected, <-refreshed
	if reconnectErr != nil || refreshErr != nil {
		t.Fatalf("GitHub reconnect and completion refresh must both succeed: reconnect=%v refresh=%v", reconnectErr, refreshErr)
	}
	var generation int64
	if err := f.pool.QueryRow(ctx, `SELECT generation FROM provider_identities WHERE id=$1`, identity.IdentityID).Scan(&generation); err != nil || generation != 2 {
		t.Fatal("reconnect did not update the existing identity", generation, err)
	}
}
