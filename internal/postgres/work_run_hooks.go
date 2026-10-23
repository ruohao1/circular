package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/runstate"
)

// All native hooks run under the Run lock; none takes a planning lock.
func nativeWorkRow(ctx context.Context, tx pgx.Tx, runID uuid.UUID, now time.Time) (workSessionRow, error) {
	var kind runstate.Kind
	var id *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT r.kind,s.id FROM runs r LEFT JOIN work_sessions s ON s.run_id=r.id AND s.task_id=r.task_id AND s.kind='native' WHERE r.id=$1`, runID).Scan(&kind, &id)
	if err != nil || kind == runstate.PRReview {
		return workSessionRow{}, err
	}
	if id == nil {
		return workSessionRow{}, ErrWorkRevoked
	}
	return readWorkSession(ctx, tx, *id, true, now)
}
func requireNativeWork(ctx context.Context, tx pgx.Tx, runID uuid.UUID) error {
	row, err := nativeWorkRow(ctx, tx, runID, time.Now().UTC())
	if err != nil {
		return err
	}
	if row.view.ReleasedAt != nil {
		return ErrWorkRevoked
	}
	return nil
}
func startNativeWork(ctx context.Context, tx pgx.Tx, runID uuid.UUID, started time.Time) error {
	row, err := nativeWorkRow(ctx, tx, runID, started)
	if err != nil || row.view.ID == uuid.Nil {
		return err
	}
	if row.view.ReleasedAt != nil {
		return ErrWorkRevoked
	}
	_, err = tx.Exec(ctx, `UPDATE work_sessions SET started_at=COALESCE(started_at,$2) WHERE id=$1`, row.view.ID, started)
	return err
}
func contactNativeWork(ctx context.Context, tx pgx.Tx, runID uuid.UUID, now time.Time) error {
	row, err := nativeWorkRow(ctx, tx, runID, now)
	if err != nil || row.view.ID == uuid.Nil {
		return err
	}
	if row.view.ReleasedAt != nil {
		return ErrWorkRevoked
	}
	if row.view.Outcome != nil || row.view.NeedsUserResolution {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE work_sessions SET last_contact_at=$2,heartbeat_sequence=heartbeat_sequence+1 WHERE id=$1`, row.view.ID, workTime(now, row.view))
	return err
}
func nativeReceipt(ctx context.Context, tx pgx.Tx, view WorkSessionView, kind, reason string, at time.Time) error {
	op := uuid.NewSHA1(uuid.NameSpaceURL, []byte("io.circular.work:"+view.ID.String()+":"+kind))
	digest, err := workDigest([]any{view.ID, kind, view.Outcome, reason})
	if err != nil {
		return err
	}
	_, err = insertWorkReceipt(ctx, tx, WorkReceipt{SessionID: view.ID, OperationID: op, Kind: kind, Actor: "system", Reason: reason, At: at}, digest, at)
	return err
}
func finishNativeWork(ctx context.Context, tx pgx.Tx, runID uuid.UUID, outcome runstate.Status, now time.Time) error {
	if !outcome.Terminal() {
		return ErrWorkInput
	}
	row, err := nativeWorkRow(ctx, tx, runID, now)
	if err != nil || row.view.ID == uuid.Nil {
		return err
	}
	if row.view.Outcome != nil {
		if *row.view.Outcome != outcome {
			return ErrResourceConflict
		}
		return nil
	}
	if row.view.ReleasedAt != nil {
		return ErrWorkRevoked
	}
	at := workTime(now, row.view)
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET outcome=$2,finished_at=$3 WHERE id=$1`, row.view.ID, outcome, at); err != nil {
		return err
	}
	if err = markTaskCompletionPending(ctx, tx, row.view.ID); err != nil {
		return err
	}
	row.view.Outcome = &outcome
	if err = nativeReceipt(ctx, tx, row.view, "finished", "native Run "+string(outcome), at); err != nil {
		return err
	}
	return releaseNativeWorkAfterCleanup(ctx, tx, runID, at)
}
func markNativeWorkRecovery(ctx context.Context, tx pgx.Tx, runID uuid.UUID, now time.Time) error {
	row, err := nativeWorkRow(ctx, tx, runID, now)
	if err != nil || row.view.ID == uuid.Nil {
		return err
	}
	if row.view.ReleasedAt != nil {
		return ErrWorkRevoked
	}
	_, err = tx.Exec(ctx, `UPDATE work_sessions SET needs_user_resolution=true WHERE id=$1`, row.view.ID)
	return err
}
func nativeCleanupPending(ctx context.Context, tx pgx.Tx, runID uuid.UUID) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `SELECT worker_id IS NOT NULL OR lease_expires_at IS NOT NULL OR
		EXISTS(SELECT 1 FROM workspaces w WHERE w.run_id=r.id AND w.status<>'released') FROM runs r WHERE id=$1`, runID).Scan(&pending)
	return pending, err
}
func releaseNativeWorkAfterCleanup(ctx context.Context, tx pgx.Tx, runID uuid.UUID, now time.Time) error {
	row, err := nativeWorkRow(ctx, tx, runID, now)
	if err != nil || row.view.ID == uuid.Nil {
		return err
	}
	if row.view.ReleasedAt != nil || row.view.NeedsUserResolution {
		return nil
	}
	pending, err := nativeCleanupPending(ctx, tx, runID)
	if err != nil || pending {
		return err
	}
	if row.view.Outcome == nil {
		return ErrResourceState
	}
	at := workTime(now, row.view)
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET released_at=$2,release_reason='native cleanup complete' WHERE id=$1`, row.view.ID, at); err != nil {
		return err
	}
	if err = markTaskCompletionPending(ctx, tx, row.view.ID); err != nil {
		return err
	}
	return nativeReceipt(ctx, tx, row.view, "released", "native cleanup complete", at)
}

