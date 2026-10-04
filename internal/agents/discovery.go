// Package agents owns the built-in engineering specializations supplied by Circular.
package agents

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const DiscoveryPreset = "repository-discovery"
const DiscoveryName = "Repository discovery"

//go:embed discovery.md
var DiscoveryInstructions string

const DiscoveryTaskDescription = "Examine this repository to understand its purpose, architecture, conventions, and development workflow. Report the most useful next tasks and recommend the smallest useful set of specialist agents, with ready-to-copy instructions grounded in repository evidence. Return the report in your final message without changing repository files."

// EnsureDiscovery creates the built-in Agent once. The project lock serializes
// concurrent setup requests; the preset identity survives names and configuration
// being customized, including an Agent being disabled.
func EnsureDiscovery(ctx context.Context, tx pgx.Tx, project string) (string, error) {
	return ensurePreset(ctx, tx, project, DiscoveryPreset, DiscoveryName, DiscoveryInstructions)
}

func ensurePreset(ctx context.Context, tx pgx.Tx, project, preset, preferredName, instructions string) (string, error) {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project).Scan(&projectID); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM agents WHERE project_id=$1 AND preset=$2`, project, preset).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT name FROM agents WHERE project_id=$1`, project)
	if err != nil {
		return "", err
	}
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		names[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	name := preferredName
	for suffix := 2; names[name]; suffix++ {
		name = fmt.Sprintf("%s (%d)", preferredName, suffix)
	}
	id = uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled,preset) VALUES($1,$2,$3,'codex',$4,'{}',true,$5)`, id, project, name, instructions, preset)
	return id, err
}

// BackfillDiscovery runs inside the additive migration's transaction. Existing
// Agents, Tasks and Runs are retained, including custom Agents with the same name.
func BackfillDiscovery(ctx context.Context, tx pgx.Tx) error {
	return backfill(ctx, tx, EnsureDiscovery)
}

func backfill(ctx context.Context, tx pgx.Tx, ensure func(context.Context, pgx.Tx, string) (string, error)) error {
	rows, err := tx.Query(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return err
	}
	projects := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := ensure(ctx, tx, project); err != nil {
			return err
		}
	}
	return nil
}
