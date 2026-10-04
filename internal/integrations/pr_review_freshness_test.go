package integrations_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/integrations"
)

func TestReviewFreshnessIsCoalescedAcrossProcessesAndNeverUsesStaleSuccess(t *testing.T) {
	f, r := completedReview(t)
	other, err := integrations.New(f.pool, f.config)
	if err != nil {
		t.Fatal(err)
	}
	before := f.provider.ReviewReads.Load()
	var group sync.WaitGroup
	for _, service := range []*integrations.Service{f.service, other} {
		group.Go(func() {
			if _, err := service.ProcessPRReviewFreshness(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if f.provider.ReviewReads.Load() != before+1 {
		t.Fatal("duplicate freshness reads")
	}
	got, err := f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.Status != "current" || f.provider.ReviewReads.Load() != before+1 {
		t.Fatal(got.Freshness, err)
	}
	checked := *got.Freshness.CheckedAt
	dueFreshness := func() {
		if _, err := f.pool.Exec(t.Context(), `UPDATE pr_review_freshness SET next_check_at=now()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
	}
	f.provider.Unavailable.Store(true)
	dueFreshness()
	got, err = f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.Status != "unavailable" || !got.Freshness.CheckedAt.Equal(checked) {
		t.Fatal("old success advertised current", got.Freshness, err)
	}
	f.provider.Unavailable.Store(false)
	pr := r.Snapshot.PR
	pr.BaseSHA = strings.Repeat("c", 40)
	f.provider.SetReviewPR(pr, "open")
	dueFreshness()
	got, err = f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.Status != "outdated" || got.Snapshot.PR.BaseSHA == pr.BaseSHA {
		t.Fatal("captured identity changed", got.Freshness, err)
	}
	f.provider.SetReviewPR(pr, "merged")
	dueFreshness()
	got, err = f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.PRState != "merged" {
		t.Fatal(got.Freshness, err)
	}
	dueFreshness()
	if worked, err := other.ProcessPRReviewFreshness(t.Context()); worked || err != nil {
		t.Fatal("merged PR kept polling", err)
	}
	f.provider.SetReviewPR(pr, "open")
	got, err = f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.PRState != "open" {
		t.Fatal("explicit refresh cannot discover reopen", got.Freshness, err)
	}
	var runs int
	var next time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs WHERE kind='pr_review'),next_check_at FROM pr_review_freshness LIMIT 1`).Scan(&runs, &next); err != nil || runs != 1 || time.Until(next) < 50*time.Second {
		t.Fatal(runs, next, err)
	}
}

func TestReviewFreshnessHonorsProviderBackoff(t *testing.T) {
	f, r := completedReview(t)
	f.provider.ReviewReadReject.Store(429)
	got, err := f.service.RefreshPRReview(t.Context(), r.ID.String())
	if err != nil || got.Freshness.Status != "unavailable" {
		t.Fatal(got.Freshness, err)
	}
	var backoff bool
	if err := f.pool.QueryRow(t.Context(), `SELECT next_check_at>now()+interval '150 seconds' FROM pr_review_freshness LIMIT 1`).Scan(&backoff); err != nil || !backoff {
		t.Fatal("provider backoff ignored", backoff, err)
	}
	before := f.provider.ReviewReads.Load()
	if _, err := f.service.ProcessPRReviewPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.provider.ReviewReads.Load() != before || f.provider.ReviewCreates.Load() != 0 {
		t.Fatal("publication bypassed rate backoff")
	}
}
