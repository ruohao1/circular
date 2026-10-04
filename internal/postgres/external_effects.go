package postgres

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrExternalStopped = errors.New("external request stopped or its route/access is disabled; no new provider write was started")

// ExternalEffectAllowed locks the request before a reservation/launch, sharing
// the stop lock order. Manual runs have no external-request policy to consult.
func ExternalEffectAllowed(ctx context.Context, tx pgx.Tx, run uuid.UUID) (bool, error) {

	var id, identity, project, route string
	e := tx.QueryRow(ctx, `SELECT q.id,q.identity_id,q.project_id,q.route_id FROM external_requests q JOIN external_session_runs s ON s.request_id=q.id WHERE s.run_id=$1`, run).Scan(&id, &identity, &project, &route)
	if errors.Is(e, pgx.ErrNoRows) {
		return true, nil
	}
	if e != nil {
		return false, e
	}
	// Identity -> binding -> route -> request is also the launch/revocation order.
	// These shared locks fence settings changes until the reservation commits.
	var locked string
	for _, lock := range []struct {
		query string
		args  []any
	}{
		{`SELECT id FROM provider_identities WHERE id=$1 FOR SHARE`, []any{identity}},
		{`SELECT project_id FROM integration_identity_bindings WHERE identity_id=$1 AND project_id=$2 FOR SHARE`, []any{identity, project}},
		{`SELECT id FROM linear_request_routes WHERE id=$1 FOR SHARE`, []any{route}},
		{`SELECT id FROM external_requests WHERE id=$1 FOR NO KEY UPDATE`, []any{id}},
	} {
		if e = tx.QueryRow(ctx, lock.query, lock.args...).Scan(&locked); errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		} else if e != nil {
			return false, e
		}
	}

	var allowed bool
	e = tx.QueryRow(ctx, `SELECT q.stopped_at IS NULL AND r.enabled AND b.enabled AND i.enabled AND i.status='available' FROM external_requests q JOIN linear_request_routes r ON r.id=q.route_id JOIN integration_identity_bindings b ON b.identity_id=q.identity_id AND b.project_id=q.project_id JOIN provider_identities i ON i.id=q.identity_id WHERE q.id=$1`, id).Scan(&allowed)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	return allowed, e
}
func ReserveExternalEffect(ctx context.Context, tx pgx.Tx, run uuid.UUID, effect string) error {
	allowed, e := ExternalEffectAllowed(ctx, tx, run)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrExternalStopped
	}
	_, e = tx.Exec(ctx, `INSERT INTO external_effect_reservations(id,request_id,run_id,effect) SELECT $1,request_id,$2,$3 FROM external_session_runs WHERE run_id=$2`, uuid.New(), run, effect)
	return e
}
