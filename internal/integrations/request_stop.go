package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
)

func (s *Service) StopExternalRequest(ctx context.Context, id string) (ExternalRequest, error) {
	if !validUUID(id) {
		return ExternalRequest{}, ErrRequestNotFound
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return ExternalRequest{}, e
	}
	defer rollback(ctx, tx)
	out, e := stopExternalRequest(ctx, tx, id)
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func stopExternalRequest(ctx context.Context, tx pgx.Tx, id string) (ExternalRequest, error) {
	var out ExternalRequest
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM external_requests q WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrRequestNotFound
	}
	if e != nil {
		return out, e
	}
	e = tx.QueryRow(ctx, `UPDATE external_requests q SET stopped_at=COALESCE(stopped_at,now()),status='stopped',reason='Stopped. Provider writes already reserved may finish; their receipts will be checked.',updated_at=now() WHERE id=$1 RETURNING to_jsonb(q)`, id).Scan(&raw)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT run_id FROM external_session_runs WHERE request_id=$1 ORDER BY run_id`, id)
	if e != nil {
		return out, e
	}
	var ids []uuid.UUID
	for rows.Next() {
		var run uuid.UUID
		if e = rows.Scan(&run); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, run)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, run := range ids {
		if e = postgres.CancelRun(ctx, tx, run, "linear"); e != nil {
			return out, e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE pr_review_launch_intents SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='External request stopped before review launch' WHERE status='pending' AND source_run_id IN(SELECT run_id FROM external_session_runs WHERE request_id=$1)`, id); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE github_run_deliveries d SET status='failed',last_error='External request stopped before publication' WHERE d.run_id IN(SELECT run_id FROM external_session_runs WHERE request_id=$1) AND d.status IN ('pending','failed') AND NOT d.pull_request_started AND NOT EXISTS(SELECT 1 FROM external_effect_reservations r WHERE r.run_id=d.run_id AND r.effect='github_pull_request')`, id); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE pr_review_publications p SET status='skipped',last_error='External request stopped before review publication' FROM pr_reviews r JOIN external_session_runs s ON s.run_id=r.run_id WHERE p.review_id=r.id AND s.request_id=$1 AND NOT p.started AND p.status IN ('pending','retrying','failed')`, id); e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE linear_agent_activities SET status='cancelled',lease_owner=NULL,lease_until=NULL WHERE request_id=$1 AND NOT started AND status='pending' AND semantic_key<>'request:stopped'`, id)
	return out, e
}
func (s *Service) acceptLinearPrompt(ctx context.Context, event linearSessionEvent, identity string) (string, string, error) {
	a := event.Activity
	if a == nil || !validUUID(a.ID) || a.SessionID != event.Session.ID || a.Content.Type != "prompt" || !validUUID(a.UserID) || a.UserID == event.ActorID {
		return "ignored", "Unsupported session prompt", nil
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return "", "", e
	}
	defer rollback(ctx, tx)
	id := uuid.NewString()
	// Tombstones survive duplicate and out-of-order creation. Only a signed stop
	// signal changes execution state; the message body cannot authorize actions.
	e = tx.QueryRow(ctx, `INSERT INTO external_requests(id,identity_id,session_id,status,reason,input_fingerprint) VALUES($1,$2,$3,'needs_routing','Waiting for the session creation event.',$4) ON CONFLICT(identity_id,session_id) DO UPDATE SET identity_id=external_requests.identity_id RETURNING id`, id, identity, event.Session.ID, digest("{}")).Scan(&id)
	if e != nil {
		return "", "", e
	}
	if a.Signal == "stop" {
		if _, e = stopExternalRequest(ctx, tx, id); e != nil {
			return "", "", e
		}
	} else {
		body := a.Content.Body
		if len(body) > maxRequestPrompt {
			body = "Additional message exceeded the 128 KiB limit and was not retained."
		}
		tag, e := tx.Exec(ctx, `INSERT INTO external_request_messages(id,request_id,activity_id,body) VALUES($1,$2,$3,$4) ON CONFLICT(request_id,activity_id) DO NOTHING`, uuid.New(), id, a.ID, body)
		if e != nil {
			return "", "", e
		}
		if tag.RowsAffected() > 0 {
			if _, e = tx.Exec(ctx, `SELECT queue_native_linear_activity($1,$2)`, id, "message:"+a.ID); e != nil {
				return "", "", e
			}
		}
	}
	return "processed", "", tx.Commit(ctx)
}

// Definitive app revocation disables new routes and stops linked work; temporary
// provider outages never enter this path. Receipts and immutable inputs survive.
func (s *Service) stopUnavailableIdentity(ctx context.Context, identity string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	var status string
	if e = tx.QueryRow(ctx, `SELECT status FROM provider_identities WHERE id=$1 FOR UPDATE`, identity).Scan(&status); e != nil {
		return e
	}
	if status == "available" {
		return nil
	}
	if _, e = tx.Exec(ctx, `UPDATE linear_request_routes SET enabled=false,generation=generation+1,updated_at=now() WHERE (identity_id=$1 OR project_id IN(SELECT b.project_id FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id WHERE i.id=$1 AND i.provider='github')) AND enabled`, identity); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT id FROM external_requests WHERE (identity_id=$1 OR project_id IN(SELECT b.project_id FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id WHERE i.id=$1 AND i.provider='github')) AND stopped_at IS NULL ORDER BY id`, identity)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if _, e = stopExternalRequest(ctx, tx, id); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Service) reserveExternalEffect(ctx context.Context, run, effect string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer rollback(ctx, tx)
	if e = postgres.ReserveExternalEffect(ctx, tx, uuid.MustParse(run), effect); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) reserveReviewPublication(ctx context.Context, run uuid.UUID, p reviewPublication) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer rollback(ctx, tx)
	if e = postgres.ReserveExternalEffect(ctx, tx, run, "github_review"); e != nil {
		return false, e
	}
	tag, e := tx.Exec(ctx, `UPDATE pr_review_publications SET started=true,body=$3,expected_author_id=$4,updated_at=now() WHERE review_id=$1 AND lease_owner=$2 AND lease_until>now() AND NOT started AND status IN ('pending','retrying')`, p.ID, p.Owner, p.Body, p.Author)
	if e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return tag.RowsAffected() == 1, nil
}
