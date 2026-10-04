package integrations_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"strings"
	"sync"
	"testing"
	"time"
)

func stopPayload(session string) map[string]any {
	p := testsupport.LinearAgentEvent(session, "prompted", "Stop this work")
	p["agentActivity"].(map[string]any)["signal"] = "stop"
	return p
}
func TestStopBeforeCreatedRemainsTombstoneAndFollowupsDoNotLaunch(t *testing.T) {
	f, r := agentRequestFixture(t)
	if _, e := f.service.SaveRequestRoute(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	session := uuid.NewString()
	stop := stopPayload(session)
	id := requestEvent(t, f, stop)
	requestEvent(t, f, stop)
	requestEvent(t, f, testsupport.LinearAgentEvent(session, "created", "late creation"))
	follow := testsupport.LinearAgentEvent(session, "prompted", "Please change the approach")
	requestEvent(t, f, follow)
	requestEvent(t, f, follow)
	q, e := f.service.ExternalRequest(t.Context(), id)
	if e != nil || q.Status != "stopped" || q.RunID != "" || len(q.Messages) != 1 {
		t.Fatal(q, e)
	}
	if worked, e := f.service.ProcessExternalRequest(t.Context()); e != nil || worked {
		t.Fatal("tombstone launched", worked, e)
	}
}
func TestStopRacesLaunchAndCancelsDescendantsOnce(t *testing.T) {
	f, route, request := preparedRequest(t)
	other, e := integrations.New(f.pool, f.config)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
		if errors.Is(e, integrations.ErrRequestUnavailable) {
			e = nil
		}
		errs <- e
	})
	wg.Go(func() { _, e := other.StopExternalRequest(t.Context(), request.ID); errs <- e })
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, e := f.service.ExternalRequest(t.Context(), request.ID)
	if e != nil || got.Status != "stopped" {
		t.Fatal(got, e)
	}
	var active int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE status NOT IN ('succeeded','failed','cancelled')`).Scan(&active); e != nil || active != 0 {
		t.Fatal("stop left active work", active, e)
	}
	// A separate run links a descendant review through the durable session relation.
	f, route, request = preparedRequest(t)
	run, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	child, reviewer := uuid.NewString(), uuid.NewString()
	if _, e = f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Reviewer','fake','Review','{}',true)`, reviewer, route.ProjectID); e != nil {
		t.Fatal(e)
	}
	_, e = f.pool.Exec(t.Context(), `WITH child AS(INSERT INTO runs(id,task_id,agent_id,parent_run_id,backend,status,attempt,external_refs,kind) SELECT $2,task_id,$3,id,'fake','running',2,'{}','pr_review' FROM runs WHERE id=$1 RETURNING id) INSERT INTO pr_reviews(id,project_id,repository_id,source_run_id,run_id,reviewer_id,identity_key,attempt,snapshot) SELECT $4,$5,$6,$1,id,$3,'fixture',1,'{}' FROM child`, run.RunID, child, reviewer, uuid.NewString(), route.ProjectID, route.RepositoryID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.StopExternalRequest(t.Context(), request.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.StopExternalRequest(t.Context(), request.ID); e != nil {
		t.Fatal(e)
	}
	var cancelled, events int
	if e = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs WHERE status='cancelled'),(SELECT count(*) FROM events WHERE type='run.cancelled')`).Scan(&cancelled, &events); e != nil || cancelled != 2 || events != 2 {
		t.Fatal(cancelled, events, e)
	}
}
func TestPublicationFenceStopsBeforeReservationAndPreservesInFlightReceipt(t *testing.T) {
	f, _, request := preparedRequest(t)
	run, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if e = postgres.ReserveExternalEffect(t.Context(), tx, uuid.MustParse(run.RunID), "fixture-write"); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, run.RunID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.StopExternalRequest(t.Context(), request.ID); e != nil {
		t.Fatal(e)
	}
	tx, e = f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	if e = postgres.ReserveExternalEffect(t.Context(), tx, uuid.MustParse(run.RunID), "another-write"); !errors.Is(e, postgres.ErrExternalStopped) {
		t.Fatal("new write allowed after stop", e)
	}
	var status string
	if e = f.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, run.RunID).Scan(&status); e != nil || status != "succeeded" {
		t.Fatal("stop rewrote terminal outcome", status, e)
	}
}
func TestLinearStopDuringActivityReconcilesWithoutNewMutation(t *testing.T) {
	f, _, request := preparedRequest(t)
	var once sync.Once
	f.provider.BeforeLinearActivity = func() {
		once.Do(func() {
			if _, e := f.service.StopExternalRequest(t.Context(), request.ID); e != nil {
				t.Error(e)
			}
		})
	}
	drainActivities(t, f)
	q, e := f.service.ExternalRequest(t.Context(), request.ID)
	if e != nil || q.Status != "stopped" || f.provider.ActivityCreates.Load() != 2 {
		t.Fatal(q.Status, f.provider.ActivityCreates.Load(), e)
	}
	var oldDelivered int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM linear_agent_activities WHERE semantic_key='ack' AND status='delivered'`).Scan(&oldDelivered); e != nil || oldDelivered != 1 {
		t.Fatal("lost reserved receipt", e)
	}
}

