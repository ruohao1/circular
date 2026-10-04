// circular-e2e-stack serves a disposable Go API and worker for browser tests.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"github.com/ruohao1/circular/internal/webhooks"
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
		"CIRCULAR_REPOSITORY_CACHE_ROOT":      filepath.Join(root, "repositories"),
		"CIRCULAR_WORKTREE_ROOT":              filepath.Join(root, "worktrees"),
		"CIRCULAR_DOCKER_WORKTREE_ROOT":       filepath.Join(root, "worktrees"),
		"CIRCULAR_ARTIFACT_ROOT":              filepath.Join(root, "artifacts"),
		"CIRCULAR_REVIEW_CONTEXT_ROOT":        filepath.Join(root, "review-contexts"),
		"CIRCULAR_DOCKER_REVIEW_CONTEXT_ROOT": filepath.Join(root, "review-contexts"),
		"CIRCULAR_CODEX_ENABLED":              "true",
		"CIRCULAR_CODEX_AUTH_MODE":            "api_key",
		"CIRCULAR_CODEX_API_KEY":              "fixture-only-not-a-real-key",
		"CIRCULAR_CODEX_IMAGE":                "circular-review-fixture:test",
		"CIRCULAR_RUNNER_IMAGE":               "circular-isq162-runner:test",
		"CIRCULAR_POLL_INTERVAL_SECONDS":      "0.1",
	}
	native, err := execution.LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		return err
	}
	source, gitBinary, base, head, err := prepareReviewFixture(ctx, root)
	if err != nil {
		return err
	}
	native.Git.GitExecutable = gitBinary
	providerListener, err := net.Listen("tcp", "127.0.0.1:18001")
	if err != nil {
		return err
	}
	provider := testsupport.NewProviderFixture()
	gitDelivery := &fixtureGitDelivery{source: source, provider: provider}
	providerServer := &http.Server{Handler: gitDelivery, ReadHeaderTimeout: 5 * time.Second}
	defer providerServer.Close()
	go func() { _ = providerServer.Serve(providerListener) }()
	providerURL := "http://127.0.0.1:18001"
	connections := integrations.Config{
		APIURL: "http://127.0.0.1:18000", WebURL: "http://127.0.0.1:15173",
		EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("fixture-", 4))),
		GitHubURL:     providerURL, GitHubAPIURL: providerURL, LinearURL: providerURL, LinearAPIURL: providerURL,
	}
	receiverService, err := integrations.New(pool, connections)
	if err != nil {
		return err
	}
	receiverListener, err := net.Listen("tcp", "127.0.0.1:18002")
	if err != nil {
		return err
	}
	receiverServer := &http.Server{Handler: webhooks.NewHandler(receiverService, receiverService, time.Now), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second}
	defer func() { _ = receiverServer.Close() }()
	go func(server *http.Server, listener net.Listener) { _ = server.Serve(listener) }(receiverServer, receiverListener)
	native.Integrations = connections
	owner := "browser-" + uuid.NewString()
	executor, err := execution.NewSupervisor(pool, owner, native)
	if err != nil {
		return err
	}
	apiConfig := httpapi.Config{ArtifactRoot: values["CIRCULAR_ARTIFACT_ROOT"], CORSOrigins: []string{"http://127.0.0.1:15173"}, SSEPollInterval: 50 * time.Millisecond, Integrations: connections}
	apiConfig.RepositoryCacheRoot = values["CIRCULAR_REPOSITORY_CACHE_ROOT"]
	handler, err := httpapi.New(pool, apiConfig)
	if err != nil {
		return err
	}
	backgroundCtx, cancelBackground := context.WithCancel(ctx)
	backgroundDone := make(chan error, 1)
	go func() { backgroundDone <- httpapi.RunBackground(backgroundCtx, pool, apiConfig) }()
	defer func() {
		cancelBackground()
		<-backgroundDone
	}()
	queue := &fixtureQueue{Queue: postgres.NewQueue(pool)}
	apiHandler := &fixtureHandler{handler: handler}
	restart := func() error {
		cancelBackground()
		<-backgroundDone
		next, e := httpapi.New(pool, apiConfig)
		if e != nil {
			return e
		}
		apiHandler.mu.Lock()
		apiHandler.handler = next
		apiHandler.mu.Unlock()
		_ = receiverServer.Close()
		listener, e := net.Listen("tcp", "127.0.0.1:18002")
		if e != nil {
			return e
		}
		service, e := integrations.New(pool, connections)
		if e != nil {
			return e
		}
		receiverServer = &http.Server{Handler: webhooks.NewHandler(service, service, time.Now), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second}
		go func(server *http.Server) { _ = server.Serve(listener) }(receiverServer)
		backgroundCtx, cancelBackground = context.WithCancel(ctx)
		backgroundDone = make(chan error, 1)
		go func(ctx context.Context, done chan error) { done <- httpapi.RunBackground(ctx, pool, apiConfig) }(backgroundCtx, backgroundDone)
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/linear-agent", linearAgentFixtureHandler(pool, provider, prefix, queue, gitDelivery, restart))
	mux.HandleFunc("/fixture/pr-review", reviewFixtureHandler(pool, provider, prefix, source, base, head))
	mux.Handle("/", apiHandler)
	server := &http.Server{Addr: "127.0.0.1:18000", Handler: mux, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	apiDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { apiDone <- server.ListenAndServe() }()
	go func() { workerDone <- worker.Run(ctx, queue, executor, owner, 100*time.Millisecond) }()
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

type fixtureHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func (f *fixtureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	h := f.handler
	f.mu.RUnlock()
	h.ServeHTTP(w, r)
}
