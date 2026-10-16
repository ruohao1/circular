package postgres

import (
	"encoding/json"
	"github.com/ruohao1/circular/internal/runstate"
)

func (r *RunResources) DelegationProgress() ([]byte, string, error) {
	if err := r.guard(); err != nil {
		return nil, "", err
	}
	if r.status != runstate.Running || r.kind != runstate.Coding || r.invocation != 1 {
		return nil, "", ErrResourceState
	}
	if _, err := r.InvocationInput(); err != nil {
		return nil, "", err
	}
	var digest string
	if err := r.tx.QueryRow(r.ctx, `SELECT input_sha256 FROM run_invocations WHERE run_id=$1 AND number=1`, r.id).Scan(&digest); err != nil {
		return nil, "", err
	}
	rows, err := r.tx.Query(r.ctx, `SELECT json_build_object('sequence',sequence,'type',type,'data',data) FROM events WHERE run_id=$1 AND type IN ('agent.message.completed','agent.message.delta') ORDER BY sequence`, r.id)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	events := []json.RawMessage{}
	size := 0
	for rows.Next() {
		var event json.RawMessage
		if err = rows.Scan(&event); err != nil {
			return nil, "", err
		}
		size += len(event)
		if size > 16*1024*1024 {
			return nil, "", ErrResourceConflict
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(map[string]any{"run_id": r.id, "invocation": 1, "input_sha256": digest, "events": events})
	if err != nil || len(raw) > 16*1024*1024 {
		return nil, "", ErrResourceConflict
	}
	return raw, digest, nil
}
