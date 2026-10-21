package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/runstate"
)

type WorkFinishInput struct {
	Credential  WorkCredential
	OperationID uuid.UUID
	Outcome     runstate.Status
	Summary     string
	Report      *completion.Report `json:"report,omitempty"`
}
type WorkAdminInput struct {
	TaskID, ExpectedSessionID, OperationID uuid.UUID
	Reason                                 string
}
type WorkReceipt struct {
	ID                   uuid.UUID  `json:"id"`
	SessionID            uuid.UUID  `json:"session_id"`
	OperationID          uuid.UUID  `json:"operation_id"`
	Kind                 string     `json:"kind"`
	Actor                string     `json:"actor"`
	Reason               string     `json:"reason"`
	At                   time.Time  `json:"at"`
	ReplacementSessionID *uuid.UUID `json:"replacement_session_id"`
}
type WorkChangeResult struct {
	Session        WorkSessionView  `json:"session"`
	Receipt        WorkReceipt      `json:"receipt"`
	Replacement    *WorkSessionView `json:"replacement"`
	PendingCleanup bool             `json:"pending_cleanup"`
}

const workReceiptColumns = `id,session_id,operation_id,kind,actor,reason,at,replacement_session_id`

func scanWorkReceipt(row pgx.Row, receipt *WorkReceipt, extra ...any) error {
	fields := []any{&receipt.ID, &receipt.SessionID, &receipt.OperationID, &receipt.Kind, &receipt.Actor, &receipt.Reason, &receipt.At, &receipt.ReplacementSessionID}
	return row.Scan(append(fields, extra...)...)
}
func WorkSessionReceipts(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) ([]WorkReceipt, error) {
	rows, err := tx.Query(ctx, `SELECT `+workReceiptColumns+` FROM work_session_receipts WHERE session_id=$1 ORDER BY at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := []WorkReceipt{}
	for rows.Next() {
		var receipt WorkReceipt
		if err := scanWorkReceipt(rows, &receipt); err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}
func replayWorkReceipt(ctx context.Context, tx pgx.Tx, session, op uuid.UUID, digest []byte, now time.Time) (WorkChangeResult, error) {
	var receipt WorkReceipt
	var saved []byte
	err := scanWorkReceipt(tx.QueryRow(ctx, `SELECT `+workReceiptColumns+`,input_digest FROM work_session_receipts WHERE session_id=$1 AND operation_id=$2`, session, op), &receipt, &saved)
	if err != nil {
		return WorkChangeResult{}, err
	}
	if !bytes.Equal(saved, digest) {
		return WorkChangeResult{}, ErrWorkReplayConflict
	}
	return workChangeResult(ctx, tx, receipt, now)
}
func workChangeResult(ctx context.Context, tx pgx.Tx, receipt WorkReceipt, now time.Time) (WorkChangeResult, error) {
	out := WorkChangeResult{Receipt: receipt}
	var err error
	out.Session, err = InspectWorkSession(ctx, tx, receipt.SessionID, now)
	if err != nil {
		return out, err
	}
	if out.Session.Kind == "native" && out.Session.ReleasedAt == nil {
		out.PendingCleanup, err = nativeCleanupPending(ctx, tx, *out.Session.RunID)
		if err != nil {
			return out, err
		}
	}
	if receipt.ReplacementSessionID != nil {
		replacement, err := InspectWorkSession(ctx, tx, *receipt.ReplacementSessionID, now)
		if err != nil {
			return out, err
		}
		out.Replacement = &replacement
	}
	return out, nil
}
func insertWorkReceipt(ctx context.Context, tx pgx.Tx, receipt WorkReceipt, digest []byte, now time.Time) (WorkChangeResult, error) {
	receipt.ID = uuid.New()
	_, err := tx.Exec(ctx, `INSERT INTO work_session_receipts(id,session_id,operation_id,kind,actor,reason,at,replacement_session_id,input_digest)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, receipt.ID, receipt.SessionID, receipt.OperationID, receipt.Kind, receipt.Actor, receipt.Reason, receipt.At, receipt.ReplacementSessionID, digest)
	if err != nil {
		return WorkChangeResult{}, err
	}
	// Read the persisted timestamp, so even nanosecond inputs replay identically.
	return replayWorkReceipt(ctx, tx, receipt.SessionID, receipt.OperationID, digest, now)
}

