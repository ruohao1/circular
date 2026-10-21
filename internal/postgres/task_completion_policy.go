package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
)

func SetTaskCompletionPolicy(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, expectedRevision int64, policy completion.Policy) error {
	if err := completion.ValidatePolicy(policy); err != nil {
		return fmt.Errorf("%w: %v", ErrTaskPlanningInput, err)
	}
	if expectedRevision < 1 || expectedRevision > maxPlanningRevision {
		return ErrTaskPlanningInput
	}
	task, err := lockPlanningTask(ctx, tx, taskID)
	if err != nil {
		return err
	}
	var frozen, owned bool
	if err = tx.QueryRow(ctx, `SELECT completion_contract IS NOT NULL,EXISTS(SELECT 1 FROM work_sessions WHERE task_id=$1 AND released_at IS NULL) FROM tasks WHERE id=$1`, taskID).Scan(&frozen, &owned); err != nil {
		return err
	}
	if task.revision != expectedRevision || frozen || owned {
		return ErrTaskPlanningConflict
	}
	if policy.Requirements == nil {
		policy.Requirements = []completion.Requirement{}
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE tasks SET completion_policy=$2,revision=revision+1,updated_at=now() WHERE id=$1 AND completion_policy IS DISTINCT FROM $2::jsonb`, taskID, raw)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=$1`, task.project)
	return err
}

func SetRepositoryChecks(ctx context.Context, tx pgx.Tx, repositoryID uuid.UUID, expectedRevision int64, config completion.CheckConfig) (completion.CheckConfig, error) {
	if expectedRevision < 0 || expectedRevision >= maxPlanningRevision {
		return completion.CheckConfig{}, ErrTaskPlanningInput
	}
	config.Revision = expectedRevision + 1
	if err := completion.ValidateChecks(config); err != nil {
		return completion.CheckConfig{}, fmt.Errorf("%w: %v", ErrTaskPlanningInput, err)
	}
	var project uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM repositories WHERE id=$1`, repositoryID).Scan(&project); err != nil {
		return completion.CheckConfig{}, err
	}
	// Admission locks the same Project before reading this configuration.
	if _, err := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, project); err != nil {
		return completion.CheckConfig{}, err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return completion.CheckConfig{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE repositories SET completion_check_config=$3 WHERE id=$1 AND COALESCE((completion_check_config->>'revision')::bigint,0)=$2`, repositoryID, expectedRevision, raw)
	if err != nil {
		return completion.CheckConfig{}, err
	}
	if tag.RowsAffected() != 1 {
		return completion.CheckConfig{}, ErrTaskPlanningConflict
	}
	return config, nil
}

// Caller holds Project → Task locks; an existing contract survives every retry.
func freezeTaskCompletion(ctx context.Context, tx pgx.Tx, task planningTask) (completion.Contract, error) {
	var raw, frozen, checks []byte
	if err := tx.QueryRow(ctx, `SELECT t.completion_policy,t.completion_contract,r.completion_check_config FROM tasks t LEFT JOIN repositories r ON r.id=t.repository_id WHERE t.id=$1`, task.id).Scan(&raw, &frozen, &checks); err != nil {
		return completion.Contract{}, err
	}
	var contract completion.Contract
	if len(frozen) > 0 {
		err := json.Unmarshal(frozen, &contract)
		return contract, err
	}
	ineligible := func(reason string) (completion.Contract, error) {
		return contract, &WorkIneligibleError{Reasons: []string{reason}, Blockers: []uuid.UUID{}}
	}
	if len(raw) == 0 {
		return ineligible("Set the Task completion policy before starting work")
	}
	var p completion.Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return contract, err
	}
	var c completion.CheckConfig
	if len(checks) > 0 {
		if err := json.Unmarshal(checks, &c); err != nil {
			return contract, err
		}
	}
	contract, err := completion.SealContract(p, c)
	if errors.Is(err, completion.ErrChecks) {
		return ineligible("Select required GitHub checks in Repository completion settings before starting review-required work")
	}
	if err != nil {
		return ineligible("Set a valid Task completion policy before starting work")
	}
	sealed, err := json.Marshal(contract)
	if err != nil {
		return contract, err
	}
	_, err = tx.Exec(ctx, `UPDATE tasks SET completion_contract=$2 WHERE id=$1`, task.id, sealed)
	return contract, err
}

func recordCompletionSession(ctx context.Context, tx pgx.Tx, taskID, sessionID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO task_completions(task_id,session_id) VALUES($1,$2)`, taskID, sessionID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE tasks SET completion_session_id=$2 WHERE id=$1`, taskID, sessionID)
	return err
}
