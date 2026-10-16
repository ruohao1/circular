package postgres_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"testing"
)

func TestDelegationPolicyScopeAndReplacement(t *testing.T) {
	pool := database(t)
	run := seed(t, pool, 1)[0]
	var parent, project uuid.UUID
	if err := pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM runs r JOIN agents a ON a.id=r.agent_id WHERE r.id=$1`, run).Scan(&parent, &project); err != nil {
		t.Fatal(err)
	}
	target := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Reviewer','fake','','{}',true)`, target, project); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	defaults, err := postgres.DelegationTargets(t.Context(), tx, parent)
	if err != nil || len(defaults) != 0 {
		t.Fatal(defaults, err)
	}
	for range 2 {
		if err = postgres.SetDelegationTargets(t.Context(), tx, parent, []uuid.UUID{target}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := postgres.DelegationTargets(t.Context(), tx, parent)
	if err != nil || len(got) != 1 || got[0].AgentID != target {
		t.Fatal(got, err)
	}
	for _, ids := range [][]uuid.UUID{{parent}, {uuid.New()}, {target, target}, {uuid.Nil}} {
		if err = postgres.SetDelegationTargets(t.Context(), tx, parent, ids); err == nil {
			t.Fatal("accepted", ids)
		}
		got, err = postgres.DelegationTargets(t.Context(), tx, parent)
		if err != nil || len(got) != 1 {
			t.Fatal("partial replacement", got, err)
		}
	}
	if err = postgres.SetDelegationTargets(t.Context(), tx, parent, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	got, err = postgres.DelegationTargets(t.Context(), tx, parent)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