func FinishExternalWork(ctx context.Context, tx pgx.Tx, in WorkFinishInput, now time.Time) (WorkChangeResult, error) {
	if in.OperationID == uuid.Nil || !in.Outcome.Terminal() || !validWorkText(in.Summary, 4000) {
		return WorkChangeResult{}, ErrWorkInput
	}
	row, err := readWorkSession(ctx, tx, in.Credential.SessionID, false, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkChangeResult{}, ErrWorkCredential
	}
	if err != nil {
		return WorkChangeResult{}, err
	}
	if err = authenticateWork(row, in.Credential); err != nil {
		return WorkChangeResult{}, err
	}
	digest, err := workDigest([]any{"finish", in})
	if err != nil {
		return WorkChangeResult{}, err
	}
	if replay, err := replayWorkReceipt(ctx, tx, row.view.ID, in.OperationID, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	if _, err = lockPlanningTask(ctx, tx, row.view.TaskID); err != nil {
		return WorkChangeResult{}, err
	}
	row, err = readWorkSession(ctx, tx, in.Credential.SessionID, true, now)
	if err != nil {
		return WorkChangeResult{}, err
	}
	if err = authenticateWork(row, in.Credential); err != nil {
		return WorkChangeResult{}, err
	}
	if replay, err := replayWorkReceipt(ctx, tx, row.view.ID, in.OperationID, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	if row.view.ReleasedAt != nil {
		return WorkChangeResult{}, ErrWorkRevoked
	}
	view, err := InspectTaskCompletion(ctx, tx, row.view.TaskID, now)
	if err != nil {
		return WorkChangeResult{}, err
	}
	if view.CurrentSessionID != row.view.ID {
		return WorkChangeResult{}, ErrWorkRevoked
	}
	if in.Outcome == runstate.Succeeded && in.Report == nil {
		return WorkChangeResult{}, &CompletionConflict{Blockers: []completion.Blocker{{Code: "report_required", Message: "Successful completion needs a report", Action: "Submit a result and every required evidence item"}}}
	}
	summary := in.Summary
	if in.Report != nil {
		if view.Contract == nil {
			return WorkChangeResult{}, ErrWorkInput
		}
		if err = completion.ValidateReport(view.Contract.Policy, *in.Report); err != nil {
			return WorkChangeResult{}, err
		}
		if summary != "" && summary != in.Report.Result {
			return WorkChangeResult{}, ErrWorkInput
		}
		summary = in.Report.Result
		raw, err := json.Marshal(in.Report)
		if err != nil {
			return WorkChangeResult{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE task_completions SET report=$2 WHERE session_id=$1`, row.view.ID, raw); err != nil {
			return WorkChangeResult{}, err
		}
	}
	at := workTime(now, row.view)
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET outcome=$2,finished_at=$3,released_at=$3,release_reason='external finish' WHERE id=$1`, row.view.ID, in.Outcome, at); err != nil {
		return WorkChangeResult{}, err
	}
	if err = markTaskCompletionPending(ctx, tx, row.view.ID); err != nil {
		return WorkChangeResult{}, err
	}
	if _, err = ReconcileTaskCompletion(ctx, tx, row.view.ID, at); err != nil {
		return WorkChangeResult{}, err
	}
	return insertWorkReceipt(ctx, tx, WorkReceipt{SessionID: row.view.ID, OperationID: in.OperationID, Kind: "finished", Actor: "external", Reason: summary, At: at}, digest, now)
}

// Administration is authorized by the private control boundary, never by client metadata.
func lockWorkAdmin(ctx context.Context, tx pgx.Tx, in WorkAdminInput, now time.Time) (planningTask, workSessionRow, error) {
	if in.TaskID == uuid.Nil || in.ExpectedSessionID == uuid.Nil || in.OperationID == uuid.Nil || strings.TrimSpace(in.Reason) == "" || !validWorkText(in.Reason, 4000) {
		return planningTask{}, workSessionRow{}, ErrWorkInput
	}
	task, err := lockPlanningTask(ctx, tx, in.TaskID)
	if err != nil {
		return task, workSessionRow{}, err
	}
	var runID *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT run_id FROM work_sessions WHERE id=$1 AND task_id=$2`, in.ExpectedSessionID, in.TaskID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return task, workSessionRow{}, ErrWorkRevoked
	}
	if err != nil {
		return task, workSessionRow{}, err
	}
	if runID != nil {
		var locked uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT id FROM runs WHERE id=$1 FOR UPDATE`, *runID).Scan(&locked); err != nil {
			return task, workSessionRow{}, err
		}
	}
	row, err := readWorkSession(ctx, tx, in.ExpectedSessionID, true, now)
	return task, row, err
}

func ReleaseTaskWork(ctx context.Context, tx pgx.Tx, in WorkAdminInput, now time.Time) (WorkChangeResult, error) {
	_, row, err := lockWorkAdmin(ctx, tx, in, now)
	if err != nil {
		return WorkChangeResult{}, err
	}
	digest, err := workDigest([]any{"release", in})
	if err != nil {
		return WorkChangeResult{}, err
	}
	if replay, err := replayWorkReceipt(ctx, tx, row.view.ID, in.OperationID, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	if row.view.ReleasedAt != nil {
		return WorkChangeResult{}, ErrWorkRevoked
	}
	if row.view.Kind != "external" {
		return releaseNativeTaskWork(ctx, tx, in, row, digest, now)
	}
	at := workTime(now, row.view)
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET released_at=$2,release_reason=$3 WHERE id=$1`, row.view.ID, at, in.Reason); err != nil {
		return WorkChangeResult{}, err
	}
	return insertWorkReceipt(ctx, tx, WorkReceipt{SessionID: row.view.ID, OperationID: in.OperationID, Kind: "released", Actor: "user", Reason: in.Reason, At: at}, digest, now)
}

// Revocation fences Circular reports; it cannot stop an external client's file edits.
func ReassignExternalWork(ctx context.Context, tx pgx.Tx, in WorkAdminInput, replacement ExternalWorkClaim, now time.Time) (WorkChangeResult, error) {
	if err := validateExternalClaim(replacement); err != nil {
		return WorkChangeResult{}, err
	}
	if replacement.TaskID != in.TaskID || replacement.SessionID == in.ExpectedSessionID {
		return WorkChangeResult{}, ErrWorkInput
	}
	task, row, err := lockWorkAdmin(ctx, tx, in, now)
	if err != nil {
		return WorkChangeResult{}, err
	}
	digest, err := workDigest([]any{"reassign", in, replacement, credentialDigest(replacement.Secret)})
	if err != nil {
		return WorkChangeResult{}, err
	}
	if replay, err := replayWorkReceipt(ctx, tx, row.view.ID, in.OperationID, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	if row.view.ReleasedAt != nil {
		return WorkChangeResult{}, ErrWorkRevoked
	}
	if row.view.Kind != "external" || bytes.Equal(row.secretDigest, credentialDigest(replacement.Secret)) {
		return WorkChangeResult{}, ErrWorkInput
	}
	if replacement.ExpectedRevision != task.revision {
		return WorkChangeResult{}, ErrTaskPlanningConflict
	}
	prerequisites, err := eligibleWorkTask(ctx, tx, task, "open", "ready", "in_progress")
	if err != nil {
		return WorkChangeResult{}, err
	}
	admission, err := workDigest(replacement)
	if err != nil {
		return WorkChangeResult{}, err
	}
	at := workTime(now, row.view)
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET released_at=$2,release_reason=$3 WHERE id=$1`, row.view.ID, at, in.Reason); err != nil {
		return WorkChangeResult{}, err
	}
	created, err := insertExternalWork(ctx, tx, replacement, task, prerequisites, admission, at)
	if err != nil {
		return WorkChangeResult{}, err
	}
	if !created {
		return WorkChangeResult{}, ErrWorkReplayConflict
	}
	if err = moveClaimedTask(ctx, tx, task); err != nil {
		return WorkChangeResult{}, err
	}
	return insertWorkReceipt(ctx, tx, WorkReceipt{SessionID: row.view.ID, OperationID: in.OperationID, Kind: "reassigned", Actor: "user", Reason: in.Reason, At: at, ReplacementSessionID: &replacement.SessionID}, digest, now)
}
