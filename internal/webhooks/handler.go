package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaxBodyBytes = 1 << 20

func NewHandler(keys SigningKeys, inbox Inbox, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ready":true}`)
	})
	for _, provider := range []string{"github", "linear"} {
		mux.HandleFunc("POST /webhooks/"+provider, func(w http.ResponseWriter, r *http.Request) { receive(w, r, provider, keys, inbox, now) })
	}
	return http.TimeoutHandler(mux, 4*time.Second, "receiver unavailable")
}
func receive(w http.ResponseWriter, r *http.Request, provider string, keys SigningKeys, inbox Inbox, now func() time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int) { http.Error(w, http.StatusText(code), code) }
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(400)
		return
	}
	delivery, event, sig := r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256")
	if provider == "linear" {
		delivery, event, sig = r.Header.Get("Linear-Delivery"), r.Header.Get("Linear-Event"), r.Header.Get("Linear-Signature")
	}
	parsed, err := uuid.Parse(delivery)
	if err != nil || len(delivery) != 36 || parsed == uuid.Nil || event == "" || len(event) > 100 || strings.ContainsAny(event, "\r\n\x00") {
		fail(400)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			fail(413)
		} else {
			fail(400)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	key, err := keys.WebhookSigningKey(ctx, provider)
	if err != nil || key.AppClientID == "" || len(key.Current) == 0 {
		fail(503)
		return
	}
	if provider == "github" {
		if !strings.HasPrefix(sig, "sha256=") {
			fail(401)
			return
		}
		sig = strings.TrimPrefix(sig, "sha256=")
	}
	supplied, err := hex.DecodeString(sig)
	if err != nil || len(supplied) != sha256.Size {
		fail(401)
		return
	}
	valid := func(secret []byte) bool {
		if len(secret) == 0 {
			return false
		}
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(body)
		return hmac.Equal(mac.Sum(nil), supplied)
	}
	received := now()
	if !valid(key.Current) && !(received.Before(key.PreviousUntil) && valid(key.Previous)) {
		fail(401)
		return
	}
	if !json.Valid(body) {
		fail(400)
		return
	}
	var envelope struct {
		Timestamp int64 `json:"webhookTimestamp"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		fail(400)
		return
	}
	if provider == "linear" {
		delta := received.Sub(time.UnixMilli(envelope.Timestamp))
		if envelope.Timestamp == 0 || delta > time.Minute || delta < -time.Minute {
			fail(400)
			return
		}
	}
	_, err = inbox.AcceptWebhookDelivery(ctx, VerifiedDelivery{Provider: provider, AppClientID: key.AppClientID, DeliveryID: parsed.String(), Event: event, Body: body, Digest: sha256.Sum256(body), ReceivedAt: received})
	if errors.Is(err, ErrDeliveryConflict) {
		fail(409)
		return
	}
	if err != nil {
		fail(503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"accepted":true}`)
}
