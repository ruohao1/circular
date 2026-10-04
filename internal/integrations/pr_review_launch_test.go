package integrations_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestReviewLaunchRejectsUnavailableAndWrongProjectReviewer(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(snap.PR, "open")
	prepared, err := f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil || !prepared.Ready {
		t.Fatal(prepared.Reason, err)
	}
	req := prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: prepared.Snapshot.InputFingerprint, Mode: "normal"}
	first, err := f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"closed", "merged"} {
		f.provider.SetReviewPR(snap.PR, state)
		p, err := f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
		if err != nil || p.Ready {
			t.Fatal("closed/merged ready", err)
		}
	}
	f.provider.SetReviewPR(snap.PR, "open")
	f.provider.SetReviewRepositories("101", "999")
	p, err := f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil || p.Ready {
		t.Fatal("fork ready", err)
	}
	f.provider.SetReviewRepositories("999", "101")
	p, err = f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil || p.Ready {
		t.Fatal("wrong repository ready", err)
	}
	f.provider.SetReviewPR(snap.PR, "open")
	if _, err = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, snap.Reviewer.AgentID); err != nil {
		t.Fatal(err)
	}
	p, err = f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil || p.Ready {
		t.Fatal("disabled ready", err)
	}
	replay, err := f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), req)
	if err != nil || replay.ID != first.ID {
		t.Fatal("mutable reads blocked replay", err)
	}
	other := testsupport.SeedPRReviewSource(t, f.pool, uuid.Nil)
	p, err = f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), other.Reviewer.AgentID.String())
	if err != nil || p.Ready {
		t.Fatal("cross project ready", err)
	}
	var coder string
	if err = f.pool.QueryRow(t.Context(), `SELECT agent_id::text FROM runs WHERE id=$1`, snap.SourceRunID).Scan(&coder); err != nil {
		t.Fatal(err)
	}
	p, err = f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), coder)
	if err != nil || p.Ready {
		t.Fatal("self review ready", err)
	}
}

func TestReviewLaunchRejectsStalePreparation(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(snap.PR, "open")
	p, err := f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	snap.PR.HeadSHA = strings.Repeat("c", 40)
	f.provider.SetReviewPR(snap.PR, "open")
	_, err = f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: p.Snapshot.InputFingerprint, Mode: "normal"})
	if !errors.Is(err, prreviews.ErrSnapshotChanged) {
		t.Fatal("stale launch", err)
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_reviews`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestReviewCannotPublishAnotherPR(t *testing.T) {
	f := setup(t)
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	review, err := postgres.NewPRReviewStore(f.pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snap, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snap.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, review.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'diff','fixture','{}')`, uuid.New(), review.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.QueueGitHubRunDelivery(t.Context(), review.RunID.String()); !errors.Is(err, integrations.ErrDeliveryNotReady) {
		t.Fatal("review can publish coding diff", err)
	}
}

func TestDisableReviewCancelsQueuedAutomaticOnly(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(snap.PR, "open")
	if _, err := f.service.SetPRReviewSettings(t.Context(), f.project, prreviews.SettingsUpdate{Automatic: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO pr_review_launch_intents(id,project_id,source_run_id,request_key,parameters,parameters_sha256,automatic) VALUES($1,$2,$3,$3,'{}','',true)`, uuid.New(), snap.ProjectID, snap.SourceRunID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.ProcessPRReviewLaunch(t.Context()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	page, err := f.service.ListPRReviews(t.Context(), snap.SourceRunID.String(), 20, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	auto := page.Items[0]
	if !auto.Automatic {
		t.Fatal("automatic intent not launched")
	}
	req := prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: auto.Snapshot.InputFingerprint, Mode: "again", PreviousReviewID: auto.ID}
	manual, err := f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.RequestKey = uuid.New()
	running, err := f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE pr_reviews SET automatic=true WHERE id=$1`, running.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE runs SET status='running' WHERE id=$1`, running.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, snap.Reviewer.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Disconnect(t.Context(), f.project, "github"); err != nil {
		t.Fatal(err)
	}
	settings, err := f.service.SetPRReviewSettings(t.Context(), f.project, prreviews.SettingsUpdate{Automatic: false})
	if err != nil || settings.Automatic {
		t.Fatal(settings, err)
	}
	for _, item := range []struct {
		id   uuid.UUID
		want string
	}{{auto.RunID, "cancelled"}, {manual.RunID, "queued"}, {running.RunID, "running"}} {
		var got string
		if err = f.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, item.id).Scan(&got); err != nil || got != item.want {
			t.Fatal(item, got, err)
		}
	}
}

func TestAutomaticManualLaunchRaceSharesOneRun(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(snap.PR, "open")
	if _, err := f.service.SetPRReviewSettings(t.Context(), f.project, prreviews.SettingsUpdate{Automatic: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO pr_review_launch_intents(id,project_id,source_run_id,request_key,parameters,parameters_sha256,automatic) VALUES($1,$2,$3,$3,'{}','',true)`, uuid.New(), snap.ProjectID, snap.SourceRunID); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.service.PreparePRReview(t.Context(), snap.SourceRunID.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 2)
	go func() { _, err := f.service.ProcessPRReviewLaunch(t.Context()); ch <- err }()
	go func() {
		_, err := f.service.LaunchPRReview(t.Context(), snap.SourceRunID.String(), prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: prepared.Snapshot.InputFingerprint, Mode: "normal"})
		ch <- err
	}()
	for range 2 {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_reviews`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
