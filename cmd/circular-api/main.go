package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/ruohao1/circular/contracts"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/postgres"
)

func main() {
	openapi := flag.Bool("openapi", false, "print the checked-in HTTP contract without connecting")
	listen := flag.String("listen", ":8000", "HTTP listen address")
	flag.Parse()
	if *openapi {
		_, _ = os.Stdout.Write(contracts.OpenAPI)
		return
	}
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("cannot read API .env configuration")
		os.Exit(1)
	}
	config, err := httpapi.LoadConfig(os.Getenv)
	if err != nil {
		slog.Error("invalid API configuration", "error", err)
		os.Exit(1)
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgresql://circular:circular@localhost:5432/circular"
	}
	poolConfig, err := pgxpool.ParseConfig(postgres.DatabaseURL(dsn))
	if err != nil {
		slog.Error("DATABASE_URL is not a valid PostgreSQL configuration")
		os.Exit(1)
	}
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		slog.Error("cannot initialize API database pool")
		os.Exit(1)
	}
	defer pool.Close()
	handler, err := httpapi.New(pool, config)
	if err != nil {
		slog.Error("cannot initialize API")
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: handler, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	finished := make(chan error, 1)
	go func() { finished <- server.ListenAndServe() }()
	select {
	case err = <-finished:
	case <-ctx.Done():
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = server.Shutdown(cleanup)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("API stopped unsuccessfully")
		os.Exit(1)
	}
}
