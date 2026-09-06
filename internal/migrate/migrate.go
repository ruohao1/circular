// Package migrate applies the control-plane schema without an external interpreter.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var scripts embed.FS

const Head = "0002"

var ErrVersion = errors.New("unsupported database schema version; no migrations applied")

// Up is transactional, serialized per database/schema, and forward-only. The
// historical ledger name deliberately remains unchanged so existing deployments
// are adopted without replaying their schema or changing their records.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("a database pool is required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('circular-schema:' || current_schema(),0))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS alembic_version (version_num VARCHAR(32) NOT NULL CONSTRAINT alembic_version_pkc PRIMARY KEY)`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT version_num FROM alembic_version`)
	if err != nil {
		return err
	}
	version, count := "", 0
	for rows.Next() {
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count > 1 || version != "" && version != "0001" && version != Head || count == 1 && version == "" {
		return ErrVersion
	}
	for _, next := range []string{"0001", Head} {
		if next <= version {
			continue
		}
		sql, err := scripts.ReadFile(next + ".sql")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply schema revision %s: %w", next, err)
		}
		if version == "" {
			_, err = tx.Exec(ctx, `INSERT INTO alembic_version(version_num) VALUES ($1)`, next)
		} else {
			_, err = tx.Exec(ctx, `UPDATE alembic_version SET version_num=$1 WHERE version_num=$2`, next, version)
		}
		if err != nil {
			return err
		}
		version = next
	}
	return tx.Commit(ctx)
}
