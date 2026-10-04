package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/webhooks"
)

// Only serialized inside the encrypted vault; never a public DTO.
type webhookSecrets struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

func webhookKeyAAD(provider, client string) string {
	return "webhook-settings:" + provider + ":" + client
}
func (s *Service) WebhookSigningKey(ctx context.Context, provider string) (webhooks.SigningKey, error) {
	if provider != "github" && provider != "linear" {
		return webhooks.SigningKey{}, ErrConfiguration
	}
	current, err := s.resolve(ctx, provider)
	if err != nil {
		return webhooks.SigningKey{}, err
	}
	client := current.app(provider).ClientID
	var encrypted []byte
	var until *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT signing_keys,previous_until FROM integration_webhook_settings WHERE provider=$1 AND app_client_id=$2`, provider, client).Scan(&encrypted, &until); err != nil {
		return webhooks.SigningKey{}, err
	}
	plain, err := s.open(encrypted, webhookKeyAAD(provider, client))
	if err != nil {
		return webhooks.SigningKey{}, ErrConfiguration
	}
	var secrets webhookSecrets
	if json.Unmarshal(plain, &secrets) != nil || secrets.Current == "" {
		return webhooks.SigningKey{}, ErrConfiguration
	}
	key := webhooks.SigningKey{AppClientID: client, Current: []byte(secrets.Current), Previous: []byte(secrets.Previous)}
	if until != nil {
		key.PreviousUntil = *until
	}
	return key, nil
}
func (s *Service) AcceptWebhookDelivery(ctx context.Context, d webhooks.VerifiedDelivery) (webhooks.Acceptance, error) {
	if (d.Provider != "github" && d.Provider != "linear") || d.AppClientID == "" || len(d.Body) > webhooks.MaxBodyBytes || sha256.Sum256(d.Body) != d.Digest || d.ReceivedAt.IsZero() {
		return webhooks.Acceptance{}, ErrAppInput
	}
	id := uuid.NewString()
	encrypted, err := s.seal(d.Body, "webhook-delivery:"+id)
	if err != nil {
		return webhooks.Acceptance{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return webhooks.Acceptance{}, err
	}
	defer rollback(ctx, tx)
	priority := 10
	if d.Provider == "linear" && d.Event == "AgentSessionEvent" {
		var event linearSessionEvent
		if json.Unmarshal(d.Body, &event) == nil && event.Activity != nil && event.Activity.Signal == "stop" {
			priority = 0
		}
	}
	result, err := tx.Exec(ctx, `INSERT INTO integration_webhook_deliveries(id,provider,app_client_id,delivery_id,event,body_sha256,payload,received_at,payload_expires_at,priority) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(provider,app_client_id,delivery_id) DO NOTHING`, id, d.Provider, d.AppClientID, d.DeliveryID, d.Event, d.Digest[:], encrypted, d.ReceivedAt, d.ReceivedAt.Add(7*24*time.Hour), priority)
	if err != nil {
		return webhooks.Acceptance{}, err
	}
	duplicate := result.RowsAffected() == 0
	if duplicate {
		var prior []byte
		var event string
		err = tx.QueryRow(ctx, `SELECT id,body_sha256,event FROM integration_webhook_deliveries WHERE provider=$1 AND app_client_id=$2 AND delivery_id=$3`, d.Provider, d.AppClientID, d.DeliveryID).Scan(&id, &prior, &event)
		if err != nil {
			return webhooks.Acceptance{}, err
		}
		if !bytes.Equal(prior, d.Digest[:]) || event != d.Event {
			return webhooks.Acceptance{}, webhooks.ErrDeliveryConflict
		}
	}
	if !duplicate {
		_, err = tx.Exec(ctx, `UPDATE integration_webhook_settings SET last_verified_at=GREATEST(COALESCE(last_verified_at,$3),$3) WHERE provider=$1 AND app_client_id=$2`, d.Provider, d.AppClientID, d.ReceivedAt)
		if err != nil {
			return webhooks.Acceptance{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return webhooks.Acceptance{}, err
	}
	return webhooks.Acceptance{ID: id, Duplicate: duplicate}, nil
}

type webhookClaim struct {
	ID, Provider, ClientID, Event, Owner string
	Body                                 []byte
	ReceivedAt                           time.Time
	Attempts                             int
}

func (s *Service) claimWebhook(ctx context.Context) (webhookClaim, error) {
	// Retention preserves immutable receipt digests, but expired inputs can never execute.
	_, err := s.pool.Exec(ctx, `UPDATE integration_webhook_deliveries SET payload=NULL,status=CASE WHEN status IN ('received','processing') THEN 'expired' ELSE status END,lease_owner=NULL,lease_until=NULL WHERE payload IS NOT NULL AND payload_expires_at<=now()`)
	if err != nil {
		return webhookClaim{}, err
	}
	c := webhookClaim{Owner: uuid.NewString()}
	var encrypted []byte
	err = s.pool.QueryRow(ctx, `WITH candidate AS (SELECT id FROM integration_webhook_deliveries WHERE status IN ('received','processing') AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) AND payload_expires_at>now() ORDER BY priority,received_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE integration_webhook_deliveries d SET status='processing',lease_owner=$1,lease_until=now()+interval '30 seconds',attempts=d.attempts+1 FROM candidate c WHERE d.id=c.id RETURNING d.id,d.provider,d.app_client_id,d.event,d.payload,d.received_at,d.attempts`, c.Owner).Scan(&c.ID, &c.Provider, &c.ClientID, &c.Event, &encrypted, &c.ReceivedAt, &c.Attempts)
	if err != nil {
		return c, err
	}
	c.Body, err = s.open(encrypted, "webhook-delivery:"+c.ID)
	return c, err
}
func (s *Service) finishWebhook(ctx context.Context, c webhookClaim, status, reason string, retry bool) error {
	if retry {
		// The provider already received an acceptance receipt. Transient
		// verification failures remain recoverable until the payload expires.
		status = "received"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integration_webhook_deliveries SET status=$3,last_error=$4,lease_owner=NULL,lease_until=NULL,next_attempt_at=now()+$5::interval WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, c.ID, c.Owner, status, reason, incomingRetryDelay(c.Attempts).String())
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("webhook lease no longer owned")
	}
	return err
}

func incomingRetryDelay(attempt int) time.Duration {
	return min(time.Duration(1<<min(max(attempt-1, 0), 5))*10*time.Second, 5*time.Minute)
}

var _ webhooks.SigningKeys = (*Service)(nil)
var _ webhooks.Inbox = (*Service)(nil)
