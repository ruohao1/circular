package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/integrations"
)

// RunBackground delivers durable integration updates independently of HTTP
// requests and coding runs. Its caller owns cancellation and waits for shutdown
// before closing the database pool. Multiple API processes may run this loop.
func RunBackground(ctx context.Context, pool *pgxpool.Pool, config Config) error {
	service, err := integrations.New(pool, integrationConfig(config))
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	for _, process := range []func(context.Context) (bool, error){service.ProcessIntegrationWebhook, service.ProcessLinearActivity, service.ProcessExternalRequest, service.ProcessLinearUpdate, service.ProcessGitHubRunDelivery, service.ProcessPRReviewLaunch, service.ProcessPRReviewPublication, service.ProcessPRReviewFreshness} {
		workers.Go(func() {
			for ctx.Err() == nil {
				worked, err := process(ctx)
				if err == nil && worked {
					continue
				}
				if err != nil && ctx.Err() == nil {
					// Provider errors may contain credentials or private content.
					slog.Warn("Integration delivery paused; retrying shortly")
				}
				timer := time.NewTimer(2 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		})
	}
	workers.Wait()
	return nil
}

func integrationConfig(config Config) integrations.Config {
	connections := config.Integrations
	connections.ArtifactRoot = config.ArtifactRoot
	connections.RepositoryCacheRoot = config.RepositoryCacheRoot
	return connections
}
