package postgres

import (
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

type CompletionConflict struct {
	Blockers []completion.Blocker `json:"blockers"`
}

func (e *CompletionConflict) Error() string {
	messages := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		messages = append(messages, b.Message+". "+b.Action)
	}
	return strings.Join(messages, "; ")
}

func InspectTaskCompletion(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, now time.Time) (completion.View, error) {
	return inspectTaskCompletion(ctx, tx, taskID, nil, now)
}
func inspectTaskCompletion(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, sessionID *uuid.UUID, now time.Time) (completion.View, error) {
	v := completion.View{TaskID: taskID, ObservedAt: now.UTC()}
	var policy, contract []byte
	var current *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT revision,status,completion_policy,completion_contract,completion_session_id,EXISTS(SELECT 1 FROM work_sessions s WHERE s.task_id=t.id AND s.released_at IS NULL) FROM tasks t WHERE id=$1`, taskID).Scan(&v.Revision, &v.Stage, &policy, &contract, &current, &v.OwnerActive)
	if err != nil {
		return v, err
	}
	if len(policy) > 0 {
		if err = json.Unmarshal(policy, &v.Policy); err != nil {
			return v, err
		}
	}
	if len(contract) > 0 {
		if err = json.Unmarshal(contract, &v.Contract); err != nil {
			return v, err
		}
	}
	if current != nil {
		v.CurrentSessionID = *current
	}
	if sessionID == nil {
		sessionID = current
	}
	if sessionID != nil {
		session, err := InspectWorkSession(ctx, tx, *sessionID, now)
		if err != nil {
			return v, err
		}
		if session.TaskID != taskID {
			return v, ErrWorkRevoked
		}
		v.SessionID = session.ID
		v.Released = session.ReleasedAt != nil
		v.NeedsUserResolution = session.NeedsUserResolution
		if session.Outcome != nil {
			v.Outcome = string(*session.Outcome)
		}
		if session.RunID != nil {
			v.CleanupPending, err = nativeCleanupPending(ctx, tx, *session.RunID)
			if err != nil {
				return v, err
			}
		}
		var report []byte
		err = tx.QueryRow(ctx, `SELECT report FROM task_completions WHERE session_id=$1`, *sessionID).Scan(&report)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return v, err
		}
		if len(report) > 0 {
			if err = json.Unmarshal(report, &v.Report); err != nil {
				return v, err
			}
		}
	}
	if v.SessionID != uuid.Nil {
		if err := inspectCompletionEvidence(ctx, tx, &v); err != nil {
			return v, err
		}
	}
	v.Verdict = completion.Evaluate(v.Input, now)
	return v, nil
}

func completionMoveError(in completion.Input, target string, now time.Time) error {
	if in.Stage == target {
		return nil
	}
	if in.OwnerActive {
		return &CompletionConflict{Blockers: []completion.Blocker{{Code: "owner_active", Message: "Work owns this Task", Action: "Finish or explicitly release the current work before changing its stage"}}}
	}
	if target != "in_review" && target != "completed" {
		return nil
	}
	verdict := completion.Evaluate(in, now)
	if target == "in_review" && verdict.NextStage == "in_review" || target == "completed" && verdict.CanComplete {
		return nil
	}
	if len(verdict.Blockers) == 0 {
		verdict.Blockers = append(verdict.Blockers, completion.Blocker{Code: "policy_mismatch", Message: "This stage does not match the frozen policy", Action: "Use the completion stage required by this Task"})
	}
	return &CompletionConflict{Blockers: verdict.Blockers}
}
func CheckTaskCompletionMove(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, target string, now time.Time) error {
	view, err := InspectTaskCompletion(ctx, tx, taskID, now)
	if err != nil {
		return err
	}
	return completionMoveError(view.Input, target, now)
}
func CheckTaskCompletionCreation(target string) error {
	return completionMoveError(completion.Input{}, target, time.Time{})
}

// Run-locked ingestion retains candidates; only the final invocation is accepted.
func (r *RunResources) SaveCompletionReport(invocation int, report completion.Report) error {
	if err := r.guard(); err != nil {
		return err
	}
	if r.kind != runstate.Coding || r.status != runstate.Running || invocation != r.invocation {
		return ErrResourceState
	}
	row, err := nativeWorkRow(r.ctx, r.tx, r.id, time.Now().UTC())
	if err != nil {
		return err
	}
	if row.view.ReleasedAt != nil || row.view.NeedsUserResolution {
		return ErrWorkRevoked
	}
	var raw []byte
	var contract completion.Contract
	if err = r.tx.QueryRow(r.ctx, `SELECT completion_contract FROM tasks WHERE id=$1`, row.view.TaskID).Scan(&raw); err != nil {
		return err
	}
	if json.Unmarshal(raw, &contract) != nil {
		return ErrResourceConflict
	}
	if err = completion.ValidateReport(contract.Policy, report); err != nil {
		return err
	}
	raw, err = json.Marshal(report)
	if err != nil {
		return err
	}
	tag, err := r.tx.Exec(r.ctx, `INSERT INTO native_completion_reports(run_id,invocation,report) VALUES($1,$2,$3) ON CONFLICT(run_id,invocation) DO NOTHING`, r.id, invocation, raw)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	var same bool
	if err = r.tx.QueryRow(r.ctx, `SELECT report=$3::jsonb FROM native_completion_reports WHERE run_id=$1 AND invocation=$2`, r.id, invocation, raw).Scan(&same); err != nil {
		return err
	}
	if !same {
		return ErrResourceConflict
	}
	return nil
}

// ReconcileTaskCompletion is also safe after a worker restart or a newer claim.
func ReconcileTaskCompletion(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, now time.Time) (bool, error) {
	var taskID uuid.UUID
	var runID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT task_id,run_id FROM work_sessions WHERE id=$1`, sessionID).Scan(&taskID, &runID); err != nil {
		return false, err
	}
	if _, err := lockPlanningTask(ctx, tx, taskID); err != nil {
		return false, err
	}
	var invocation int
	if runID != nil {
		if err := tx.QueryRow(ctx, `SELECT invocation FROM runs WHERE id=$1 FOR UPDATE`, *runID).Scan(&invocation); err != nil {
			return false, err
		}
	}
	row, err := readWorkSession(ctx, tx, sessionID, true, now)
	if err != nil {
		return false, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT reconciliation_pending FROM task_completions WHERE session_id=$1 FOR UPDATE`, sessionID).Scan(&pending); err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	if runID != nil && row.view.Outcome != nil && row.view.ReleasedAt != nil && !row.view.NeedsUserResolution {
		if _, err = tx.Exec(ctx, `UPDATE task_completions c SET report=n.report,report_invocation=n.invocation FROM native_completion_reports n WHERE c.session_id=$1 AND c.report IS NULL AND n.run_id=$2 AND n.invocation=$3`, sessionID, *runID, invocation); err != nil {
			return false, err
		}
	}
	view, err := inspectTaskCompletion(ctx, tx, taskID, &sessionID, now)
	if err != nil {
		return false, err
	}
	var applied *time.Time
	if view.Verdict.NextStage != "" {
		task, err := lockPlanningTask(ctx, tx, taskID)
		if err != nil {
			return false, err
		}
		if _, err = MoveTask(ctx, tx, taskID, TaskMoveInput{ExpectedRevision: task.revision, ExpectedBoardRevision: task.board, Status: view.Verdict.NextStage, Placement: "end"}); err != nil {
			return false, err
		}
		at := now.UTC()
		applied = &at
	}
	decision, err := json.Marshal(view.Verdict)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE task_completions SET decision=$2,reconciliation_pending=false,applied_at=COALESCE(applied_at,$3) WHERE session_id=$1`, sessionID, decision, applied)
	return true, err
}
