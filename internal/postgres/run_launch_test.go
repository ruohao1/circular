package postgres_test

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"testing"
)

func TestRunLaunchKeyReplayBeforeMutableAgentChecks(t *testing.T) {
	p := database(t)
	original := seed(t, p, 1)[0]
	var task, agent uuid.UUID
	if e := p.QueryRow(t.Context(), `SELECT task_id,agent_id FROM runs WHERE id=$1`, original).Scan(&task, &agent); e != nil {
		t.Fatal(e)
	}
	launch := func(key string, refs json.RawMessage) (postgres.RunLaunchResult, error) {
		tx, e := p.Begin(t.Context())
		if e != nil {
			return postgres.RunLaunchResult{}, e
		}
		defer tx.Rollback(t.Context())
		r, e := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: task, AgentID: agent, RequestKey: key, ExternalRefs: refs})
		if e == nil {
			e = tx.Commit(t.Context())
		}
		return r, e
	}
	first, e := launch("one", json.RawMessage(`{"from":"test"}`))
	if e != nil || !first.Created {
		t.Fatal(first, e)
	}
	if _, e = p.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, agent); e != nil {
		t.Fatal(e)
	}
	again, e := launch("one", json.RawMessage(`{"from":"test"}`))
	if e != nil || again.Created || first.RunID != again.RunID {
		t.Fatal(again, e)
	}
	if _, e = launch("one", json.RawMessage(`{"from":"changed"}`)); !errors.Is(e, postgres.ErrRunLaunchConflict) {
		t.Fatal(e)
	}
	if _, e = launch("new", json.RawMessage(`{}`)); !errors.Is(e, postgres.ErrRunLaunchInput) {
		t.Fatal(e)
	}
}
