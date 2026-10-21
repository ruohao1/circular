package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/runstate"
	"time"
)

var ErrDelegationConflict = errors.New("this parent Run already has a different delegation request")

// SuspendForDelegation owns the Project→Task→Run transaction. The caller has
// verified the checkpoint and removed the exact cleanly exited container.
func (s *Resources) SuspendForDelegation(ctx context.Context, parentID uuid.UUID, request delegations.Request, checkpoint delegations.Checkpoint) (record delegations.Record, result error) {
	request, err := delegations.Normalize(request)
	if err != nil {
		return record, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer rollback(ctx, tx)
	var project, task, parentAgent uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT t.project_id,t.id,r.agent_id FROM runs r JOIN tasks t ON t.id=r.task_id WHERE r.id=$1`, parentID).Scan(&project, &task, &parentAgent); err != nil {
		return record, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, project); err != nil {
		return record, err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM tasks WHERE id=$1 FOR UPDATE`, task); err != nil {
		return record, err
	}
	var fingerprint string
	err = tx.QueryRow(ctx, `SELECT id,parent_run_id,target_agent_id,child_task_id,child_run_id,request_key,status,input_fingerprint FROM delegations WHERE parent_run_id=$1 AND request_key IS NOT NULL`, parentID).Scan(&record.ID, &record.ParentRunID, &record.TargetAgentID, &record.ChildTaskID, &record.ChildRunID, &record.RequestKey, &record.Status, &fingerprint)
	if err == nil {
		if request.RequestKey != record.RequestKey || request.Fingerprint() != fingerprint {
			return record, ErrDelegationConflict
		}
		if record.Status == "rejected" {
			return record, ErrDelegationPolicy
		}
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return record, err
	}
	r := &RunResources{ctx: ctx, tx: tx, id: parentID, open: true}
	defer func() { r.open = false }()
	var owner *string
	var expires *time.Time
	err = tx.QueryRow(ctx, `SELECT status,backend,worker_id,lease_expires_at,kind,started_at,invocation FROM runs WHERE id=$1 FOR UPDATE`, parentID).Scan(&r.status, &r.backend, &owner, &expires, &r.kind, &r.startedAt, &r.invocation)
	if err != nil {
		return record, err
	}
	if owner == nil || *owner != s.owner || expires == nil || !expires.After(time.Now()) {
		return record, ErrLeaseLost
	}
	if r.status != runstate.Running {
		return record, ErrResourceState
	}
	if err = requireNativeWork(ctx, tx, parentID); err != nil {
		return record, err
	}
	reject := func() (delegations.Record, error) {
		const reason = "Delegation request rejected: parent, target policy, or checkpoint is no longer eligible"
		objective, _ := json.Marshal(request.Objective)
		_, err := tx.Exec(ctx, `INSERT INTO delegations(id,parent_run_id,target_agent_id,objective,depth,status,request_key,input_fingerprint,objective_json,error) SELECT $1,$2,id,$4,1,'rejected',$5,$6,$7,$8 FROM agents WHERE id=$3`, uuid.New(), parentID, request.AgentID, request.Objective.Description, request.RequestKey, request.Fingerprint(), objective, reason)
		if err != nil {
			return delegations.Record{}, err
		}
		if err = r.event("delegation.rejected", "worker", map[string]any{"reason": reason}); err != nil {
			return delegations.Record{}, err
		}
		if err = r.RecordFailure(reason, nil); err != nil {
			return delegations.Record{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return delegations.Record{}, err
		}
		return delegations.Record{}, ErrDelegationPolicy
	}
	if r.kind != runstate.Coding || r.invocation != 1 || checkpoint.Validate(parentID) != nil || request.AgentID == parentAgent {
		return reject()
	}
	var nested, allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delegations WHERE child_run_id=$1 AND request_key IS NOT NULL),EXISTS(SELECT 1 FROM agent_delegation_targets WHERE parent_agent_id=$2 AND target_agent_id=$3 AND project_id=$4)`, parentID, parentAgent, request.AgentID, project).Scan(&nested, &allowed); err != nil {
		return record, err
	}
	if nested || !allowed {
		return reject()
	}
	rows, err := tx.Query(ctx, `SELECT id,project_id,enabled,backend,instructions,backend_config FROM agents WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []uuid.UUID{parentAgent, request.AgentID})
	if err != nil {
		return record, err
	}
	var instructions, backend string
	var config json.RawMessage
	count := 0
	valid := true
	for rows.Next() {
		var id, scope uuid.UUID
		var enabled bool
		var selected, text string
		var raw json.RawMessage
		if err = rows.Scan(&id, &scope, &enabled, &selected, &text, &raw); err != nil {
			rows.Close()
			return record, err
		}
		count++
		if scope != project || !enabled {
			valid = false
		}
		if id == request.AgentID {
			instructions, backend, config = text, selected, raw
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record, err
	}
	if !valid || count != 2 || (backend != "codex" && backend != "fake") || (r.backend != "codex" && r.backend != "fake") {
		return reject()
	}
	input, err := readInvocation(ctx, tx, parentID, 1)
	if err != nil || input.Backend != r.backend {
		return reject()
	}
	var digest string
	var bound bool
	if err = tx.QueryRow(ctx, `SELECT i.input_sha256,repo.id=$2 AND repo.project_id=$3 AND repo.clone_url=$4 FROM run_invocations i JOIN runs r ON r.id=i.run_id JOIN tasks t ON t.id=r.task_id LEFT JOIN repositories repo ON repo.id=t.repository_id WHERE i.run_id=$1 AND i.number=1`, parentID, input.RepositoryID, project, input.CloneURL).Scan(&digest, &bound); err != nil || !bound || digest != checkpoint.InputSHA256 || input.BaseRef != checkpoint.BaseRef {
		return reject()
	}
	workspace, err := r.workspace()
	if err != nil {
		return record, err
	}
	if workspace == nil || workspace.Status != "ready" || workspace.ContainerID == nil {
		return reject()
	}
	record = delegations.Record{ID: uuid.New(), ParentRunID: parentID, TargetAgentID: request.AgentID, RequestKey: request.RequestKey, Status: "waiting"}
	childTask := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,parent_task_id,completion_policy) VALUES($1,$2,$3,$4,$5,'open','{}',$6,'{"mode":"direct_completion","requirements":[{"key":"handoff","description":"Describe the child result and evidence the parent needs to continue"}]}')`, childTask, project, input.RepositoryID, request.Objective.Title, request.Objective.Description, task); err != nil {
		return record, err
	}
	child, err := CreateRun(ctx, tx, RunLaunchInput{TaskID: childTask, AgentID: request.AgentID, RequestKey: record.ID.String(), ExternalRefs: json.RawMessage(`{}`)})
	if err != nil {
		return record, err
	}
	if _, err = tx.Exec(ctx, `UPDATE runs SET parent_run_id=$2 WHERE id=$1`, child.RunID, parentID); err != nil {
		return record, err
	}
	record.ChildTaskID, record.ChildRunID = &childTask, &child.RunID
	frozen := ProvisioningContext{Invocation: 1, RunID: child.RunID, WorkspaceID: WorkspaceID(child.RunID), RepositoryID: input.RepositoryID, Kind: runstate.Coding, CloneURL: input.CloneURL, BaseRef: checkpoint.SourceCommit, Backend: backend, TaskTitle: request.Objective.Title, TaskDescription: request.Objective.Description, Instructions: instructions, BackendConfig: config, SourceRunID: parentID, SourceCheckpoint: &checkpoint}
	if err = tx.QueryRow(ctx, `SELECT completion_contract FROM tasks WHERE id=$1`, childTask).Scan(&frozen.CompletionContract); err != nil {
		return record, err
	}
	if err = SaveInvocationInput(ctx, tx, frozen); err != nil {
		return record, err
	}
	objective, _ := json.Marshal(request.Objective)
	saved, _ := json.Marshal(checkpoint)
	if _, err = tx.Exec(ctx, `INSERT INTO delegations(id,parent_run_id,target_agent_id,child_task_id,child_run_id,objective,depth,status,request_key,input_fingerprint,objective_json,checkpoint) VALUES($1,$2,$3,$4,$5,$6,1,'waiting',$7,$8,$9,$10)`, record.ID, parentID, request.AgentID, childTask, child.RunID, request.Objective.Description, request.RequestKey, request.Fingerprint(), objective, saved); err != nil {
		return record, err
	}
	if err = r.event("delegation.created", "worker", map[string]any{"delegation_id": record.ID, "child_run_id": child.RunID, "child_task_id": childTask, "target_agent_id": request.AgentID}); err != nil {
		return record, err
	}
	if _, err = tx.Exec(ctx, `UPDATE workspaces SET container_id=NULL WHERE run_id=$1`, parentID); err != nil {
		return record, err
	}
	workspace.ContainerID = nil
	if err = r.workspaceStatus(workspace, "suspended", "worker"); err != nil {
		return record, err
	}
	if err = r.executionTransition(runstate.WaitingForInput, nil); err != nil {
		return record, err
	}
	if err = r.event("run.suspended", "worker", map[string]any{"reason": "delegation", "delegation_id": record.ID, "child_run_id": child.RunID}); err != nil {
		return record, err
	}
	if _, err = tx.Exec(ctx, `UPDATE runs SET worker_id=NULL,lease_expires_at=NULL WHERE id=$1`, parentID); err != nil {
		return record, err
	}
	return record, tx.Commit(ctx)
}
