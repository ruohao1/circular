package integrations_test

import (
	"crypto/sha256"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
	"github.com/ruohao1/circular/internal/webhooks"
	"testing"
	"time"
)

func TestWebhookProviderOutageRecoversAfterRetryBudget(t *testing.T) {
	f, route := agentRequestFixture(t)
	if _, err := f.service.SaveRequestRoute(t.Context(), route); err != nil {
		t.Fatal(err)
	}
	payload := testsupport.LinearAgentEvent(uuid.NewString(), "created", "Recover this accepted request once.")
	f.provider.SetLinearAgentSession(payload["agentSession"].(map[string]any))
	id := acceptFixtureWebhook(t, f, "linear", "fixture-linear", "AgentSessionEvent", payload)
	f.provider.Unavailable.Store(true)
	for attempt := range 10 {
		if _, err := f.pool.Exec(t.Context(), `UPDATE integration_webhook_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
		if worked, err := f.service.ProcessIntegrationWebhook(t.Context()); err != nil || !worked {
			t.Fatalf("accepted delivery stranded at attempt %d: worked=%v err=%v", attempt+1, worked, err)
		}
	}
	var status string
	var attempts, requests int
	var scheduled bool
	if err := f.pool.QueryRow(t.Context(), `SELECT status,attempts,next_attempt_at>now() AND next_attempt_at<=now()+interval '5 minutes',(SELECT count(*) FROM external_requests) FROM integration_webhook_deliveries WHERE id=$1`, id).Scan(&status, &attempts, &scheduled, &requests); err != nil || status != "received" || attempts != 10 || !scheduled || requests != 0 {
		t.Fatal("transient outage did not retain bounded retry", status, attempts, scheduled, requests, err)
	}
	f.provider.Unavailable.Store(false)
	if _, err := f.pool.Exec(t.Context(), `UPDATE integration_webhook_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.ProcessIntegrationWebhook(t.Context()); err != nil || !worked {
		t.Fatal("restored provider did not recover accepted request", worked, err)
	}
	if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || !worked {
		t.Fatal("recovered request did not launch", worked, err)
	}
	acceptFixtureWebhook(t, f, "linear", "fixture-linear", "AgentSessionEvent", payload)
	if _, err := f.service.ProcessIntegrationWebhook(t.Context()); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || worked {
		t.Fatal("recovery duplicated execution", worked, err)
	}
	var runs int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM external_requests),(SELECT count(*) FROM runs)`).Scan(&requests, &runs); err != nil || requests != 1 || runs != 1 {
		t.Fatal("expected one request and run after recovery", requests, runs, err)
	}
}

func acceptFixtureWebhook(t *testing.T, f fixture, provider, client, event string, body any) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	a, e := f.service.AcceptWebhookDelivery(t.Context(), webhooks.VerifiedDelivery{Provider: provider, AppClientID: client, DeliveryID: uuid.NewString(), Event: event, Body: raw, Digest: sha256.Sum256(raw), ReceivedAt: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	return a.ID
}
func TestWebhookReceiverRecoversClaimAndInvalidatesCachedInstallation(t *testing.T) {
	f := githubAppFixture(t)
	repo, _, e := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
	if e != nil {
		t.Fatal(e)
	}
	status, e := f.service.SaveGitHubIdentity(t.Context(), f.project, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/fixture/private-source.git"); e != nil {
		t.Fatal(e)
	}
	receipt := acceptFixtureWebhook(t, f, "github", "fixture-github", "installation", map[string]any{"action": "suspend", "installation": map[string]int{"id": 101}})
	if _, e = f.pool.Exec(t.Context(), `UPDATE integration_webhook_deliveries SET status='processing',lease_owner=$2,lease_until=now()+interval '1 minute' WHERE id=$1`, receipt, uuid.NewString()); e != nil {
		t.Fatal(e)
	}
	other, e := integrations.New(f.pool, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if worked, e := other.ProcessIntegrationWebhook(t.Context()); e != nil || worked {
		t.Fatal("stole a live claim", worked, e)
	}
	f.provider.SuspendedInstallation.Store(true)
	if _, e = f.pool.Exec(t.Context(), `UPDATE integration_webhook_deliveries SET lease_until=now()-interval '1 second' WHERE id=$1`, receipt); e != nil {
		t.Fatal(e)
	}
	if worked, e := other.ProcessIntegrationWebhook(t.Context()); e != nil || !worked {
		t.Fatal("lost restart receipt", worked, e)
	}
	var state string
	if e = f.pool.QueryRow(t.Context(), `SELECT status FROM provider_identities WHERE id=$1`, status.IdentityID).Scan(&state); e != nil || state != "needs_access" {
		t.Fatal("suspended identity still available", state, e)
	}
	if token, e := f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/fixture/private-source.git"); e == nil || token != "" {
		t.Fatal("stale cached token still usable")
	}
	if worked, e := other.ProcessIntegrationWebhook(t.Context()); e != nil || worked {
		t.Fatal("processed twice", worked, e)
	}
}
func TestWebhookForeignAccountsStaleEventsAndExpiredPayloads(t *testing.T) {
	f := githubAppFixture(t)
	identity, e := f.service.SaveGitHubIdentity(t.Context(), f.project, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		provider, client, event string
		body                    any
	}{{"github", "foreign", "installation", map[string]any{"installation": map[string]int{"id": 101}}}, {"github", "fixture-github", "installation", map[string]any{"installation": map[string]int{"id": 999}}}, {"linear", "fixture-linear", "OAuthApp", map[string]any{"action": "revoked", "organizationId": uuid.NewString(), "oauthClientId": "fixture-linear"}}, {"linear", "fixture-linear", "AgentSessionEvent", map[string]any{"action": "created"}}, {"github", "fixture-github", "installation", map[string]any{"action": "deleted", "installation": map[string]int{"id": 101}}}} {
		acceptFixtureWebhook(t, f, c.provider, c.client, c.event, c.body)
		if worked, e := f.service.ProcessIntegrationWebhook(t.Context()); e != nil || !worked {
			t.Fatal(worked, e)
		}
	}
	got, e := f.service.Identity(t.Context(), f.project, "github")
	if e != nil || got.Status != "enabled" || got.IdentityID != identity.IdentityID {
		t.Fatal("stale payload revoked freshly verified installation", got, e)
	}
	id := acceptFixtureWebhook(t, f, "github", "fixture-github", "installation", map[string]any{"installation": map[string]int{"id": 101}})
	if _, e = f.pool.Exec(t.Context(), `UPDATE integration_webhook_deliveries SET payload_expires_at=now()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if worked, e := f.service.ProcessIntegrationWebhook(t.Context()); e != nil || worked {
		t.Fatal("expired input processed", worked, e)
	}
	var payload []byte
	var state string
	var runs int
	if e = f.pool.QueryRow(t.Context(), `SELECT payload,status,(SELECT count(*) FROM runs) FROM integration_webhook_deliveries WHERE id=$1`, id).Scan(&payload, &state, &runs); e != nil || payload != nil || state != "expired" || runs != 0 {
		t.Fatal(state, runs, e)
	}
}
