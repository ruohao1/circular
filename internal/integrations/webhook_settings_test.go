package integrations_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/webhooks"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookSettingsRejectNonPublicOrigins(t *testing.T) {
	f := setup(t)
	for _, origin := range []string{"http://receiver.test", "https://localhost", "https://receiver.localhost", "https://127.0.0.1", "https://[::1]", "https://10.2.3.4", "https://169.254.0.1", "https://user:pass@receiver.test", "https://receiver.test/path", "https://receiver.test?x=1", "https://receiver.test#fragment", "https://receiver.local", "https://2130706433"} {
		if _, e := f.service.SaveWebhookSettings(t.Context(), "linear", origin, "fixture-linear-signing-secret"); e == nil {
			t.Fatal("accepted", origin)
		}
	}
}
func TestWebhookSettingsWaitingVerifiedAndSecretRotation(t *testing.T) {
	f := setup(t)
	key := "fixture-linear-signing-secret"
	got, e := f.service.SaveWebhookSettings(t.Context(), "linear", "https://receiver.example", key)
	if e != nil || got.Status != "waiting" {
		t.Fatal(got, e)
	}
	public, _ := json.Marshal(got)
	if bytes.Contains(public, []byte(key)) {
		t.Fatal("secret leaked")
	}
	handler := webhooks.NewHandler(f.service, f.service, time.Now)
	deliver := func(secret string, want int) {
		t.Helper()
		raw := []byte(`{"webhookTimestamp":` + timeNumber() + `}`)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		r := httptest.NewRequest("POST", "/webhooks/linear", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Linear-Delivery", uuid.NewString())
		r.Header.Set("Linear-Event", "Issue")
		r.Header.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	deliver(key, 200)
	got, e = f.service.WebhookSettings(t.Context(), "linear")
	if e != nil || got.Status != "receiving" || got.LastVerifiedAt == nil {
		t.Fatal(got, e)
	}
	newKey := "rotated-fixture-linear-secret"
	if _, e = f.service.SaveWebhookSettings(t.Context(), "linear", "https://receiver.example", newKey); e != nil {
		t.Fatal(e)
	}
	deliver(key, 200)
	deliver(newKey, 200)
	if _, e = f.pool.Exec(t.Context(), `UPDATE integration_webhook_settings SET previous_until=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	deliver(key, 401)
}
func timeNumber() string {
	return json.Number(strings.TrimSpace(string(mustJSON(time.Now().UnixMilli())))).String()
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func TestWebhookGitHubConfigureAndFailurePreserveWorkingSecret(t *testing.T) {
	f := githubAppFixture(t)
	if _, e := f.service.SaveGitHubIdentity(t.Context(), f.project, nil); e != nil {
		t.Fatal(e)
	}
	old := "original-fixture-hook-secret"
	got, e := f.service.SaveWebhookSettings(t.Context(), "github", "https://receiver.example", old)
	if e != nil || got.Status != "waiting" {
		t.Fatal(got, e)
	}
	key, e := f.service.WebhookSigningKey(t.Context(), "github")
	if e != nil || string(key.Current) != old {
		t.Fatal(e)
	}
	config := f.config
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/app/hook/config" && r.Method == "PATCH" {
			return &http.Response{StatusCode: 403, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	broken, e := integrations.New(f.pool, config)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = broken.SaveWebhookSettings(t.Context(), "github", "https://new.example", "new-fixture-hook-secret-rotate"); e == nil {
		t.Fatal("provider failure hidden")
	}
	key, e = f.service.WebhookSigningKey(t.Context(), "github")
	if e != nil || string(key.Current) != old {
		t.Fatal("discarded working signing key", e)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
