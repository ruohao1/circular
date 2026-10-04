package integrations_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLinearActivityPermissionRestorationRetriesSameUUID(t *testing.T) {
	f, _, request := preparedRequest(t)
	var id string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM linear_agent_activities WHERE request_id=$1 AND semantic_key='ack'`, request.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.provider.ActivityReject.Store(http.StatusBadRequest)
	if worked, err := f.service.ProcessLinearActivity(t.Context()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var started bool
	var status string
	if err := f.pool.QueryRow(t.Context(), `SELECT started,status FROM linear_agent_activities WHERE id=$1`, id).Scan(&started, &status); err != nil || started || status != "pending" {
		t.Fatalf("rejected activity must remain retryable: started=%v status=%s err=%v", started, status, err)
	}
	// Restoring permission allows the durable reservation to be retried by a
	// fresh consumer, without allocating another activity identity.
	f.provider.ActivityReject.Store(0)
	service, err := integrations.New(f.pool, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	var receipt string
	if err := f.pool.QueryRow(t.Context(), `SELECT status,receipt_id FROM linear_agent_activities WHERE id=$1`, id).Scan(&status, &receipt); err != nil || status != "delivered" || receipt != id {
		t.Fatalf("restored permission did not deliver the same activity: status=%s receipt=%s err=%v", status, receipt, err)
	}
	activities := f.provider.LinearActivities()
	if len(activities) != 1 || activities[0].ID != id || f.provider.ActivityCreates.Load() != 1 {
		t.Fatal("restored permission duplicated the native activity", activities)
	}
}

func drainActivities(t *testing.T, f fixture) {
	t.Helper()
	for range 20 {
		worked, e := f.service.ProcessLinearActivity(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			return
		}
	}
	t.Fatal("activity consumer did not settle")
}
func retryActivitiesNow(t *testing.T, f fixture) {
	t.Helper()
	if _, e := f.pool.Exec(t.Context(), `UPDATE linear_agent_activities SET next_attempt_at=now(),lease_until=NULL`); e != nil {
		t.Fatal(e)
	}
}
func TestLinearActivityLostResponseRecoversSameUUIDAndNoOrdinaryComments(t *testing.T) {
	f, _, request := preparedRequest(t)
	start := time.Now()
	f.provider.LoseActivityResponse.Store(true)
	if worked, e := f.service.ProcessLinearActivity(t.Context()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	if f.provider.ActivityCreates.Load() != 1 {
		t.Fatal("missing native acknowledgement")
	}
	other, e := integrations.New(f.pool, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.service = other
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	if f.provider.ActivityCreates.Load() != 1 || time.Since(start) > 10*time.Second {
		t.Fatal("duplicate or slow acknowledgement")
	}
	activities := f.provider.LinearActivities()
	if len(activities) != 1 || activities[0].SessionID != request.SessionID || activities[0].ActorID != testsupport.ProviderAppActorID {
		t.Fatal(activities)
	}
	if _, e = f.service.SetLinearRunUpdates(t.Context(), f.project, true); e != nil {
		t.Fatal(e)
	}
	run, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='running' WHERE id=$1`, run.RunID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, run.RunID); e != nil {
		t.Fatal(e)
	}
	drainActivities(t, f)
	var ordinary int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM linear_run_updates WHERE run_id=$1`, run.RunID).Scan(&ordinary); e != nil || ordinary != 0 {
		t.Fatal("session run produced duplicate ordinary comments", ordinary, e)
	}
	var delivered []string
	for _, a := range f.provider.LinearActivities() {
		delivered = append(delivered, string(a.Content))
	}
	joined := strings.Join(delivered, " ")
	if !strings.Contains(joined, "succeeded") || !strings.Contains(joined, "Request engineer") {
		t.Fatal(joined)
	}
	if strings.Contains(joined, `"body":"Queued`) {
		t.Fatal("late queue status after completion")
	}
}
func TestLinearActivityUncertainReceiptNeverCreatesAgain(t *testing.T) {
	f, _, request := preparedRequest(t)
	f.provider.LoseActivityResponse.Store(true)
	if _, e := f.service.ProcessLinearActivity(t.Context()); e != nil {
		t.Fatal(e)
	}
	first := f.provider.LinearActivities()[0]
	for _, field := range []string{"hidden", "actor", "session", "body"} {
		t.Run(field, func(t *testing.T) {
			f.provider.HideActivities.Store(field == "hidden")
			wrong := first
			switch field {
			case "actor":
				wrong.ActorID = uuid.NewString()
			case "session":
				wrong.SessionID = uuid.NewString()
			case "body":
				wrong.Content = json.RawMessage(`{"type":"response","body":"not our reply"}`)
			}
			f.provider.SetLinearActivity(wrong)
			retryActivitiesNow(t, f)
			if _, e := f.service.ProcessLinearActivity(t.Context()); e != nil {
				t.Fatal(e)
			}
			r, e := f.service.ExternalRequest(t.Context(), request.ID)
			if e != nil || r.DeliveryStatus != "uncertain" || f.provider.ActivityCreates.Load() != 1 {
				t.Fatal(r.DeliveryStatus, e)
			}
		})
	}
	f.provider.HideActivities.Store(false)
	f.provider.SetLinearActivity(first)
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	r, e := f.service.ExternalRequest(t.Context(), request.ID)
	if e != nil || r.DeliveryStatus != "delivered" {
		t.Fatal(r, e)
	}
}