func releaseNativeTaskWork(ctx context.Context, tx pgx.Tx, in WorkAdminInput, row workSessionRow, digest []byte, now time.Time) (WorkChangeResult, error) {
	runID := *row.view.RunID
	var status runstate.Status
	var expired bool
	err := tx.QueryRow(ctx, `SELECT status,worker_id IS NOT NULL AND (lease_expires_at IS NULL OR lease_expires_at<=$2) FROM runs WHERE id=$1`, runID, now).Scan(&status, &expired)
	if err != nil {
		return WorkChangeResult{}, err
	}
	if expired {
		if err = markNativeWorkRecovery(ctx, tx, runID, now); err != nil {
			return WorkChangeResult{}, err
		}
	}
	if !status.Terminal() {
		if err = CancelRun(ctx, tx, runID, "api"); err != nil {
			return WorkChangeResult{}, err
		}
	}
	row, err = readWorkSession(ctx, tx, row.view.ID, true, now)
	if err != nil {
		return WorkChangeResult{}, err
	}
	pending, err := nativeCleanupPending(ctx, tx, runID)
	if err != nil {
		return WorkChangeResult{}, err
	}
	at := workTime(now, row.view)
	kind := "cancellation_requested"
	if !pending {
		kind = "released"
		if row.view.ReleasedAt == nil {
			if row.view.Outcome == nil {
				return WorkChangeResult{}, ErrResourceState
			}
			if _, err = tx.Exec(ctx, `UPDATE work_sessions SET released_at=$2,release_reason=$3 WHERE id=$1`, row.view.ID, at, in.Reason); err != nil {
				return WorkChangeResult{}, err
			}
		}
	}
	return insertWorkReceipt(ctx, tx, WorkReceipt{SessionID: row.view.ID, OperationID: in.OperationID, Kind: kind, Actor: "user", Reason: in.Reason, At: at}, digest, now)
}

func markTaskCompletionPending(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE task_completions SET reconciliation_pending=true WHERE session_id=$1 AND applied_at IS NULL`, sessionID)
	return err
}
