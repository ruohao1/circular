package agents_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestConcurrentDiscoverySetupPreservesCustomAgents(t *testing.T) {
	pool := testsupport.Database(t)
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Discovery concurrency')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Repository discovery','fake','User instructions','{}',false)`, uuid.New(), project); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 6)
	for range cap(results) {
		go func() {
			tx, err := pool.Begin(t.Context())
			if err != nil {
				results <- result{err: err}
				return
			}
			id, err := agents.EnsureDiscovery(t.Context(), tx, project)
			if err == nil {
				err = tx.Commit(t.Context())
			} else {
				_ = tx.Rollback(t.Context())
			}
			results <- result{id, err}
		}()
	}
	first := ""
	for range cap(results) {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if first == "" {
			first = result.id
		}
		if first != result.id {
			t.Fatal("concurrent requests duplicated discovery Agents")
		}
	}
	var count int
	var name, original string
	if err := pool.QueryRow(t.Context(), `SELECT name FROM agents WHERE id=$1`, first).Scan(&name); err != nil || name != "Repository discovery (2)" {
		t.Fatal("default replaced a custom Agent name", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*),max(instructions) FILTER (WHERE preset IS NULL) FROM agents WHERE project_id=$1`, project).Scan(&count, &original); err != nil || count != 2 || original != "User instructions" {
		t.Fatal("custom Agent changed", err)
	}
}
