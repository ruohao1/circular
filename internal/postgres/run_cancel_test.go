package postgres_test

import (
	"errors"
	"github.com/ruohao1/circular/internal/postgres"
	"testing"
)

func TestCancelRunSharedIdempotentAndTerminalPolicy(t *testing.T) {
	p := database(t)
	runs := seed(t, p, 2)
	cancel := func(i int, source string) error {
		tx, e := p.Begin(t.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback(t.Context())
		if e = postgres.CancelRun(t.Context(), tx, runs[i], source); e != nil {
			return e
		}
		return tx.Commit(t.Context())
	}
	if e := cancel(0, "linear"); e != nil {
		t.Fatal(e)
	}
	if e := cancel(0, "api"); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, runs[1]); e != nil {
		t.Fatal(e)
	}
	if e := cancel(1, "api"); !errors.Is(e, postgres.ErrRunCancelConflict) {
		t.Fatal(e)
	}
	if e := cancel(1, "linear"); e != nil {
		t.Fatal(e)
	}
	var events int
	if e := p.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='run.cancelled'`).Scan(&events); e != nil || events != 1 {
		t.Fatal(events, e)
	}
}
