package migrate_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestConcurrentFreshMigrationAndRepeatPreserveData(t *testing.T) {
	pool := testsupport.EmptyDatabase(t)
	done := make(chan error, 6)
	for range cap(done) {
		go func() { done <- migrate.Up(t.Context(), pool) }()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO projects(id,name,description) VALUES($1,'preserved','unchanged')", id); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var name, description, version string
	if err := pool.QueryRow(t.Context(), "SELECT name,description FROM projects WHERE id=$1", id).Scan(&name, &description); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT version_num FROM alembic_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if name != "preserved" || description != "unchanged" || version != migrate.Head {
		t.Fatal("repeat migration changed stored data")
	}
}

func TestExistingRevisionOneIsUpgradedInPlace(t *testing.T) {
	pool := testsupport.Database(t)
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'legacy data')`, id); err != nil {
		t.Fatal(err)
	}
	// Return the schema to the exact pre-lease shape without dropping any data.
	if _, err := pool.Exec(t.Context(), `ALTER TABLE runs DROP COLUMN lease_expires_at, DROP COLUMN recovery_attempts; UPDATE alembic_version SET version_num='0001'`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var name string
	var columns int
	if err := pool.QueryRow(t.Context(), "SELECT name FROM projects WHERE id=$1", id).Scan(&name); err != nil || name != "legacy data" {
		t.Fatal("existing record lost", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runs' AND column_name IN ('lease_expires_at','recovery_attempts')`).Scan(&columns); err != nil || columns != 2 {
		t.Fatal("lease columns not installed", err)
	}
}

func TestUnknownOrMultipleHeadsAreRejectedWithoutMutation(t *testing.T) {
	for _, versions := range [][]string{{"future"}, {""}, {"0001", "0002"}} {
		pool := testsupport.EmptyDatabase(t)
		if _, err := pool.Exec(t.Context(), "CREATE TABLE alembic_version(version_num varchar(32) PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if _, err := pool.Exec(t.Context(), "INSERT INTO alembic_version VALUES($1)", v); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate.Up(t.Context(), pool); !errors.Is(err, migrate.ErrVersion) {
			t.Fatalf("unsupported ledger accepted: %v", err)
		}
		var tables int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&tables); err != nil || tables != 1 {
			t.Fatal("unknown schema mutated", err)
		}
	}
}

func TestFailedDDLAndLedgerChangesRollbackTogether(t *testing.T) {
	pool := testsupport.EmptyDatabase(t)
	if _, err := pool.Exec(t.Context(), "CREATE TABLE agents(sentinel text); INSERT INTO agents VALUES('keep me')"); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err == nil {
		t.Fatal("schema collision was ignored")
	}
	var tables int
	var value string
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&tables); err != nil || tables != 1 {
		t.Fatal("partial migration escaped rollback", err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT sentinel FROM agents").Scan(&value); err != nil || value != "keep me" {
		t.Fatal("preexisting data was overwritten", err)
	}
}
