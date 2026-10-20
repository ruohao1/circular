package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/worker"
)

func settleDelegation(ctx context.Context, tx pgx.Tx, parent uuid.UUID, status runstate.Status) error {
	if !status.Terminal() {
		return nil
	}
	lifecycle := "completed"
	if status == runstate.Cancelled {
		lifecycle = "cancelled"
	}
	_, err := tx.Exec(ctx, `UPDATE delegations SET status=$2,updated_at=CURRENT_TIMESTAMP WHERE parent_run_id=$1 AND request_key IS NOT NULL AND status IN ('waiting','continuing')`, parent, lifecycle)
	return err
}

func claimOrphanedDelegation(ctx context.Context, tx pgx.Tx, owner string, now time.Time) (*worker.Claim, error) {
	var id uuid.UUID
	var invocation int
	var status runstate.Status
	err := tx.QueryRow(ctx, `SELECT p.id,p.invocation,p.status FROM runs p JOIN workspaces w ON w.run_id=p.id
		WHERE p.status IN ('succeeded','failed','cancelled') AND p.worker_id IS NULL AND p.recovery_attempts<$1
		AND EXISTS(SELECT 1 FROM work_sessions s WHERE s.run_id=p.id AND s.task_id=p.task_id AND s.released_at IS NULL)
		AND w.status<>'released' AND EXISTS(SELECT 1 FROM delegations d WHERE d.parent_run_id=p.id AND d.request_key IS NOT NULL)
		ORDER BY p.created_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1`, MaxRecoveries).Scan(&id, &invocation, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE runs SET worker_id=$2,claimed_at=$3,lease_expires_at=$4,recovery_attempts=recovery_attempts+1,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id, owner, now, now.Add(LeaseDuration)); err != nil {
		return nil, err
	}
	if err = settleDelegation(ctx, tx, id, status); err != nil {
		return nil, err
	}
	return &worker.Claim{RunID: id, Invocation: invocation, Recovery: true}, nil
}

// Only the parent is locked. Released child output is immutable, and child
// cleanup never waits for its parent while holding the child lock.
func claimContinuation(ctx context.Context, tx pgx.Tx, owner string, now time.Time) (*worker.Claim, error) {
	var id, delegation uuid.UUID
	var result delegations.Result
	err := tx.QueryRow(ctx, `SELECT p.id,d.id,c.id,c.status,COALESCE(c.error,'')
		FROM runs p JOIN delegations d ON d.parent_run_id=p.id AND d.request_key IS NOT NULL
		JOIN runs c ON c.id=d.child_run_id LEFT JOIN workspaces w ON w.run_id=c.id
		WHERE p.status='waiting_for_input' AND p.invocation=1 AND p.worker_id IS NULL AND d.status='waiting'
		AND EXISTS(SELECT 1 FROM work_sessions s WHERE s.run_id=p.id AND s.task_id=p.task_id AND s.released_at IS NULL)
		AND c.status IN ('succeeded','failed','cancelled') AND c.worker_id IS NULL AND (w.id IS NULL OR w.status='released')
		ORDER BY p.created_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1`).Scan(&id, &delegation, &result.ChildRunID, &result.Status, &result.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT kind,uri,metadata->>'sha256',(metadata->>'size_bytes')::bigint FROM artifacts WHERE run_id=$1 AND kind IN ('diff','delegation_report')`, result.ChildRunID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var content artifacts.Content
		if err = rows.Scan(&kind, &content.URI, &content.SHA256, &content.SizeBytes); err != nil {
			rows.Close()
			return nil, err
		}
		if kind == "diff" {
			if result.Patch != nil {
				rows.Close()
				return nil, ErrResourceConflict
			}
			result.Patch = &content
		} else {
			if result.Report != nil {
				rows.Close()
				return nil, ErrResourceConflict
			}
			result.Report = &content
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if err = result.Validate(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `UPDATE delegations SET status='continuing',result=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, delegation, raw); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE runs SET invocation=2,worker_id=$2,claimed_at=$3,lease_expires_at=$4,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id, owner, now, now.Add(LeaseDuration)); err != nil {
		return nil, err
	}
	return &worker.Claim{RunID: id, Invocation: 2, Continuation: true}, nil
}

