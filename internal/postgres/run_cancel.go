package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/runstate"
	"time"
)

var ErrRunCancelConflict = errors.New("run cannot be cancelled from its current state")

func CancelRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, source string) error {
	if source != "api" && source != "linear" {
		return ErrRunCancelConflict
	}
	if err := cancelOneRun(ctx, tx, id, source, false); err != nil {
		return err
	}
	// Admission and cancellation both lock the parent before any child.
	var child uuid.UUID
	err := tx.QueryRow(ctx, `SELECT d.child_run_id FROM delegations d JOIN runs p ON p.id=d.parent_run_id WHERE p.id=$1 AND p.status='cancelled' AND d.request_key IS NOT NULL AND d.child_run_id IS NOT NULL`, id).Scan(&child)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return cancelOneRun(ctx, tx, child, source, true)
}

func cancelOneRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, source string, preserveTerminal bool) error {
	var current runstate.Status
	if e := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1 FOR UPDATE`, id).Scan(&current); e != nil {
		return e
	}
	if current == runstate.Cancelled || ((source == "linear" || preserveTerminal) && current.Terminal()) {
		return nil
	}
	if e := runstate.Validate(current, runstate.Cancelled); e != nil {
		return fmt.Errorf("%w: %s", ErrRunCancelConflict, e)
	}
	now := time.Now().UTC()
	if _, e := tx.Exec(ctx, `UPDATE runs SET status='cancelled',finished_at=$2,updated_at=$2 WHERE id=$1`, id, now); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) SELECT $1,$2,COALESCE(MAX(sequence),0)+1,'run.cancelled',$3,json_build_object('invocation',(SELECT invocation FROM runs WHERE id=$2)),$4 FROM events WHERE run_id=$2`, uuid.New(), id, source, now)
	if e != nil {
		return e
	}
	return settleDelegation(ctx, tx, id, runstate.Cancelled)
}
