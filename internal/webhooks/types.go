// Package webhooks exposes only the verified, durable provider ingress boundary.
package webhooks

import (
	"context"
	"errors"
	"time"
)

var ErrDeliveryConflict = errors.New("delivery ID was already used with different content")

type SigningKey struct {
	AppClientID       string
	Current, Previous []byte
	PreviousUntil     time.Time
}
type VerifiedDelivery struct {
	Provider, AppClientID, DeliveryID, Event string
	Body                                     []byte
	Digest                                   [32]byte
	ReceivedAt                               time.Time
}
type Acceptance struct {
	ID        string
	Duplicate bool
}
type SigningKeys interface {
	WebhookSigningKey(context.Context, string) (SigningKey, error)
}
type Inbox interface {
	AcceptWebhookDelivery(context.Context, VerifiedDelivery) (Acceptance, error)
}
