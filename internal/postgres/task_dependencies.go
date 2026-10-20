package postgres

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TaskPrerequisites(ctx context.Context, tx pgx.Tx, taskID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT prerequisite_task_id FROM task_dependencies WHERE task_id=$1 ORDER BY prerequisite_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// The caller holds the Project lock, serializing graph edits with admission.
func replaceTaskPrerequisites(ctx context.Context, tx pgx.Tx, task planningTask, ids []uuid.UUID) (bool, error) {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	for i, id := range ids {
		if id == uuid.Nil || id == task.id || i > 0 && ids[i-1] == id {
			return false, fmt.Errorf("%w: prerequisites must be distinct, non-self Task IDs", ErrTaskPlanningInput)
		}
	}
	current, err := TaskPrerequisites(ctx, tx, task.id)
	if err != nil {
		return false, err
	}
	if slices.Equal(ids, current) {
		return false, nil
	}
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_sessions WHERE task_id=$1 AND released_at IS NULL)`, task.id).Scan(&owned); err != nil {
		return false, err
	}
	if owned {
		return false, ErrTaskPlanningConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id=$1 AND id=ANY($2::uuid[])`, task.project, ids).Scan(&count); err != nil {
		return false, err
	}
	if count != len(ids) {
		return false, fmt.Errorf("%w: every prerequisite must exist in this Project", ErrTaskPlanningInput)
	}
	var cycle bool
	err = tx.QueryRow(ctx, `WITH RECURSIVE reachable(id) AS (
		SELECT unnest($2::uuid[]) UNION
		SELECT d.prerequisite_task_id FROM task_dependencies d JOIN reachable r ON d.task_id=r.id WHERE d.project_id=$1
	) SELECT EXISTS(SELECT 1 FROM reachable WHERE id=$3)`, task.project, ids, task.id).Scan(&cycle)
	if err != nil {
		return false, err
	}
	if cycle {
		return false, fmt.Errorf("%w: prerequisites would create a cycle", ErrTaskPlanningInput)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM task_dependencies WHERE task_id=$1`, task.id); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_dependencies(project_id,task_id,prerequisite_task_id) SELECT $1,$2,unnest($3::uuid[])`, task.project, task.id, ids)
	return err == nil, err
}
