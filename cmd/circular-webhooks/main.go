// circular-webhooks is the only public provider ingress. It mounts no control API.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/webhooks"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8001", "dedicated webhook listener")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, *listen); err != nil {
		slog.Error("Webhook receiver stopped; check private configuration")
		os.Exit(1)
	}
}
func run(ctx context.Context, address string) error {
	cfg, err := pgxpool.ParseConfig(postgres.DatabaseURL(os.Getenv("DATABASE_URL")))
	if err != nil {
		return err
	}
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	config := integrations.LoadConfig(os.Getenv)
	config.GitHubPrivateKeyFile = "" // reception never needs signing authority
	service, err := integrations.New(pool, config)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: address, Handler: webhooks.NewHandler(service, service, time.Now), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
