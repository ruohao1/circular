package webhooks_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/webhooks"
)

type memoryInbox struct {
	received []webhooks.VerifiedDelivery
	err      error
	key      webhooks.SigningKey
}

func (m *memoryInbox) WebhookSigningKey(context.Context, string) (webhooks.SigningKey, error) {
	return m.key, nil
}
func (m *memoryInbox) AcceptWebhookDelivery(_ context.Context, d webhooks.VerifiedDelivery) (webhooks.Acceptance, error) {
	if m.err != nil {
		return webhooks.Acceptance{}, m.err
	}
	m.received = append(m.received, d)
	return webhooks.Acceptance{ID: uuid.NewString()}, nil
}
func signature(key, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func TestWebhookVerification(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	key := []byte("fixture-signing-secret-32-bytes-long")
	for _, tc := range []struct {
		name, provider string
		want           int
		mutate         func(*http.Request)
	}{
		{"github raw", "github", 200, nil}, {"linear signed timestamp", "linear", 200, nil},
		{"missing signature", "github", 401, func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") }},
		{"invalid hex", "linear", 401, func(r *http.Request) { r.Header.Set("Linear-Signature", "invalid") }},
		{"changed bytes", "github", 401, func(r *http.Request) {
			r.Header.Set("X-Hub-Signature-256", "sha256="+signature(key, []byte(`{"action":"ping"}`)))
		}},
		{"invalid delivery", "github", 400, func(r *http.Request) { r.Header.Set("X-GitHub-Delivery", "arbitrary") }},
		{"wrong content type", "github", 400, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf("{ \"action\": \"ping\", \"webhookTimestamp\": %d }", now.UnixMilli()))
			m := &memoryInbox{key: webhooks.SigningKey{AppClientID: "trusted-client", Current: key}}
			r := httptest.NewRequest("POST", "/webhooks/"+tc.provider, bytes.NewReader(raw))
			r.Header.Set("Content-Type", "application/json")
			if tc.provider == "github" {
				r.Header.Set("X-Hub-Signature-256", "sha256="+signature(key, raw))
				r.Header.Set("X-GitHub-Delivery", uuid.NewString())
				r.Header.Set("X-GitHub-Event", "ping")
			} else {
				r.Header.Set("Linear-Signature", signature(key, raw))
				r.Header.Set("Linear-Delivery", uuid.NewString())
				r.Header.Set("Linear-Event", "Issue")
			}
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			webhooks.NewHandler(m, m, func() time.Time { return now }).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.want == 200 {
				if len(m.received) != 1 || !bytes.Equal(m.received[0].Body, raw) || m.received[0].AppClientID != "trusted-client" {
					t.Fatal("lost verified bytes or trusted client")
				}
			} else if len(m.received) != 0 {
				t.Fatal("invalid input persisted")
			}
		})
	}
}
func TestWebhookLimitsReplayStorageAndRotation(t *testing.T) {
	now := time.Now()
	key := []byte("current-key")
	old := []byte("previous-key")
	for _, tc := range []struct {
		name     string
		raw      []byte
		key      []byte
		expired  bool
		storeErr error
		want     int
	}{
		{"oversized", bytes.Repeat([]byte("x"), 1024*1024+1), key, false, nil, 413},
		{"unsigned timestamp cannot rescue replay", []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, now.Add(-2*time.Minute).UnixMilli())), key, false, nil, 400},
		{"database unavailable", []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, now.UnixMilli())), key, false, errors.New("private database password"), 503},
		{"duplicate body conflict", []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, now.UnixMilli())), key, false, webhooks.ErrDeliveryConflict, 409},
		{"previous key overlap", []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, now.UnixMilli())), old, false, nil, 200},
		{"expired previous key", []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, now.UnixMilli())), old, true, nil, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			until := now.Add(time.Minute)
			if tc.expired {
				until = now.Add(-time.Minute)
			}
			m := &memoryInbox{err: tc.storeErr, key: webhooks.SigningKey{AppClientID: "app", Current: key, Previous: old, PreviousUntil: until}}
			r := httptest.NewRequest("POST", "/webhooks/linear", bytes.NewReader(tc.raw))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Linear-Signature", signature(tc.key, tc.raw))
			r.Header.Set("Linear-Delivery", uuid.NewString())
			r.Header.Set("Linear-Event", "Issue")
			r.Header.Set("Linear-Timestamp", fmt.Sprint(now.UnixMilli()))
			w := httptest.NewRecorder()
			webhooks.NewHandler(m, m, func() time.Time { return now }).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if bytes.Contains(w.Body.Bytes(), []byte("password")) {
				t.Fatal("private error leaked")
			}
		})
	}
}
func TestReceiverExposesOnlyWebhooksAndHealth(t *testing.T) {
	m := &memoryInbox{}
	h := webhooks.NewHandler(m, m, time.Now)
	for _, path := range []string{"/api/v1/runs", "/mcp", "/api/v1/integrations/github/callback", "/artifacts", "/webhooks/other"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
