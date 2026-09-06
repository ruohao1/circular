// circular-worker-go is migration stage one: Go claims, Python resource execution.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/worker"
)

func main() {
	check := flag.Bool("check", false, "check configuration and execution bridge without claiming Runs")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, *check); err != nil {
		slog.Error("Go worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, check bool) error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot read worker .env configuration")
	}
	config, err := worker.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	poolConfig, err := pgxpool.ParseConfig(postgres.DatabaseURL(config.DatabaseURL))
	if err != nil {
		// Driver errors can contain the original URL; do not log credentials.
		return fmt.Errorf("DATABASE_URL is not a valid PostgreSQL configuration")
	}
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	python, err := exec.LookPath(config.Python)
	if err != nil {
		return fmt.Errorf("Python execution bridge is unavailable; configure CIRCULAR_EXECUTOR_PYTHON")
	}
	preflightCtx, preflightCancel := context.WithTimeout(ctx, 10*time.Second)
	preflight := exec.CommandContext(preflightCtx, python, "-m", "circular.worker.execute_run", "--check")
	preflight.Stdout, preflight.Stderr = os.Stdout, os.Stderr
	err = preflight.Run()
	preflightCancel()
	if err != nil {
		return fmt.Errorf("Python execution bridge preflight failed; no Runs were claimed")
	}
	if check {
		slog.Info("Go worker configuration and Python execution bridge are valid; no Runs claimed")
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("initialize worker database pool")
	}
	defer pool.Close()
	executor := worker.ProcessExecutor{
		Command:     []string{python, "-m", "circular.worker.execute_run"},
		GracePeriod: 80 * time.Second, // Leave room inside Compose's 90-second grace.
	}
	err = worker.Run(ctx, postgres.NewQueue(pool), executor, config.WorkerID, config.Poll)
	if err != nil {
		return fmt.Errorf("worker database operation failed; uncompleted claims retain recovery leases")
	}
	return nil
}
