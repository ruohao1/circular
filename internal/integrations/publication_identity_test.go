package integrations_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruohao1/circular/internal/integrations"
)

func TestPublicationIdentityUsesSelectedActor(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	before, err := f.service.PublicationIdentity(t.Context(), f.project, "linear", "20000000-0000-4000-8000-000000000004")
	if err != nil || before.Mode != "user" || before.ActorID == "" {
		t.Fatal(before, err)
	}
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.PublicationIdentity(t.Context(), f.project, "linear", before.AccountID)
	if err != nil || after.Mode != "app" || after.ActorID == before.ActorID {
		t.Fatal(after, err)
	}
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	deliver(t, f)
	comments := f.provider.LinearComments()
	if len(comments) != 1 || comments[0].ActorID != after.ActorID {
		t.Fatal("new comment did not use the app actor")
	}
	if err := f.service.DetachIdentity(t.Context(), f.project, "linear"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PublicationIdentity(t.Context(), f.project, "linear", before.AccountID); err == nil {
		t.Fatal("disabled binding chose a publisher")
	}
}

func enableFixtureGitHubIdentity(t *testing.T, f *fixture) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.pem")
	if err := os.WriteFile(path, f.provider.GitHubPrivateKey(), 0600); err != nil {
		t.Fatal(err)
	}
	f.config.GitHubPrivateKeyFile = path
	service, err := integrations.New(f.pool, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	if _, err := f.service.SaveGitHubIdentity(t.Context(), f.project, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationIdentityReviewUsesAppAfterUserExpires(t *testing.T) {
	f, r := completedReview(t)
	enableFixtureGitHubIdentity(t, &f)
	if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET credentials=NULL WHERE project_id=$1 AND provider='github'`, f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PreparePRReview(t.Context(), r.Snapshot.SourceRunID.String(), r.Snapshot.Reviewer.AgentID.String()); err != nil {
		t.Fatal("app-enabled review still depends on user", err)
	}
	if worked, err := f.service.ProcessPRReviewPublication(t.Context()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	got, err := f.service.PRReview(t.Context(), r.ID.String())
	if err != nil || got.GitHub.Status != "published" {
		t.Fatal(got.GitHub, err)
	}
	receipts := f.provider.ReviewReceipts()
	if len(receipts) != 1 || receipts[0].AuthorID != "501" {
		t.Fatal("review was not authored by app", receipts)
	}
}

func TestPublicationIdentityDraftPRPinsAuthorAcrossUpgrade(t *testing.T) {
	f := newDeliveryFixture(t, false)
	f.remote.losePR = true
	if _, err := f.service.QueueGitHubRunDelivery(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ProcessGitHubRunDelivery(t.Context()); err != nil {
		t.Fatal(err)
	}
	enableFixtureGitHubIdentity(t, &f.fixture)
	if _, err := f.service.QueueGitHubRunDelivery(t.Context(), f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ProcessGitHubRunDelivery(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || got.Status != "delivered" || f.remote.prs != 1 {
		t.Fatal("duplicate PR after identity upgrade", got, err)
	}
	var actor string
	if err := f.pool.QueryRow(t.Context(), `SELECT publisher->>'actor_id' FROM github_run_deliveries WHERE run_id=$1`, f.run).Scan(&actor); err != nil || actor != "101" {
		t.Fatal("historical author changed", actor, err)
	}
}

func TestPublicationIdentityLinearLostResponseSurvivesAppUpgrade(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	f.provider.LoseCommentResponse.Store(true)
	deliver(t, f)
	var publisher []byte
	var started bool
	if err := f.pool.QueryRow(t.Context(), `SELECT publisher,started FROM linear_run_updates WHERE run_id=$1`, run).Scan(&publisher, &started); err != nil {
		t.Fatal("write intent not durable", err)
	}
	var old integrations.PublisherIdentity
	if json.Unmarshal(publisher, &old) != nil || old.Mode != "user" || !started {
		t.Fatal("missing immutable author")
	}
	auth, code := f.startLinearApp(t)
	if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
		t.Fatal(err)
	}
	restarted, err := integrations.New(f.pool, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.service = restarted
	due(t, f)
	deliver(t, f)
	status, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || status.Status != "delivered" || f.provider.CommentCreates.Load() != 1 {
		t.Fatal("upgrade duplicated the comment", status, err)
	}
	var current []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT publisher FROM linear_run_updates WHERE run_id=$1`, run).Scan(&current); err != nil {
		t.Fatal(err)
	}
	var pinned integrations.PublisherIdentity
	if json.Unmarshal(current, &pinned) != nil || pinned != old {
		t.Fatal("author changed during recovery")
	}
}

func TestPublicationIdentityLegacyLinearReconcilesWithoutNewPost(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(map[bool]string{true: "receipt", false: "unknown"}[exists], func(t *testing.T) {
			f := setup(t)
			f.connect(t, "linear")
			enable(t, f)
			run := linearRun(t, f)
			transition(t, f, run, "succeeded")
			if exists {
				deliver(t, f)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE linear_run_updates SET publisher=NULL,legacy_reconcile=true,started=false,status='pending',attempts=0 WHERE run_id=$1`, run); err != nil {
				t.Fatal(err)
			}
			auth, code := f.startLinearApp(t)
			if _, err := f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, code); err != nil {
				t.Fatal(err)
			}
			deliver(t, f)
			status, err := f.service.RunLinearDelivery(t.Context(), run)
			want := "uncertain"
			var count int64
			if exists {
				want = "delivered"
				count = 1
			}
			if err != nil || status.Status != want || f.provider.CommentCreates.Load() != count {
				t.Fatal("legacy comment was reposted", status, err)
			}
		})
	}
}