func TestLinearActivityRecoversProviderFormattedReceiptWithoutDuplicate(t *testing.T) {
	f, _, request := preparedRequest(t)
	f.provider.LoseActivityResponse.Store(true)
	if _, err := f.service.ProcessLinearActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	activity := f.provider.LinearActivities()[0]
	activity.Content, _ = json.Marshal(map[string]string{
		"type": "elicitation",
		"body": "Review the request in Circular before starting.\n\n[Open request](<" + f.service.WebURL() + "/requests/" + request.ID + ">)",
	})
	f.provider.SetLinearActivity(activity)
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	current, err := f.service.ExternalRequest(t.Context(), request.ID)
	if err != nil || current.DeliveryStatus != "delivered" {
		t.Fatalf("provider-formatted receipt did not recover: %s, %v", current.DeliveryStatus, err)
	}
	if f.provider.ActivityCreates.Load() != 1 {
		t.Fatal("receipt recovery created a duplicate activity")
	}
}

func TestLinearActivityRecoversProviderListReceiptWithoutDuplicate(t *testing.T) {
	f, _, request := preparedRequest(t)
	frozen := json.RawMessage(`{"type":"response","body":"Checks performed:\n\n- Regression passed\n- Database passed"}`)
	if _, err := f.pool.Exec(t.Context(), `UPDATE linear_agent_activities SET content=$2 WHERE request_id=$1 AND semantic_key='ack'`, request.ID, frozen); err != nil {
		t.Fatal(err)
	}
	f.provider.LoseActivityResponse.Store(true)
	if _, err := f.service.ProcessLinearActivity(t.Context()); err != nil {
		t.Fatal(err)
	}
	activity := f.provider.LinearActivities()[0]
	activity.Content = json.RawMessage(`{"type":"response","body":"Checks performed:\n\n* Regression passed\n* Database passed"}`)
	f.provider.SetLinearActivity(activity)
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	current, err := f.service.ExternalRequest(t.Context(), request.ID)
	if err != nil || current.DeliveryStatus != "delivered" {
		t.Fatalf("provider-formatted list receipt did not recover: %s, %v", current.DeliveryStatus, err)
	}
	if f.provider.ActivityCreates.Load() != 1 {
		t.Fatal("receipt recovery created a duplicate activity")
	}
}
func TestSessionDeliveryHeartbeatCoalescesAndProviderBackoff(t *testing.T) {
	f, _, request := preparedRequest(t)
	drainActivities(t, f)
	run, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='running' WHERE id=$1`, run.RunID); e != nil {
		t.Fatal(e)
	}
	f.provider.ActivityReject.Store(429)
	if _, e = f.service.ProcessLinearActivity(t.Context()); e != nil {
		t.Fatal(e)
	}
	var scheduled bool
	if e = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM linear_agent_activities WHERE status='pending' AND next_attempt_at>now())`).Scan(&scheduled); e != nil || !scheduled {
		t.Fatal("missing backoff", e)
	}
	f.provider.ActivityReject.Store(0)
	retryActivitiesNow(t, f)
	drainActivities(t, f)
	count := f.provider.ActivityCreates.Load()
	drainActivities(t, f)
	if f.provider.ActivityCreates.Load() != count {
		t.Fatal("heartbeat spam within a minute")
	}
}