func TestRevocationAndScopeLossStopWorkAndDisableRoutes(t *testing.T) {
	for _, mode := range []string{"revoked", "scope_loss"} {
		t.Run(mode, func(t *testing.T) {
			f, route, q := preparedRequest(t)
			run, e := f.service.StartExternalRequest(t.Context(), q.ID, q.InputFingerprint)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "revoked" {
				f.provider.SetLinearOrganization(uuid.NewString())
			} else {
				var stored []byte
				if e = f.pool.QueryRow(t.Context(), `SELECT credentials FROM provider_identities WHERE id=$1`, q.IdentityID).Scan(&stored); e != nil {
					t.Fatal(e)
				}
				block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
				vault, _ := cipher.NewGCM(block)
				plain, e := vault.Open(nil, stored[:vault.NonceSize()], stored[vault.NonceSize():], []byte("provider-identity:"+q.IdentityID))
				if e != nil {
					t.Fatal(e)
				}
				var tok map[string]any
				if e = json.Unmarshal(plain, &tok); e != nil {
					t.Fatal(e)
				}
				tok["expires_at"] = time.Now().Add(-time.Minute)
				plain, _ = json.Marshal(tok)
				nonce := make([]byte, vault.NonceSize())
				if _, e = rand.Read(nonce); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pool.Exec(t.Context(), `UPDATE provider_identities SET credentials=$2 WHERE id=$1`, q.IdentityID, vault.Seal(nonce, nonce, plain, []byte("provider-identity:"+q.IdentityID))); e != nil {
					t.Fatal(e)
				}
				f.provider.SetLinearScopes("read comments:create")
			}
			acceptFixtureWebhook(t, f, "linear", "fixture-linear", "OAuthApp", map[string]any{"action": "revoked", "organizationId": "20000000-0000-4000-8000-000000000004", "oauthClientId": "fixture-linear"})
			if worked, e := f.service.ProcessIntegrationWebhook(t.Context()); e != nil || !worked {
				t.Fatal(worked, e)
			}
			var status string
			var enabled bool
			if e = f.pool.QueryRow(t.Context(), `SELECT status,(SELECT enabled FROM linear_request_routes WHERE id=$2) FROM runs WHERE id=$1`, run.RunID, route.ID).Scan(&status, &enabled); e != nil || status != "cancelled" || enabled {
				t.Fatal(status, enabled, e)
			}
		})
	}
}
func TestStopAcceptedAfterIdentityBecomesUnavailable(t *testing.T) {
	f, _, q := preparedRequest(t)
	if _, e := f.pool.Exec(t.Context(), `UPDATE provider_identities SET status='needs_access' WHERE id=$1`, q.IdentityID); e != nil {
		t.Fatal(e)
	}
	requestEvent(t, f, stopPayload(q.SessionID))
	got, e := f.service.ExternalRequest(t.Context(), q.ID)
	if e != nil || got.Status != "stopped" {
		t.Fatal(got.Status, e)
	}
}

func TestPublicationFenceSerializesWithRouteDisable(t *testing.T) {
	f, route, q := preparedRequest(t)
	run, e := f.service.StartExternalRequest(t.Context(), q.ID, q.InputFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	change, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer change.Rollback(t.Context())
	if _, e = change.Exec(t.Context(), `UPDATE linear_request_routes SET enabled=false WHERE id=$1`, route.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		tx, e := f.pool.Begin(t.Context())
		if e != nil {
			done <- e
			return
		}
		defer tx.Rollback(t.Context())
		e = postgres.ReserveExternalEffect(t.Context(), tx, uuid.MustParse(run.RunID), "test-route-race")
		if e == nil {
			e = tx.Commit(t.Context())
		}
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatalf("write raced past disabling route: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	if e = change.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, postgres.ErrExternalStopped) {
		t.Fatal("reserved after disable", e)
	}
}

func TestFollowupAcknowledgesSavedInstructionsWithoutSteeringRun(t *testing.T) {
	f, _, q := preparedRequest(t)
	requestEvent(t, f, testsupport.LinearAgentEvent(q.SessionID, "prompted", "Change the implementation entirely"))
	drainActivities(t, f)
	var found bool
	for _, a := range f.provider.LinearActivities() {
		if strings.Contains(string(a.Content), "saved") && strings.Contains(string(a.Content), "do not change") {
			found = true
		}
	}
	if !found {
		t.Fatal("follow-up falsely implied instructions were applied")
	}
}
