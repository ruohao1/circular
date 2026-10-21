package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrTaskPlanningInput = errors.New("invalid task planning input")
var ErrTaskPlanningConflict = errors.New("Task changed or its content is locked; reload before trying again")

type TaskPlanningPatch struct {
	Title, Description, Priority        *string
	RepositorySet, PreferredAgentSet    bool
	RepositoryID, PreferredAgentID      *uuid.UUID
	ExpectedBoardRevision               *int64
	PlannedStartSet, PlannedFinishSet   bool
	PlannedStartDate, PlannedFinishDate *string
	PrerequisitesSet                    bool
	PrerequisiteTaskIDs                 []uuid.UUID
}
type TaskMoveInput struct {
	ExpectedRevision, ExpectedBoardRevision int64
	Status, Placement                       string
	AnchorID                                *uuid.UUID
}
type TaskPlanningResult struct {
	TaskID                  uuid.UUID
	Revision, BoardRevision int64
	Changed                 bool
}
type TaskEditCapabilities struct {
	ContentEditable, RepositoryEditable     bool
	ContentLockReason, RepositoryLockReason *string
	DependenciesEditable                    bool
	DependenciesLockReason                  *string
}

const maxPlanningRevision int64 = 9007199254740991

func ValidTaskPriority(priority string) bool {
	return slices.Contains([]string{"none", "low", "normal", "high", "urgent"}, priority)
}
func ValidTaskStatus(status string) bool {
	return slices.Contains([]string{"open", "ready", "in_progress", "in_review", "completed", "cancelled"}, status)
}

// Capability reads are non-locking. Mutation callers recheck under the Task lock
// also used by CreateRun, so a queued Run and an instruction edit cannot race.
func TaskPlanningCapabilities(ctx context.Context, tx pgx.Tx, taskID uuid.UUID) (TaskEditCapabilities, error) {
	var source, active, hasRuns, owned, hasWork bool
	err := tx.QueryRow(ctx, `SELECT
  (t.external_refs->'linear'->>'issue_id' IS NOT NULL OR COALESCE(t.external_refs->'circular'->>'kind','')='repository-discovery'
   OR EXISTS(SELECT 1 FROM runs r JOIN external_session_runs s ON s.run_id=r.id WHERE r.task_id=t.id)
   OR EXISTS(SELECT 1 FROM runs r JOIN external_requests q ON q.run_id=r.id WHERE r.task_id=t.id)),
  EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.id AND r.status IN ('queued','provisioning','running','waiting_for_approval','waiting_for_input','finalizing')),
  EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.id),
  EXISTS(SELECT 1 FROM work_sessions s WHERE s.task_id=t.id AND s.released_at IS NULL),
  EXISTS(SELECT 1 FROM work_sessions s WHERE s.task_id=t.id)
  FROM tasks t WHERE t.id=$1`, taskID).Scan(&source, &active, &hasRuns, &owned, &hasWork)
	if err != nil {
		return TaskEditCapabilities{}, err
	}
	out := TaskEditCapabilities{ContentEditable: !source && !active && !owned, RepositoryEditable: !source && !hasRuns && !hasWork, DependenciesEditable: !owned}
	reason := func(s string) *string { return &s }
	if owned {
		out.DependenciesLockReason = reason("active_work")
	}
	if source {
		out.ContentLockReason = reason("source_managed")
		out.RepositoryLockReason = reason("source_managed")
	} else {
		if owned {
			out.ContentLockReason = reason("active_work")
		} else if active {
			out.ContentLockReason = reason("active_run")
		}
		if hasRuns {
			out.RepositoryLockReason = reason("has_runs")
		} else if hasWork {
			out.RepositoryLockReason = reason("has_work_sessions")
		}
	}
	return out, nil
}

type planningTask struct {
	id, project                          uuid.UUID
	repository, preferred                *uuid.UUID
	title, description, priority, status string
	revision, board                      int64
	plannedStart, plannedFinish          *string
}

