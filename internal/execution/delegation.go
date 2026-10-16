package execution

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
	"time"
)

var errDelegationSuspended = errors.New("parent suspended for delegation")

func (s *Supervisor) handoff(ctx context.Context, id uuid.UUID, request delegations.Request) error {
	checkpoint, err := s.retention.Checkpoint(ctx, id)
	if err != nil {
		return executionFailure("could not verify delegation checkpoint", err)
	}
	err = s.store.WithRun(ctx, id, func(r *postgres.RunResources) error {
		state, err := r.State()
		if err != nil {
			return err
		}
		if state.Status != runstate.Running || state.Workspace == nil || state.Workspace.ContainerID == nil {
			return postgres.ErrResourceState
		}
		return s.docker.Release(ctx, id, *state.Workspace.ContainerID)
	})
	if err != nil {
		return executionFailure("could not release the parent invocation for delegation", err)
	}
	_, err = s.store.SuspendForDelegation(ctx, id, request, checkpoint)
	if err != nil && !errors.Is(err, postgres.ErrDelegationPolicy) && !errors.Is(err, postgres.ErrDelegationConflict) {
		// A lost acknowledgement may have committed admission and released the claim.
		replay, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err = s.store.SuspendForDelegation(replay, id, request, checkpoint)
	}
	if err != nil {
		return executionFailure("delegation request could not be admitted", err)
	}
	return nil
}
