package migrate_test

import (
	"bytes"
	"errors"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/postgres"
	"testing"
)

func TestApprovalMigrationPreservesHistoryWithoutInventingConsent(t *testing.T) {
	p := legacyDatabase(t, 21)
	_, task, _ := workHistory(t, p)
	before := workHistoryJSON(t, p)
	for range 2 {
		if err := migrate.Up(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, workHistoryJSON(t, p)) {
		t.Fatal("approval migration changed history")
	}
	var count int
	if err := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM task_proposals)+(SELECT count(*) FROM task_proposal_decisions)+(SELECT count(*) FROM approval_sessions)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented consent", count, err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = postgres.RequireTaskApproval(t.Context(), tx, task); !errors.Is(err, postgres.ErrTaskApprovalRequired) {
		t.Fatal("historical consent was assumed", err)
	}
}
