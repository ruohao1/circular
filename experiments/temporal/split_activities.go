package temporaleval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

type SplitActivities struct{ Activities }

type cleanupState struct {
	status           runstate.Status
	owner            *string
	expires, claimed *time.Time
	released         bool
}

func (s cleanupState) done() bool { return s.owner == nil && s.status.Terminal() && s.released }
func (a SplitActivities) state(ctx context.Context, id uuid.UUID) (s cleanupState, err error) {
	err = a.Pool.QueryRow(ctx, `SELECT r.status,r.worker_id,r.lease_expires_at,r.claimed_at,
 NOT EXISTS(SELECT 1 FROM workspaces w WHERE w.run_id=r.id AND w.status<>'released')
 FROM runs r JOIN temporal_eval_admissions e ON e.run_id=r.id WHERE r.id=$1 AND r.backend='fake' AND r.kind='coding' AND r.invocation=1`, id).Scan(&s.status, &s.owner, &s.expires, &s.claimed, &s.released)
	return s, err
}

// ClaimCleanup returns a wait duration instead of polling inside an Activity.
func (a SplitActivities) ClaimCleanup(ctx context.Context, id uuid.UUID) (CleanupAdmission, error) {
	stop := heartbeat(ctx)
	defer stop()
	state, err := a.state(ctx, id)
	if err != nil {
		return CleanupAdmission{}, err
	}
	if state.done() {
		return CleanupAdmission{Done: true}, nil
	}
	if state.owner != nil {
		expires := state.expires
		if expires == nil && state.claimed != nil {
			value := state.claimed.Add(postgres.LeaseDuration)
			expires = &value
		}
		if expires != nil && expires.After(time.Now()) {
			return CleanupAdmission{Wait: time.Until(*expires) + 100*time.Millisecond}, nil
		}
	}
	owner := "temporal-split-cleanup:" + uuid.NewString()
	claim, err := postgres.NewQueue(a.Pool).Acquire(ctx, owner)
	if err != nil {
		return CleanupAdmission{}, err
	}
	if claim == nil || claim.RunID != id {
		return CleanupAdmission{}, errors.New("cleanup recovery unavailable; operator attention required")
	}
	if !claim.Recovery {
		store, err := postgres.NewResources(a.Pool, owner)
		if err != nil {
			return CleanupAdmission{}, err
		}
		if err = store.WithRun(ctx, id, func(r *postgres.RunResources) error {
			return r.RecordFailure("Temporal execution stopped before claiming the Run", nil)
		}); err != nil {
			return CleanupAdmission{}, err
		}
	}
	return CleanupAdmission{Claim: CleanupClaim{RunID: id, Owner: owner}}, nil
}

// Retain stops the exact allocation before capturing output that can no longer change.
func (a SplitActivities) Retain(ctx context.Context, claim CleanupClaim) error {
	return a.useClaim(ctx, claim, func(operation context.Context, store *postgres.Resources, retention *execution.Retention, docker *runtimes.Docker) (result error) {
		defer func() {
			if result == nil || errors.Is(result, postgres.ErrLeaseLost) {
				return
			}
			record, cancel := context.WithTimeout(context.WithoutCancel(operation), 5*time.Second)
			defer cancel()
			result = errors.Join(result, store.WithRun(record, claim.RunID, func(r *postgres.RunResources) error {
				return r.RecordCleanupFailure("Run output retention failed; resources require retry")
			}))
		}()
		var state postgres.ResourceState
		if err := store.WithRun(operation, claim.RunID, func(r *postgres.RunResources) error {
			var err error
			state, err = r.State()
			if err != nil {
				return err
			}
			if !state.Status.Terminal() {
				return postgres.ErrResourceState
			}
			if state.Workspace != nil && state.Workspace.Status == "released" {
				return nil
			}
			container := ""
			if state.Workspace != nil && state.Workspace.ContainerID != nil {
				container = *state.Workspace.ContainerID
			}
			return docker.Release(operation, claim.RunID, container)
		}); err != nil {
			return err
		}
		if state.Workspace == nil || state.Workspace.Status == "released" {
			return nil
		}
		path := filepath.Join(a.Config.Git.WorktreeRoot, claim.RunID.String())
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		return retention.Retain(operation, claim.RunID)
	})
}

func (a SplitActivities) Release(ctx context.Context, claim CleanupClaim) error {
	return a.useClaim(ctx, claim, func(operation context.Context, store *postgres.Resources, retention *execution.Retention, docker *runtimes.Docker) error {
		if err := retention.Cleanup(operation, claim.RunID, docker); err != nil {
			return err
		}
		return store.WithRun(operation, claim.RunID, func(r *postgres.RunResources) error { return r.ReleaseClaim() })
	})
}

// Database heartbeats outlive caller cancellation while bounded cleanup still owns resources.
func (a SplitActivities) useClaim(ctx context.Context, claim CleanupClaim, action func(context.Context, *postgres.Resources, *execution.Retention, *runtimes.Docker) error) error {
	stop := heartbeat(ctx)
	defer stop()
	state, err := a.state(ctx, claim.RunID)
	if err != nil {
		return err
	}
	if state.done() {
		return nil
	}
	store, err := postgres.NewResources(a.Pool, claim.Owner)
	if err != nil {
		return err
	}
	if _, err = store.Heartbeat(ctx, claim.RunID); err != nil {
		return err
	}
	retention, err := execution.NewRetention(store, a.Config.Git, a.Config.ArtifactRoot)
	if err != nil {
		return err
	}
	config := a.Config.Docker
	if config.WorktreeRoot == "" {
		config.WorktreeRoot = a.Config.Git.WorktreeRoot
	}
	docker, err := runtimes.NewDocker(config)
	if err != nil {
		return err
	}
	operation, cancelOperation := context.WithCancel(ctx)
	defer cancelOperation()
	monitor, cancelMonitor := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitor.Done():
				return
			case <-ticker.C:
			}
			beat, cancel := context.WithTimeout(monitor, postgres.LeaseDuration)
			_, err := store.Heartbeat(beat, claim.RunID)
			cancel()
			if err != nil {
				cancelOperation()
				return
			}
		}
	}()
	defer func() { cancelMonitor(); <-done }()
	return action(operation, store, retention, docker)
}
