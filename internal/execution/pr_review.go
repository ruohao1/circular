package execution

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func configureReviewRoots(config *Config, retention *Retention) error {
	if config.ReviewContextRoot == "" {
		config.ReviewContextRoot = filepath.Join(filepath.Dir(retention.worktreeRoot), "review-contexts")
	}
	path := filepath.Clean(config.ReviewContextRoot)
	if !filepath.IsAbs(path) {
		return ErrConfiguration
	}
	resolved, err := resolve(path)
	if err != nil || resolved != path || resolved == "/" {
		return ErrConfiguration
	}
	for _, other := range []string{retention.worktreeRoot, retention.repositoryRoot, config.ArtifactRoot, config.CodexAuthRoot} {
		if other == "" {
			continue
		}
		other, err = resolve(filepath.Clean(other))
		if err != nil || other == path || strings.HasPrefix(path, other+"/") || strings.HasPrefix(other, path+"/") {
			return ErrConfiguration
		}
	}
	config.ReviewContextRoot = path
	retention.reviewContextRoot = path
	if config.Docker.ReviewContextRoot == "" {
		config.Docker.ReviewContextRoot = path
	}
	return nil
}
func reviewExecutionContext(ctx context.Context, state postgres.ResourceState) (context.Context, context.CancelFunc, error) {
	if state.Kind != runstate.PRReview {
		return ctx, func() {}, nil
	}
	if state.StartedAt == nil {
		return nil, nil, postgres.ErrResourceState
	}
	execution, stop := context.WithDeadline(ctx, state.StartedAt.Add(prreviews.ExecutionLimit))
	return execution, stop, nil
}
