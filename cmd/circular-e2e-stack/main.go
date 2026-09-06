// circular-e2e-stack serves a disposable Go API and worker for browser tests.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("test stack failed", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context) error {
	dsn, prefix := os.Getenv("TEST_DATABASE_URL"), os.Getenv("CIRCULAR_E2E_PREFIX")
	if dsn == "" || !strings.HasPrefix(prefix, "__circular_ui_test_") || len(prefix) < 25 {
		return errors.New("disposable TEST_DATABASE_URL and unique CIRCULAR_E2E_PREFIX are required")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	config, err := pgxpool.ParseConfig(postgres.DatabaseURL(dsn))
	if err != nil {
		return errors.New("invalid test database configuration")
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot initialize test database")
	}
	defer admin.Close()
	schema := "circular_ui_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		return errors.New("cannot create isolated test schema")
	}
	root, err := os.MkdirTemp("", "circular-ui-e2e-")
	if err != nil {
		return err
	}
	config = config.Copy()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return err
	}
	defer pool.Close()
	cleanupSafe := true
	defer func() {
		if !cleanupSafe {
			slog.Error("test resources retained for recovery", "schema", schema, "root", root)
			return
		}
		pool.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			slog.Error("could not remove test schema", "schema", schema)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			slog.Error("could not remove test-owned directory", "root", root)
		}
	}()
	if err := migrate.Up(ctx, pool); err != nil {
		return errors.New("test schema migration failed")
	}
	values := map[string]string{
		"CIRCULAR_REPOSITORY_CACHE_ROOT": filepath.Join(root, "repositories"),
		"CIRCULAR_WORKTREE_ROOT":         filepath.Join(root, "worktrees"),
		"CIRCULAR_DOCKER_WORKTREE_ROOT":  filepath.Join(root, "worktrees"),
		"CIRCULAR_ARTIFACT_ROOT":         filepath.Join(root, "artifacts"),
		"CIRCULAR_RUNNER_IMAGE":          "circular-isq162-runner:test",
		"CIRCULAR_POLL_INTERVAL_SECONDS": "0.1",
	}
	native, err := execution.LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		return err
	}
	owner := "browser-" + uuid.NewString()
	executor, err := execution.NewSupervisor(pool, owner, native)
	if err != nil {
		return err
	}
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: values["CIRCULAR_ARTIFACT_ROOT"], CORSOrigins: []string{"http://127.0.0.1:15173"}, SSEPollInterval: 50 * time.Millisecond})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: "127.0.0.1:18000", Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	apiDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { apiDone <- server.ListenAndServe() }()
	go func() { workerDone <- worker.Run(ctx, postgres.NewQueue(pool), executor, owner, 100*time.Millisecond) }()
	cleanupSafe = false
	workerStopped := false
	select {
	case <-ctx.Done():
	case err = <-apiDone:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case err = <-workerDone:
		workerStopped = true
	}
	cancel()
	cleanup, stop := context.WithTimeout(context.Background(), 85*time.Second)
	defer stop()
	if !workerStopped {
		select {
		case workerErr := <-workerDone:
			err = errors.Join(err, workerErr)
		case <-cleanup.Done():
			return errors.New("worker cleanup timed out; test resources retained")
		}
	}
	if shutdownErr := server.Shutdown(cleanup); shutdownErr != nil {
		return errors.New("API shutdown failed")
	}
	var active int
	if queryErr := pool.QueryRow(cleanup, `SELECT count(*) FROM runs WHERE worker_id IS NOT NULL OR id IN (SELECT run_id FROM workspaces WHERE status<>'released')`).Scan(&active); queryErr != nil || active != 0 {
		return fmt.Errorf("%d unfinished test claims; resources retained", active)
	}
	cleanupSafe = true
	return err
}
