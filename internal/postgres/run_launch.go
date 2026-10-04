package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
)

var ErrRunLaunchConflict = errors.New("request_key was already used with different launch parameters; reuse the original parameters or choose a new key for a new attempt")
var ErrRunLaunchInput = errors.New("invalid run launch")

type RunLaunchInput struct {
	TaskID, AgentID uuid.UUID
	RequestKey      string
	ExternalRefs    json.RawMessage
}
type RunLaunchResult struct {
	RunID   uuid.UUID
	Created bool
}

func CreateRun(ctx context.Context, tx pgx.Tx, in RunLaunchInput) (RunLaunchResult, error) {
	var out RunLaunchResult
	var project uuid.UUID
	if in.TaskID == uuid.Nil || in.AgentID == uuid.Nil || len(in.RequestKey) > 200 || in.RequestKey != "" && strings.TrimSpace(in.RequestKey) == "" {
		return out, ErrRunLaunchInput
	}
	if len(in.ExternalRefs) == 0 {
		in.ExternalRefs = json.RawMessage(`{}`)
	}
	var refs map[string]any
	if json.Unmarshal(in.ExternalRefs, &refs) != nil || refs == nil {
		return out, ErrRunLaunchInput
	}
	if e := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id=$1 FOR UPDATE`, in.TaskID).Scan(&project); e != nil {
		return out, e
	}
	if in.RequestKey != "" {
		var same bool
		e := tx.QueryRow(ctx, `SELECT id,agent_id=$3 AND external_refs::jsonb=$4::jsonb FROM runs WHERE task_id=$1 AND request_key=$2`, in.TaskID, in.RequestKey, in.AgentID, in.ExternalRefs).Scan(&out.RunID, &same)
		if e == nil {
			if !same {
				return RunLaunchResult{}, ErrRunLaunchConflict
			}
			return out, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
	}
	var agentProject uuid.UUID
	var backend string
	var enabled bool
	if e := tx.QueryRow(ctx, `SELECT project_id,backend,enabled FROM agents WHERE id=$1`, in.AgentID).Scan(&agentProject, &backend, &enabled); e != nil {
		return out, e
	}
	if project != agentProject {
		return out, fmt.Errorf("%w: task and agent belong to different projects", ErrRunLaunchInput)
	}
	if !enabled {
		return out, fmt.Errorf("%w: agent is disabled", ErrRunLaunchInput)
	}
	out.RunID = uuid.New()
	_, e := tx.Exec(ctx, `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs,request_key) SELECT $1,$2,$3,$4,'queued',COALESCE(MAX(attempt),0)+1,$5,NULLIF($6,'') FROM runs WHERE task_id=$2`, out.RunID, in.TaskID, in.AgentID, backend, in.ExternalRefs, in.RequestKey)
	if e != nil {
		return RunLaunchResult{}, e
	}
	out.Created = true
	return out, nil
}
