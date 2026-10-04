package migrate_test

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
)

func TestIdentityUpgradePreservesLegacyConnections(t *testing.T) {
	pool := legacyDatabase(t, 12)
	project, connection := uuid.New(), uuid.New()
	grant := []byte("opaque-encrypted-existing-grant")
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Identity upgrade') RETURNING id)
	 INSERT INTO integrations(id,project_id,provider,enabled,config,credentials) SELECT $2,id,'linear',true,'{"account_id":"original"}', $3 FROM p`, project, connection, grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var bindings, identities, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM integration_identity_bindings),(SELECT count(*) FROM provider_identities),(SELECT count(*) FROM runs)`).Scan(&bindings, &identities, &runs); err != nil {
		t.Fatal("identity migration is missing", err)
	}
	if bindings != 0 || identities != 0 || runs != 0 {
		t.Fatal("upgrade enabled app identity or launched work")
	}
	var enabled bool
	var stored []byte
	var account string
	if err := pool.QueryRow(t.Context(), `SELECT enabled,credentials,config->>'account_id' FROM integrations WHERE id=$1`, connection).Scan(&enabled, &stored, &account); err != nil {
		t.Fatal(err)
	}
	if !enabled || !bytes.Equal(grant, stored) || account != "original" {
		t.Fatal("upgrade changed the existing connection")
	}
}
