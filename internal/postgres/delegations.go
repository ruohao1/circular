package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/delegations"
)

var ErrDelegationPolicy = errors.New("delegation targets must be distinct enabled Agents in the parent's Project, excluding the parent")

func SetDelegationTargets(ctx context.Context, tx pgx.Tx, parentAgentID uuid.UUID, targetIDs []uuid.UUID) error {
	if len(targetIDs) > 100 {
		return ErrDelegationPolicy
	}
	var project uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM agents WHERE id=$1`, parentAgentID).Scan(&project); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, project); err != nil {
		return err
	}
	ids := []uuid.UUID{parentAgentID}
	seen := map[uuid.UUID]bool{parentAgentID: true}
	for _, id := range targetIDs {
		if id == uuid.Nil || seen[id] {
			return ErrDelegationPolicy
		}
		seen[id] = true
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx, `SELECT id,project_id,enabled FROM agents WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return err
	}
	count := 0
	valid := true
	for rows.Next() {
		var id, scope uuid.UUID
		var enabled bool
		if err = rows.Scan(&id, &scope, &enabled); err != nil {
			rows.Close()
			return err
		}
		count++
		if scope != project || !enabled && len(targetIDs) > 0 {
			valid = false
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !valid || count != len(ids) {
		return ErrDelegationPolicy
	}
	if _, err = tx.Exec(ctx, `DELETE FROM agent_delegation_targets WHERE parent_agent_id=$1`, parentAgentID); err != nil {
		return err
	}
	for _, id := range targetIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO agent_delegation_targets(project_id,parent_agent_id,target_agent_id) VALUES($1,$2,$3)`, project, parentAgentID, id); err != nil {
			return err
		}
	}
	return nil
}

func DelegationTargets(ctx context.Context, tx pgx.Tx, parentAgentID uuid.UUID) ([]delegations.Target, error) {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT true FROM agents WHERE id=$1`, parentAgentID).Scan(&present); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.name FROM agent_delegation_targets p JOIN agents a ON a.id=p.target_agent_id WHERE p.parent_agent_id=$1 ORDER BY a.id`, parentAgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []delegations.Target{}
	for rows.Next() {
		var target delegations.Target
		if err = rows.Scan(&target.AgentID, &target.Name); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
