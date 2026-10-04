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
	var current runstate.Status
	if e := tx.QueryRow(ctx, `SELECT status FROM runs WHERE id=$1 FOR UPDATE`, id).Scan(&current); e != nil {
		return e
	}
	if current == runstate.Cancelled || (source == "linear" && current.Terminal()) {
		return nil
	}
	if e := runstate.Validate(current, runstate.Cancelled); e != nil {
		return fmt.Errorf("%w: %s", ErrRunCancelConflict, e)
	}
	now := time.Now().UTC()
	if _, e := tx.Exec(ctx, `UPDATE runs SET status='cancelled',finished_at=$2,updated_at=$2 WHERE id=$1`, id, now); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) SELECT $1,$2,COALESCE(MAX(sequence),0)+1,'run.cancelled',$3,'{}'::json,$4 FROM events WHERE run_id=$2`, uuid.New(), id, source, now)
	return e
}
