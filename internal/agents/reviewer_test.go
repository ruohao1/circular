package agents_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestReviewerPresetPreservesCustomizationAndStartsNoRuns(t *testing.T) {
	pool := testsupport.Database(t)
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Review fixture')`, project); err != nil {
		t.Fatal(err)
	}
	ensure := func() string {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		id, err := agents.EnsureReviewer(t.Context(), tx, project)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := ensure()
	if _, err := pool.Exec(t.Context(), `UPDATE agents SET name='My reviewer',enabled=false,instructions='Preserve me',backend_config='{"model":"gpt-5.6-terra","reasoning_effort":"high"}' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	if second := ensure(); second != first {
		t.Fatal("preset identity changed")
	}
	var name, instructions, model string
	var enabled bool
	var runs int
	err := pool.QueryRow(t.Context(), `SELECT name,instructions,enabled,backend_config->>'model',(SELECT count(*) FROM runs) FROM agents WHERE id=$1`, first).Scan(&name, &instructions, &enabled, &model, &runs)
	if err != nil || name != "My reviewer" || instructions != "Preserve me" || enabled || model != "gpt-5.6-terra" || runs != 0 {
		t.Fatalf("customization or no-work default changed: %v", err)
	}
}

func TestConcurrentReviewerSetupPreservesCustomAgents(t *testing.T) {
	pool := testsupport.Database(t)
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Reviewer concurrency')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'PR reviewer','fake','User instructions','{}',false)`, uuid.New(), project); err != nil {
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
			id, err := agents.EnsureReviewer(t.Context(), tx, project)
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
			t.Fatal("concurrent requests duplicated reviewer Agents")
		}
	}
	var count int
	var name, original string
	if err := pool.QueryRow(t.Context(), `SELECT name FROM agents WHERE id=$1`, first).Scan(&name); err != nil || name != "PR reviewer (2)" {
		t.Fatal("default replaced a custom Agent name", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*),max(instructions) FILTER (WHERE preset IS NULL) FROM agents WHERE project_id=$1`, project).Scan(&count, &original); err != nil || count != 2 || original != "User instructions" {
		t.Fatal("custom Agent changed", err)
	}
}
