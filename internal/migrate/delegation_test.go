package migrate_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
)

func TestDelegationMigrationPreservesHistory(t *testing.T) {
	p := legacyDatabase(t, 17)
	project, agent, task, run, delegation := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects(id,name) VALUES($1,'History')`, []any{project}},
		{`INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Agent','fake','','{}',true)`, []any{agent, project}},
		{`INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Keep','History','open','{}')`, []any{task, project}},
		{`INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','succeeded',1,'{}')`, []any{run, task, agent}},
		{`INSERT INTO delegations(id,parent_run_id,target_agent_id,objective,depth,status) VALUES($1,$2,$3,'Legacy objective',3,'legacy')`, []any{delegation, run, agent}},
	}
	for _, s := range statements {
		if _, err := p.Exec(t.Context(), s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrate.Up(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	var objective, status string
	var key *string
	var depth, invocation, count int
	if err := p.QueryRow(t.Context(), `SELECT objective,status,request_key,depth FROM delegations WHERE id=$1`, delegation).Scan(&objective, &status, &key, &depth); err != nil || objective != "Legacy objective" || status != "legacy" || key != nil || depth != 3 {
		t.Fatal(objective, status, key, depth, err)
	}
	if err := p.QueryRow(t.Context(), `SELECT invocation FROM runs WHERE id=$1`, run).Scan(&invocation); err != nil || invocation != 1 {
		t.Fatal(invocation, err)
	}
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM agent_delegation_targets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("default policy enabled", count, err)
	}
}
