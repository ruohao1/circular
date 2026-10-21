package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func validateExternalClaim(in ExternalWorkClaim) error {
	if in.SessionID == uuid.Nil || in.TaskID == uuid.Nil || in.ExpectedRevision < 1 || in.ExpectedRevision > maxPlanningRevision ||
		len(in.Secret) != 32 || strings.TrimSpace(in.ClientName) == "" || !validWorkText(in.ClientName, 80) || !validWorkText(in.ClientVersion, 80) {
		return ErrWorkInput
	}
	return nil
}
func replayExternalClaim(ctx context.Context, tx pgx.Tx, in ExternalWorkClaim, digest []byte, now time.Time) (WorkClaimResult, error) {
	row, err := readWorkSession(ctx, tx, in.SessionID, false, now)
	if err != nil {
		return WorkClaimResult{}, err
	}
	if err = authenticateWork(row, WorkCredential{in.SessionID, in.Secret}); err != nil {
		return WorkClaimResult{}, err
	}
	if !bytes.Equal(row.admissionDigest, digest) {
		return WorkClaimResult{}, ErrWorkReplayConflict
	}
	return WorkClaimResult{Session: row.view}, nil
}

func ClaimExternalWork(ctx context.Context, tx pgx.Tx, in ExternalWorkClaim, now time.Time) (WorkClaimResult, error) {
	if err := validateExternalClaim(in); err != nil {
		return WorkClaimResult{}, err
	}
	digest, err := workDigest(in)
	if err != nil {
		return WorkClaimResult{}, err
	}
	if replay, err := replayExternalClaim(ctx, tx, in, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	task, err := lockPlanningTask(ctx, tx, in.TaskID)
	if err != nil {
		return WorkClaimResult{}, err
	}
	// Recheck after the planning lock: another claim may have committed while waiting.
	if replay, err := replayExternalClaim(ctx, tx, in, digest, now); !errors.Is(err, pgx.ErrNoRows) {
		return replay, err
	}
	if owner, err := CurrentTaskWork(ctx, tx, task.id, now); err != nil {
		return WorkClaimResult{}, err
	} else if owner != nil {
		return WorkClaimResult{}, &WorkOwnershipConflict{Owner: *owner}
	}
	if task.revision != in.ExpectedRevision {
		return WorkClaimResult{}, ErrTaskPlanningConflict
	}
	prerequisites, err := eligibleWorkTask(ctx, tx, task, "ready")
	if err != nil {
		return WorkClaimResult{}, err
	}
	if _, err = freezeTaskCompletion(ctx, tx, task); err != nil {
		return WorkClaimResult{}, err
	}
	created, err := insertExternalWork(ctx, tx, in, task, prerequisites, digest, now)
	if err != nil {
		return WorkClaimResult{}, err
	}
	if !created {
		return replayExternalClaim(ctx, tx, in, digest, now)
	}
	if err = moveClaimedTask(ctx, tx, task); err != nil {
		return WorkClaimResult{}, err
	}
	view, err := InspectWorkSession(ctx, tx, in.SessionID, now)
	return WorkClaimResult{Session: view, Created: true}, err
}

func eligibleWorkTask(ctx context.Context, tx pgx.Tx, task planningTask, stages ...string) (json.RawMessage, error) {
	invalid := &WorkIneligibleError{Reasons: []string{}, Blockers: []uuid.UUID{}}
	if !slices.Contains(stages, task.status) {
		invalid.Reasons = append(invalid.Reasons, "stage must be "+strings.Join(stages, " or "))
	}
	if strings.TrimSpace(task.title) == "" {
		invalid.Reasons = append(invalid.Reasons, "title is required")
	}
	if strings.TrimSpace(task.description) == "" {
		invalid.Reasons = append(invalid.Reasons, "description is required")
	}
	var repository bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE id=$1 AND project_id=$2)`, task.repository, task.project).Scan(&repository); err != nil {
		return nil, err
	}
	if !repository {
		invalid.Reasons = append(invalid.Reasons, "a Repository in this Project is required")
	}
	rows, err := tx.Query(ctx, `SELECT t.id,t.revision,t.status FROM task_dependencies d JOIN tasks t ON t.id=d.prerequisite_task_id WHERE d.task_id=$1 ORDER BY t.id`, task.id)
	if err != nil {
		return nil, err
	}
	type prerequisite struct {
		TaskID   uuid.UUID `json:"task_id"`
		Revision int64     `json:"revision"`
	}
	snapshot := []prerequisite{}
	for rows.Next() {
		var p prerequisite
		var status string
		if err := rows.Scan(&p.TaskID, &p.Revision, &status); err != nil {
			rows.Close()
			return nil, err
		}
		snapshot = append(snapshot, p)
		if status != "completed" {
			invalid.Blockers = append(invalid.Blockers, p.TaskID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(invalid.Blockers) > 0 {
		invalid.Reasons = append(invalid.Reasons, fmt.Sprintf("%d prerequisite Tasks are not completed", len(invalid.Blockers)))
	}
	if len(invalid.Reasons) > 0 {
		return nil, invalid
	}
	return json.Marshal(snapshot)
}

func insertExternalWork(ctx context.Context, tx pgx.Tx, in ExternalWorkClaim, task planningTask, prerequisites json.RawMessage, digest []byte, now time.Time) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO work_sessions(id,project_id,task_id,kind,secret_digest,admission_digest,client_name,client_version,admitted_revision,admitted_prerequisites,claimed_at,started_at,last_contact_at)
		VALUES($1,$2,$3,'external',$4,$5,$6,$7,$8,$9,$10,$10,$10) ON CONFLICT(id) DO NOTHING`,
		in.SessionID, task.project, task.id, credentialDigest(in.Secret), digest, in.ClientName, in.ClientVersion, task.revision, prerequisites, now.UTC())
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if err = recordCompletionSession(ctx, tx, task.id, in.SessionID); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO work_session_receipts(id,session_id,operation_id,kind,input_digest,actor,at) VALUES($1,$2,$2,'claimed',$3,'external',$4)`, uuid.New(), in.SessionID, digest, now.UTC())
	return err == nil, err
}
func moveClaimedTask(ctx context.Context, tx pgx.Tx, task planningTask) error {
	if task.status == "in_progress" {
		return nil
	}
	_, err := MoveTask(ctx, tx, task.id, TaskMoveInput{ExpectedRevision: task.revision, ExpectedBoardRevision: task.board, Status: "in_progress", Placement: "end"})
	return err
}

func claimNativeWork(ctx context.Context, tx pgx.Tx, task planningTask, runID uuid.UUID, now time.Time) (WorkSessionView, error) {
	prerequisites, err := eligibleWorkTask(ctx, tx, task, "open", "ready")
	if err != nil {
		return WorkSessionView{}, err
	}
	digest, err := workDigest([]uuid.UUID{task.id, runID})
	if err != nil {
		return WorkSessionView{}, err
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO work_sessions(id,project_id,task_id,kind,run_id,admission_digest,client_name,admitted_revision,admitted_prerequisites,claimed_at)
		VALUES($1,$2,$3,'native',$4,$5,'Circular',$6,$7,$8)`, id, task.project, task.id, runID, digest, task.revision, prerequisites, now)
	if err != nil {
		return WorkSessionView{}, err
	}
	if err = recordCompletionSession(ctx, tx, task.id, id); err != nil {
		return WorkSessionView{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_session_receipts(id,session_id,operation_id,kind,input_digest,actor,at) VALUES($1,$2,$2,'claimed',$3,'system',$4)`, uuid.New(), id, digest, now); err != nil {
		return WorkSessionView{}, err
	}
	if err = moveClaimedTask(ctx, tx, task); err != nil {
		return WorkSessionView{}, err
	}
	return InspectWorkSession(ctx, tx, id, now)
}

func HeartbeatWork(ctx context.Context, tx pgx.Tx, credential WorkCredential, sequence int64, now time.Time) (WorkSessionView, error) {
	if sequence < 1 || sequence > maxPlanningRevision {
		return WorkSessionView{}, ErrWorkInput
	}
	row, err := readWorkSession(ctx, tx, credential.SessionID, true, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkSessionView{}, ErrWorkCredential
	}
	if err != nil {
		return WorkSessionView{}, err
	}
	if err = authenticateWork(row, credential); err != nil {
		return WorkSessionView{}, err
	}
	if row.view.ReleasedAt != nil {
		return WorkSessionView{}, ErrWorkRevoked
	}
	if sequence <= row.view.HeartbeatSequence {
		return row.view, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE work_sessions SET heartbeat_sequence=$2,last_contact_at=$3 WHERE id=$1`, credential.SessionID, sequence, workTime(now, row.view)); err != nil {
		return WorkSessionView{}, err
	}
	return InspectWorkSession(ctx, tx, credential.SessionID, now)
}
