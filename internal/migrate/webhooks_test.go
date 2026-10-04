package migrate_test

import (
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
)

func TestWebhookUpgradeIsPassive(t *testing.T) {
	pool := legacyDatabase(t, 13)
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var settings, deliveries, runs int
	err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM integration_webhook_settings),(SELECT count(*) FROM integration_webhook_deliveries),(SELECT count(*) FROM runs)`).Scan(&settings, &deliveries, &runs)
	if err != nil {
		t.Fatal(err)
	}
	if settings+deliveries+runs != 0 {
		t.Fatal("migration enabled reception or created work")
	}
}
