package migrate_test

import (
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
)

func TestExternalRequestUpgradeNeverEnablesOrReplays(t *testing.T) {
	p := legacyDatabase(t, 14)
	if e := migrate.Up(t.Context(), p); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM linear_request_routes)+(SELECT count(*) FROM external_requests)+(SELECT count(*) FROM external_run_inputs)+(SELECT count(*) FROM linear_agent_activities)`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
