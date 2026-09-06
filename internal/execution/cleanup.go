package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runtimes"
)

// Cleanup settles a terminal Run despite caller cancellation, with a one-minute
// cooperative budget (the runtime may finish its own bounded compensation).
// It stops/removes the container, retains output outside database locks, then
// fences exact worktree release and its Workspace Events in one transaction.
// The caller still owns heartbeats and claim release; this is not a supervisor.
func (r *Retention) Cleanup(caller context.Context, id uuid.UUID, docker *runtimes.Docker) (result error) {
	if docker == nil {
		return ErrRetention
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), time.Minute)
	defer cancel()
	defer func() {
		if result == nil || errors.Is(result, postgres.ErrLeaseLost) {
			return
		}
		record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Do not persist arbitrary Git, filesystem, database, or runtime error
		// strings. Their typed causes remain available to the trusted caller.
		err := r.store.WithRun(record, id, func(run *postgres.RunResources) error {
			return run.RecordCleanupFailure("Run resource cleanup failed; retained resources require retry")
		})
		result = errors.Join(result, err)
	}()
	var initial postgres.ResourceState
	alreadyReleased := false
	if err := r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		var err error
		initial, err = run.State()
		if err != nil {
			return err
		}
		if !initial.Status.Terminal() {
			return postgres.ErrResourceState
		}
		if initial.Workspace != nil && initial.Workspace.Status == "released" {
			alreadyReleased = true
			return nil
		}
		containerID := ""
		if initial.Workspace != nil && initial.Workspace.ContainerID != nil {
			containerID = *initial.Workspace.ContainerID
		}
		return docker.Release(ctx, id, containerID)
	}); err != nil {
		return err
	}
	if alreadyReleased || initial.Workspace == nil {
		return nil
	}
	target, err := r.target(initial)
	if err != nil {
		return err
	}
	present, err := pathExists(target)
	if err != nil {
		return err
	}
	if present {
		if initial.RepositoryID == nil {
			return ErrRetention
		}
		if err := r.Retain(ctx, id); err != nil {
			return err
		}
	}
	return r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		current, err := run.State()
		if err != nil {
			return err
		}
		if !current.Status.Terminal() || current.Workspace == nil {
			return postgres.ErrResourceState
		}
		if current.Workspace.Status == "released" {
			return nil
		}
		path, err := r.target(current)
		if err != nil || path != target || current.Workspace.ID != initial.Workspace.ID || !sameRepository(current.RepositoryID, initial.RepositoryID) {
			return ErrRetention
		}
		nowPresent, err := pathExists(target)
		if err != nil {
			return err
		}
		if nowPresent && !present {
			return ErrRetention
		}
		if current.RepositoryID != nil {
			repository := filepath.Join(r.repositoryRoot, current.RepositoryID.String())
			cacheExists, err := pathExists(repository)
			if err != nil {
				return err
			}
			if cacheExists {
				if err := r.git.Release(ctx, git.Worktree{RunID: id, RepositoryPath: repository, Path: target, Branch: "circular/run/" + id.String()}, git.ReleaseOptions{DiscardChanges: true}); err != nil {
					return err
				}
			} else if present {
				return ErrRetention
			}
		}
		return run.ReleaseWorkspace()
	})
}

func sameRepository(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
