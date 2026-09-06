// Package testsupport owns disposable external fixtures, never application state.
package testsupport

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/postgres"
)

func Database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := EmptyDatabase(t)
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// EmptyDatabase returns an isolated schema in an explicitly configured test
// database. Cleanup may drop only the exact random schema created by this call.
func EmptyDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, postgres.DatabaseURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	schema := "circular_go_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("remove owned test schema: %v", err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(postgres.DatabaseURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
