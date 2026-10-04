package integrations_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/webhooks"
)

func TestWebhookInboxDurableEncryptedDeduplication(t *testing.T) {
	f := setup(t)
	raw := []byte(`{"private":"issue content"}`)
	d := webhooks.VerifiedDelivery{Provider: "linear", AppClientID: "fixture-linear", DeliveryID: uuid.NewString(), Event: "Issue", Body: raw, Digest: sha256.Sum256(raw), ReceivedAt: time.Now()}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() { a, e := f.service.AcceptWebhookDelivery(t.Context(), d); ids <- a.ID; failures <- e })
	}
	wg.Wait()
	close(ids)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id == "" || id != first {
			t.Fatal("duplicate receipt", id, first)
		}
	}
	var payload []byte
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT payload FROM integration_webhook_deliveries WHERE id=$1`, first).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("issue content")) || bytes.Equal(payload, raw) {
		t.Fatal("payload plaintext")
	}
	d.Body = []byte(`{"private":"changed"}`)
	d.Digest = sha256.Sum256(d.Body)
	if _, err := f.service.AcceptWebhookDelivery(t.Context(), d); !errors.Is(err, webhooks.ErrDeliveryConflict) {
		t.Fatal("changed delivery accepted", err)
	}
	d.Body = raw
	d.Digest = sha256.Sum256(raw)
	d.DeliveryID = uuid.NewString()
	a, err := f.service.AcceptWebhookDelivery(t.Context(), d)
	if err != nil || a.ID == first {
		t.Fatal("transport dedup swallowed distinct receipt", err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_webhook_deliveries`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE integration_webhook_deliveries ADD CONSTRAINT fixture_insert_failure CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	d.DeliveryID = uuid.NewString()
	a, err = f.service.AcceptWebhookDelivery(t.Context(), d)
	if err == nil || a.ID != "" {
		t.Fatal("acknowledged failed persistence")
	}
}
