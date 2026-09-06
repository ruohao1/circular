// circular-worker-go owns durable claims and native Run execution.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/worker"
)

func main() {
	check := flag.Bool("check", false, "check native execution configuration without claiming Runs")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Cleanup shields caller cancellation while it publishes output and releases
	// owned resources. Bound the whole process as well as individual operations,
	// leaving five seconds inside Compose's 90-second termination grace period.
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, *check) }()
	var err error
	select {
	case err = <-finished:
	case <-ctx.Done():
		timer := time.NewTimer(85 * time.Second)
		defer timer.Stop()
		select {
		case err = <-finished:
		case <-timer.C:
			err = fmt.Errorf("worker shutdown deadline exceeded; unfinished claims retain recovery leases")
		}
	}
	if err != nil {
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
	if check {
		poolConfig.MinConns = 0
		poolConfig.MinIdleConns = 0
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("initialize worker database pool")
	}
	defer pool.Close()
	native, err := execution.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	executor, err := execution.NewSupervisor(pool, config.WorkerID, native)
	if err != nil {
		return fmt.Errorf("Go execution configuration is invalid: %w", err)
	}
	if check {
		slog.Info("Go execution configuration is valid; no Runs claimed")
		return nil
	}
	err = worker.Run(ctx, postgres.NewQueue(pool), executor, config.WorkerID, config.Poll)
	if err != nil {
		return fmt.Errorf("worker database operation failed; uncompleted claims retain recovery leases")
	}
	return nil
}
