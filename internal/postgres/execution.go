package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/runstate"
)

// Heartbeat renews only a live claim and returns the same locked Run's status,
// including cancellation. Cleanup needs heartbeats even after a terminal decision.
func (s *Resources) Heartbeat(ctx context.Context, id uuid.UUID) (status runstate.Status, err error) {
	err = s.WithRun(ctx, id, func(r *RunResources) error {
		status = r.status
		return r.RenewLease()
	})
	return status, err
}

// RenewLease is also used before committing a fenced allocation. The Run lock
// has prevented takeover throughout the operation, even if its old expiry passed.
func (r *RunResources) RenewLease() error {
	if err := r.guard(); err != nil {
		return err
	}
	_, err := r.tx.Exec(r.ctx, `UPDATE runs SET lease_expires_at=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, r.id, time.Now().UTC().Add(LeaseDuration))
	return err
}

// ProvisioningContext binds the claimed attempt to its Task, Agent and
// Repository. The Backend is the Run's selection, not the Agent's current one.
type ProvisioningContext struct {
	RunID, WorkspaceID, RepositoryID uuid.UUID
	CloneURL, BaseRef, Backend       string
	TaskTitle, TaskDescription       string
	Instructions                     string
	BackendConfig                    json.RawMessage
}

func (r *RunResources) ProvisioningContext() (ProvisioningContext, error) {
	if err := r.guard(); err != nil {
		return ProvisioningContext{}, err
	}
	if r.status != runstate.Provisioning {
		return ProvisioningContext{}, ErrResourceState
	}
	inputs := ProvisioningContext{RunID: r.id, WorkspaceID: WorkspaceID(r.id), Backend: r.backend}
	err := r.tx.QueryRow(r.ctx, `SELECT repositories.id,repositories.clone_url,repositories.default_branch,
		tasks.title,tasks.description,agents.instructions,agents.backend_config
		FROM runs JOIN tasks ON tasks.id=runs.task_id JOIN agents ON agents.id=runs.agent_id
		JOIN repositories ON repositories.id=tasks.repository_id WHERE runs.id=$1`, r.id).
		Scan(&inputs.RepositoryID, &inputs.CloneURL, &inputs.BaseRef, &inputs.TaskTitle,
			&inputs.TaskDescription, &inputs.Instructions, &inputs.BackendConfig)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisioningContext{}, errors.New("Run Task has no Repository to provision")
	}
	return inputs, err
}

func (r *RunResources) BeginFinalizing() error {
	return r.executionTransition(runstate.Finalizing, nil)
}

// AppendBackendEvent commits one already-validated fake protocol record. The
// decoder owns protocol validation; persistence owns Run state and ordering.
// A terminal decision closes this output stream without replacing earlier facts.
func (r *RunResources) AppendBackendEvent(kind string, data, raw map[string]any) error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.status != runstate.Running || data == nil || raw == nil {
		return ErrResourceState
	}
	switch kind {
	case "agent.message.delta", "agent.message.completed", "usage.updated":
		return r.eventWithRaw(kind, "fake-container-workload", data, raw)
	default:
		return ErrResourceState
	}
}

// Complete and its Event are one transaction. Only the finalizing attempt can
// succeed; an API cancellation or recovery decision cannot be overwritten.
func (r *RunResources) Complete() error {
	if err := r.executionTransition(runstate.Succeeded, nil); err != nil {
		return err
	}
	return r.event("run.completed", "worker", map[string]any{})
}

// RecordFailure is also the supervisor's final reconciliation. Terminal
// decisions are immutable; cleanup errors must use RecordCleanupFailure instead.
func (r *RunResources) RecordFailure(message string, raw map[string]any) error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.status.Terminal() {
		return nil
	}
	message = strings.ReplaceAll(strings.ToValidUTF8(message, "�"), "\x00", "�")
	if runes := []rune(message); len(runes) > 4000 {
		message = string(runes[:4000])
	}
	if err := r.executionTransition(runstate.Failed, &message); err != nil {
		return err
	}
	return r.eventWithRaw("run.failed", "worker", map[string]any{"error": message}, raw)
}

// FailProvisioning retains a started container's immutable identity even when
// its ready handoff failed. Identity, Workspace failure and Run failure commit
// together; an API cancellation must instead be preserved by RecordFailure.
func (r *RunResources) FailProvisioning(message, containerID string) error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.status != runstate.Provisioning {
		return ErrResourceState
	}
	if containerID != "" {
		if _, err := r.RecordContainer(containerID); err != nil {
			return err
		}
	}
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if w != nil && (w.Status == "pending" || w.Status == "ready") {
		if err := r.workspaceStatus(w, "failed", "worker"); err != nil {
			return err
		}
	}
	return r.RecordFailure(message, nil)
}

func (r *RunResources) executionTransition(target runstate.Status, message *string) error {
	if err := r.guard(); err != nil {
		return err
	}
	if err := runstate.Validate(r.status, target); err != nil {
		return ErrResourceState
	}
	_, err := r.tx.Exec(r.ctx, `UPDATE runs SET status=$2,error=$3,
		finished_at=CASE WHEN $4 THEN clock_timestamp() ELSE finished_at END,
		updated_at=CURRENT_TIMESTAMP WHERE id=$1`, r.id, target, message, target.Terminal())
	if err == nil {
		r.status = target
	}
	return err
}

// ReleaseClaim is the final successful-cleanup write, never a replacement for
// cleanup. An active Run or an unreleased Workspace must retain recovery ownership.
func (r *RunResources) ReleaseClaim() error {
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if !r.status.Terminal() || w != nil && w.Status != "released" {
		return ErrResourceState
	}
	_, err = r.tx.Exec(r.ctx, `UPDATE runs SET worker_id=NULL,lease_expires_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, r.id)
	return err
}
