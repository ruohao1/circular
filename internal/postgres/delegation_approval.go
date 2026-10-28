package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/delegations"
)

// Admission owns a separate Project→Task→Run transaction. Approval only records
// scope; this worker step checks the saved execution intent against current policy.
func (q *Queue) admitApprovedDelegation(ctx context.Context) error {
	var parent, project, task uuid.UUID
	err := q.pool.QueryRow(ctx, `SELECT r.id,t.project_id,t.id FROM delegations d JOIN task_proposals p ON p.id=d.proposal_id JOIN runs r ON r.id=d.parent_run_id JOIN tasks t ON t.id=r.task_id WHERE d.status='awaiting_approval' AND (p.status='approved' OR EXISTS(SELECT 1 FROM task_proposal_decisions decision WHERE decision.proposal_id=p.id AND decision.decision='rejected')) AND r.status='waiting_for_input' AND r.worker_id IS NULL ORDER BY d.created_at,d.id LIMIT 1`).Scan(&parent, &project, &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if _, err = lockPlanningTask(ctx, tx, task); err != nil {
		return err
	}
	var available bool
	if err = tx.QueryRow(ctx, `SELECT status='waiting_for_input' AND worker_id IS NULL AND invocation=1 FROM runs WHERE id=$1 FOR UPDATE`, parent).Scan(&available); err != nil {
		return err
	}
	if !available {
		return nil
	}
	var id, target, proposalID uuid.UUID
	var raw []byte
	var rejected bool
	err = tx.QueryRow(ctx, `SELECT id,target_agent_id,proposal_id,execution_intent,EXISTS(SELECT 1 FROM task_proposal_decisions decision WHERE decision.proposal_id=delegations.proposal_id AND decision.decision='rejected') FROM delegations WHERE parent_run_id=$1 AND status='awaiting_approval' FOR UPDATE`, parent).Scan(&id, &target, &proposalID, &raw, &rejected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = requireNativeWork(ctx, tx, parent); err != nil {
		return err
	}
	p, err := ReadTaskProposal(ctx, tx, proposalID)
	if err != nil {
		return err
	}
	if p.Status == "pending" && !rejected {
		return nil
	}
	refuse := func(outcome, reason string) error {
		result, _ := json.Marshal(delegations.Result{Outcome: outcome, Error: reason})
		if _, err := tx.Exec(ctx, `UPDATE delegations SET status='waiting',result=$2,error=$3,updated_at=now() WHERE id=$1`, id, result, reason); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// Revising the proposal cannot undo refusal of this saved execution intent.
	if rejected {
		return refuse("approval_rejected", "The user rejected the proposed child work. Continue the original parent scope without creating this child again.")
	}
	var intent ProvisioningContext
	if json.Unmarshal(raw, &intent) != nil || intent.SourceRunID != parent || intent.SourceCheckpoint == nil || intent.SourceCheckpoint.Validate(parent) != nil {
		return ErrResourceConflict
	}
	if p.TaskID == nil {
		return ErrResourceConflict
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agents a ON a.id=r.agent_id JOIN agent_delegation_targets d ON d.parent_agent_id=a.id JOIN agents target ON target.id=d.target_agent_id JOIN repositories repo ON repo.id=$3 JOIN tasks child ON child.id=$4 WHERE r.id=$1 AND d.target_agent_id=$2 AND a.enabled AND target.enabled AND target.project_id=$5 AND target.backend=$6 AND target.instructions=$7 AND target.backend_config::jsonb=$8::jsonb AND repo.project_id=$5 AND repo.clone_url=$9 AND child.project_id=$5 AND child.parent_task_id=r.task_id AND child.repository_id=repo.id AND child.title=$10 AND child.description=$11)`, parent, target, intent.RepositoryID, p.TaskID, project, intent.Backend, intent.Instructions, intent.BackendConfig, intent.CloneURL, intent.TaskTitle, intent.TaskDescription).Scan(&eligible)
	if err != nil {
		return err
	}
	if !eligible {
		return refuse("admission_rejected", "The approved child scope or execution settings no longer match the saved delegation intent. Continue the original parent scope; child execution needs a new explicit intent.")
	}
	if _, err = tx.Exec(ctx, `SAVEPOINT child_admission`); err != nil {
		return err
	}
	child, err := CreateRun(ctx, tx, RunLaunchInput{TaskID: *p.TaskID, AgentID: target, RequestKey: id.String(), ExternalRefs: json.RawMessage(`{}`)})
	if err != nil {
		var ineligible *WorkIneligibleError
		var owned *WorkOwnershipConflict
		if !errors.As(err, &ineligible) && !errors.As(err, &owned) {
			return err
		}
		if _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT child_admission`); e != nil {
			return e
		}
		return refuse("admission_rejected", "Child execution could not be admitted: "+err.Error()+". Continue the original parent scope.")
	}
	if _, err = tx.Exec(ctx, `UPDATE runs SET parent_run_id=$2 WHERE id=$1`, child.RunID, parent); err != nil {
		return err
	}
	intent.RunID = child.RunID
	intent.WorkspaceID = WorkspaceID(child.RunID)
	if err = tx.QueryRow(ctx, `SELECT completion_contract,approval_context FROM tasks WHERE id=$1`, p.TaskID).Scan(&intent.CompletionContract, &intent.TaskApprovalContext); err != nil {
		return err
	}
	if err = SaveInvocationInput(ctx, tx, intent); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE delegations SET status='waiting',child_task_id=$2,child_run_id=$3,updated_at=now() WHERE id=$1`, id, p.TaskID, child.RunID); err != nil {
		return err
	}
	r := &RunResources{ctx: ctx, tx: tx, id: parent, open: true}
	defer func() { r.open = false }()
	if err = r.event("delegation.created", "worker", map[string]any{"delegation_id": id, "proposal_id": proposalID, "child_run_id": child.RunID, "child_task_id": p.TaskID, "target_agent_id": target}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
