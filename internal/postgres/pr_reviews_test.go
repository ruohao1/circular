package postgres_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestReviewLaunchDeduplicatesAcrossKeysAndRejectsChangedReplay(t *testing.T) {
	pool := testsupport.Database(t)
	snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	store := postgres.NewPRReviewStore(pool)
	request := prreviews.LaunchRequest{
		RequestKey: uuid.New(), ReviewerID: snapshot.Reviewer.AgentID,
		ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal",
	}
	results := make([]prreviews.Review, 12)
	failures := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			req := request
			if i > 0 {
				req.RequestKey = uuid.New()
			}
			results[i], failures[i] = store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: req})
		})
	}
	wg.Wait()
	for i := range results {
		if failures[i] != nil || results[i].ID != results[0].ID || results[i].RunID != results[0].RunID {
			t.Fatalf("launch %d was not the same review: %v", i, failures[i])
		}
	}
	request.Mode, request.PreviousReviewID = "again", results[0].ID
	if _, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: request}); !errors.Is(err, prreviews.ErrRequestConflict) {
		t.Fatalf("changed request key accepted: %v", err)
	}
	request.RequestKey = uuid.New()
	again, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: request})
	if err != nil || again.ID == results[0].ID || again.PreviousReviewID == nil || *again.PreviousReviewID != results[0].ID {
		t.Fatalf("explicit new attempt lost its identity: %v", err)
	}
	replay, err := store.Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: request})
	if err != nil || replay.ID != again.ID {
		t.Fatalf("lost-response retry started another attempt: %v", err)
	}
}

func TestReviewProvisioningUsesFrozenAgentAndTask(t *testing.T) {
	pool := testsupport.Database(t)
	snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ReviewerID: snapshot.Reviewer.AgentID, ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE agents SET instructions='Changed',backend_config='{"model":"gpt-5.6-terra","reasoning_effort":"high"}' WHERE id=$1`, snapshot.Reviewer.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE tasks SET title='Changed',description='Changed' WHERE id=$1`, snapshot.TaskID); err != nil {
		t.Fatal(err)
	}
	acquire(t, postgres.NewQueue(pool), "review-owner")
	resources, err := postgres.NewResources(pool, "review-owner")
	if err != nil {
		t.Fatal(err)
	}
	err = resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
		inputs, err := r.ProvisioningContext()
		if err != nil {
			return err
		}
		if inputs.TaskTitle != snapshot.TaskTitle || inputs.TaskDescription != snapshot.TaskDescription || inputs.Instructions != snapshot.Reviewer.Instructions || inputs.BaseRef != snapshot.PR.HeadSHA {
			t.Fatalf("review inputs were not frozen: %+v", inputs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReviewTransitionsNeverQueueCodingDelivery(t *testing.T) {
	pool := testsupport.Database(t)
	snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO github_run_delivery_settings(project_id,enabled) VALUES($1,true)`, snapshot.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO linear_run_update_settings(project_id,enabled) VALUES($1,true)`, snapshot.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO integrations(id,project_id,provider,config,enabled) VALUES($1,$2,'linear','{"account_id":"linear-workspace","account_url":"https://linear.app/fixture"}',true)`, uuid.New(), snapshot.ProjectID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "succeeded"} {
		if _, err = pool.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, review.RunID, status); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM github_run_deliveries WHERE run_id=$1)+(SELECT count(*) FROM linear_run_updates WHERE run_id=$1)`, review.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatal("review queued coding publication", count, err)
	}
}

func TestReviewContextIsLeaseFencedAndSnapshotBound(t *testing.T) {
	pool := testsupport.Database(t)
	snap := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snap, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snap.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	acquire(t, postgres.NewQueue(pool), "review-context-owner")
	store, err := postgres.NewResources(pool, "review-context-owner")
	if err != nil {
		t.Fatal(err)
	}
	value := prreviews.Context{ReviewID: review.ID, RunID: review.RunID, Snapshot: snap, MergeBaseSHA: snap.PR.BaseSHA, DiffSHA256: strings.Repeat("a", 64), Files: []prreviews.ChangedFile{}, Evidence: []prreviews.Evidence{}, Limitations: []string{}}
	hash, _ := prreviews.Fingerprint(value)
	for range 2 {
		if err = store.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.PersistReviewContext(value, hash) }); err != nil {
			t.Fatal(err)
		}
	}
	value.Snapshot.TaskTitle = "forged"
	value.Snapshot, _ = prreviews.SealSnapshot(value.Snapshot)
	changedHash, _ := prreviews.Fingerprint(value)
	if err = store.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { return r.PersistReviewContext(value, changedHash) }); !errors.Is(err, postgres.ErrResourceConflict) {
		t.Fatal("replaced frozen context", err)
	}
	expire(t, pool, review.RunID)
	if err = store.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { _, err := r.ReviewContext(); return err }); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatal("stale context read", err)
	}
}
