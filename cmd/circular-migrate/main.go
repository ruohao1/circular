package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/postgres"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("cannot read migration .env configuration")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgresql://circular:circular@localhost:5432/circular"
	}
	config, err := pgxpool.ParseConfig(postgres.DatabaseURL(dsn))
	if err != nil {
		slog.Error("DATABASE_URL is not a valid PostgreSQL configuration")
		os.Exit(1)
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		slog.Error("cannot initialize migration database pool")
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool); err != nil {
		// SQL and connection errors can contain credentials or schema data.
		slog.Error("schema migration failed; database changes were rolled back")
		os.Exit(1)
	}
	slog.Info("database schema is current", "version", migrate.Head)
}