func (r *RunResources) ContinuationContext() (ProvisioningContext, delegations.Checkpoint, delegations.Result, error) {
	var input ProvisioningContext
	var checkpoint delegations.Checkpoint
	var result delegations.Result
	if err := r.guard(); err != nil {
		return input, checkpoint, result, err
	}
	if r.invocation != 2 || r.kind != runstate.Coding || r.status != runstate.WaitingForInput {
		return input, checkpoint, result, ErrResourceState
	}
	var source, outcome []byte
	var child uuid.UUID
	if err := r.tx.QueryRow(r.ctx, `SELECT checkpoint,result,child_run_id FROM delegations WHERE parent_run_id=$1 AND request_key IS NOT NULL AND status='continuing'`, r.id).Scan(&source, &outcome, &child); err != nil {
		return input, checkpoint, result, err
	}
	if json.Unmarshal(source, &checkpoint) != nil || json.Unmarshal(outcome, &result) != nil || checkpoint.Validate(r.id) != nil || result.Validate() != nil || result.ChildRunID != child {
		return input, checkpoint, result, ErrResourceConflict
	}
	input, err := readInvocation(r.ctx, r.tx, r.id, 1)
	if err != nil {
		return input, checkpoint, result, err
	}
	_, digest, err := sealInvocation(input)
	if err != nil || checkpoint.InputSHA256 != digest || input.SourceCheckpoint != nil {
		return input, checkpoint, result, ErrResourceConflict
	}
	input.Invocation = 2
	input.DelegationTargets = nil
	input.DelegationContextSHA256, err = delegations.ContextFingerprint(delegations.Context{RunID: r.id, Checkpoint: checkpoint, Result: result})
	if err == nil {
		err = SaveInvocationInput(r.ctx, r.tx, input)
	}
	return input, checkpoint, result, err
}

func (r *RunResources) ResumeWorkspace() error {
	if err := r.guard(); err != nil {
		return err
	}
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if r.invocation != 2 || r.status != runstate.WaitingForInput || w == nil || w.Status != "suspended" || w.ContainerID != nil {
		return ErrResourceState
	}
	if _, err = r.InvocationInput(); err != nil {
		return err
	}
	_, err = r.tx.Exec(r.ctx, `UPDATE workspaces SET status='pending',updated_at=CURRENT_TIMESTAMP WHERE id=$1`, w.ID)
	if err != nil {
		return err
	}
	return r.event("workspace.provisioning", "worker", map[string]any{"status": "pending", "workspace_id": w.ID.String(), "invocation": 2})
}

func (r *RunResources) MarkContinued() error {
	if err := r.guard(); err != nil {
		return err
	}
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if r.invocation != 2 || r.status != runstate.WaitingForInput || w == nil || w.Status != "pending" || w.ContainerID == nil {
		return ErrResourceState
	}
	if err = r.workspaceStatus(w, "ready", "worker"); err != nil {
		return err
	}
	if err = r.executionTransition(runstate.Running, nil); err != nil {
		return err
	}
	return r.event("run.continued", "worker", map[string]any{"invocation": 2})
}

func (r *RunResources) DelegationReport() (string, error) {
	if err := r.guard(); err != nil {
		return "", err
	}
	if !r.status.Terminal() {
		return "", ErrResourceState
	}
	var report string
	err := r.tx.QueryRow(r.ctx, `SELECT data->>'content' FROM events WHERE run_id=$1 AND type='agent.message.completed'
		AND EXISTS(SELECT 1 FROM delegations WHERE child_run_id=$1 AND request_key IS NOT NULL) ORDER BY sequence DESC LIMIT 1`, r.id).Scan(&report)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return report, err
}

func (r *RunResources) PersistDelegationReport(worktree string, content artifacts.Content) error {
	if err := r.validContent(content, "delegation-report.txt"); err != nil {
		return err
	}
	a := artifacts.Record{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("io.circular.delegation_report:"+r.id.String())), RunID: r.id, Kind: "delegation_report", URI: content.URI, Metadata: map[string]any{"media_type": "text/plain", "size_bytes": content.SizeBytes, "sha256": content.SHA256}}
	return r.persistArtifact(worktree, a, "worker-cleanup")
}