func lockPlanningTask(ctx context.Context, tx pgx.Tx, id uuid.UUID) (planningTask, error) {
	t := planningTask{id: id}
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id=$1`, id).Scan(&t.project); err != nil {
		return t, err
	}
	if err := tx.QueryRow(ctx, `SELECT task_board_revision FROM projects WHERE id=$1 FOR NO KEY UPDATE`, t.project).Scan(&t.board); err != nil {
		return t, err
	}
	err := tx.QueryRow(ctx, `SELECT repository_id,preferred_agent_id,title,description,priority,status,revision,to_char(planned_start_date,'YYYY-MM-DD'),to_char(planned_finish_date,'YYYY-MM-DD') FROM tasks WHERE id=$1 AND project_id=$2 FOR UPDATE`, id, t.project).Scan(&t.repository, &t.preferred, &t.title, &t.description, &t.priority, &t.status, &t.revision, &t.plannedStart, &t.plannedFinish)
	return t, err
}

func UpdateTaskPlanning(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, expectedRevision int64, patch TaskPlanningPatch) (TaskPlanningResult, error) {
	if expectedRevision < 1 || expectedRevision > maxPlanningRevision {
		return TaskPlanningResult{}, ErrTaskPlanningInput
	}
	if (patch.PlannedStartSet || patch.PlannedFinishSet) && patch.ExpectedBoardRevision == nil || patch.ExpectedBoardRevision != nil && (*patch.ExpectedBoardRevision < 1 || *patch.ExpectedBoardRevision > maxPlanningRevision) {
		return TaskPlanningResult{}, ErrTaskPlanningInput
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" || utf8.RuneCountInString(title) > 500 {
			return TaskPlanningResult{}, ErrTaskPlanningInput
		}
		patch.Title = &title
	}
	if patch.Description != nil && utf8.RuneCountInString(*patch.Description) > 20000 || patch.Priority != nil && !ValidTaskPriority(*patch.Priority) {
		return TaskPlanningResult{}, ErrTaskPlanningInput
	}
	t, err := lockPlanningTask(ctx, tx, taskID)
	if err != nil {
		return TaskPlanningResult{}, err
	}
	out := TaskPlanningResult{TaskID: taskID, Revision: t.revision, BoardRevision: t.board}
	if t.revision != expectedRevision || patch.ExpectedBoardRevision != nil && t.board != *patch.ExpectedBoardRevision {
		return out, ErrTaskPlanningConflict
	}
	start, finish := t.plannedStart, t.plannedFinish
	if patch.PlannedStartSet {
		start = patch.PlannedStartDate
	}
	if patch.PlannedFinishSet {
		finish = patch.PlannedFinishDate
	}
	if err := ValidateTaskSchedule(start, finish); err != nil {
		return out, err
	}
	changedDates := !sameOptionalDate(start, t.plannedStart) || !sameOptionalDate(finish, t.plannedFinish)
	caps, err := TaskPlanningCapabilities(ctx, tx, taskID)
	if err != nil {
		return out, err
	}
	changedText := patch.Title != nil && *patch.Title != t.title || patch.Description != nil && *patch.Description != t.description
	changedRepo := patch.RepositorySet && !sameOptionalUUID(patch.RepositoryID, t.repository)
	changedAgent := patch.PreferredAgentSet && !sameOptionalUUID(patch.PreferredAgentID, t.preferred)
	if changedText && !caps.ContentEditable || changedRepo && !caps.RepositoryEditable {
		return out, ErrTaskPlanningConflict
	}
	if changedRepo && patch.RepositoryID != nil {
		var project uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT project_id FROM repositories WHERE id=$1`, *patch.RepositoryID).Scan(&project); err != nil {
			return out, err
		}
		if project != t.project {
			return out, pgx.ErrNoRows
		}
	}
	if changedAgent && patch.PreferredAgentID != nil {
		var project uuid.UUID
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT project_id,enabled FROM agents WHERE id=$1`, *patch.PreferredAgentID).Scan(&project, &enabled); err != nil {
			return out, err
		}
		if project != t.project {
			return out, pgx.ErrNoRows
		}
		if !enabled {
			return out, fmt.Errorf("%w: preferred Agent is disabled", ErrTaskPlanningInput)
		}
	}
	changedDependencies := false
	if patch.PrerequisitesSet {
		if changedDependencies, err = replaceTaskPrerequisites(ctx, tx, t, patch.PrerequisiteTaskIDs); err != nil {
			return out, err
		}
	}
	if !changedText && !changedRepo && !changedAgent && !changedDates && !changedDependencies && (patch.Priority == nil || *patch.Priority == t.priority) {
		return out, nil
	}
	if patch.Title != nil {
		t.title = *patch.Title
	}
	if patch.Description != nil {
		t.description = *patch.Description
	}
	if patch.Priority != nil {
		t.priority = *patch.Priority
	}
	if patch.RepositorySet {
		t.repository = patch.RepositoryID
	}
	if patch.PreferredAgentSet {
		t.preferred = patch.PreferredAgentID
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET title=$2,description=$3,repository_id=$4,preferred_agent_id=$5,priority=$6,planned_start_date=$7::text::date,planned_finish_date=$8::text::date,revision=revision+1,updated_at=now() WHERE id=$1`, taskID, t.title, t.description, t.repository, t.preferred, t.priority, start, finish); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=$1 RETURNING task_board_revision`, t.project).Scan(&out.BoardRevision); err != nil {
		return out, err
	}
	out.Revision++
	out.Changed = true
	return out, nil
}

func sameOptionalUUID(a, b *uuid.UUID) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func MoveTask(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, input TaskMoveInput) (TaskPlanningResult, error) {
	return moveTask(ctx, tx, taskID, input, false)
}
func moveTask(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, input TaskMoveInput, admission bool) (TaskPlanningResult, error) {
	anchored := input.Placement == "before" || input.Placement == "after"
	if input.ExpectedRevision < 1 || input.ExpectedRevision > maxPlanningRevision || input.ExpectedBoardRevision < 1 || input.ExpectedBoardRevision > maxPlanningRevision || !ValidTaskStatus(input.Status) || !slices.Contains([]string{"start", "end", "before", "after", "up", "down"}, input.Placement) || anchored != (input.AnchorID != nil) {
		return TaskPlanningResult{}, ErrTaskPlanningInput
	}
	t, err := lockPlanningTask(ctx, tx, taskID)
	if err != nil {
		return TaskPlanningResult{}, err
	}
	out := TaskPlanningResult{TaskID: taskID, Revision: t.revision, BoardRevision: t.board}
	if t.revision != input.ExpectedRevision || t.board != input.ExpectedBoardRevision {
		return out, ErrTaskPlanningConflict
	}
	if (input.Placement == "up" || input.Placement == "down") && t.status != input.Status {
		return out, ErrTaskPlanningInput
	}
	if !admission {
		if err = CheckTaskCompletionMove(ctx, tx, taskID, input.Status, time.Now().UTC()); err != nil {
			return out, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id,status FROM tasks WHERE project_id=$1 AND status IN ($2,$3) ORDER BY board_position,id`, t.project, t.status, input.Status)
	if err != nil {
		return out, err
	}
	groups := map[string][]uuid.UUID{t.status: {}, input.Status: {}}
	for rows.Next() {
		var id uuid.UUID
		var status string
		if err = rows.Scan(&id, &status); err != nil {
			rows.Close()
			return out, err
		}
		groups[status] = append(groups[status], id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	source := groups[t.status]
	oldIndex := slices.Index(source, taskID)
	if oldIndex < 0 {
		return out, pgx.ErrNoRows
	}
	groups[t.status] = slices.Delete(slices.Clone(source), oldIndex, oldIndex+1)
	target := groups[input.Status]
	index := len(target)
	switch input.Placement {
	case "start":
		index = 0
	case "before", "after":
		index = slices.Index(target, *input.AnchorID)
		if index < 0 || *input.AnchorID == taskID {
			return out, ErrTaskPlanningInput
		}
		if input.Placement == "after" {
			index++
		}
	case "up":
		index = max(0, oldIndex-1)
	case "down":
		index = min(len(target), oldIndex+1)
	}
	target = slices.Insert(target, index, taskID)
	groups[input.Status] = target
	if t.status == input.Status && slices.Equal(source, target) {
		return out, nil
	}
	// Renumber only affected rows. Pure ordering changes do not rewrite semantic
	// Task revisions/timestamps, including those of displaced neighbors.
	if t.status != input.Status {
		if _, err = tx.Exec(ctx, `UPDATE tasks SET status=$2::text,completion_session_id=CASE WHEN $2::text IN ('open','ready','cancelled') THEN NULL ELSE completion_session_id END,revision=revision+1,updated_at=now() WHERE id=$1`, taskID, input.Status); err != nil {
			return out, err
		}
		out.Revision++
	}
	for _, ids := range groups {
		for position, id := range ids {
			if _, err = tx.Exec(ctx, `UPDATE tasks SET board_position=$2 WHERE id=$1 AND board_position<>$2`, id, position); err != nil {
				return out, err
			}
		}
	}
	if err = tx.QueryRow(ctx, `UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=$1 RETURNING task_board_revision`, t.project).Scan(&out.BoardRevision); err != nil {
		return out, err
	}
	out.Changed = true
	return out, nil
}
