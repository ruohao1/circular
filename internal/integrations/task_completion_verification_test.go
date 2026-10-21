package integrations_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
	"testing"
)

func TestCompletionGitHubIdentityScopedAndNoHumanFallback(t *testing.T) {
	f := githubAppFixture(t)
	source := testsupport.SeedExternalPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(source.PR, "open")
	v, err := f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || v.Observation == nil || v.Observation.ErrorCode != "provider_unavailable" {
		t.Fatal(v, err)
	}
	if f.provider.ReviewReads.Load() != 0 || len(f.provider.InstallationScopes()) != 0 {
		t.Fatal("used human identity")
	}
	if _, err = f.service.SaveGitHubIdentity(t.Context(), f.project, nil); err != nil {
		t.Fatal(err)
	}
	v, err = f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || v.Verdict.CanComplete {
		t.Fatal(v, err)
	}
	scopes := f.provider.InstallationScopes()
	if len(scopes) != 1 || len(scopes[0].Permissions) != 5 || scopes[0].Permissions["actions"] != "read" || scopes[0].Permissions["checks"] != "read" || scopes[0].Permissions["contents"] != "read" || scopes[0].Permissions["pull_requests"] != "read" || len(scopes[0].RepositoryIDs) != 1 || scopes[0].RepositoryIDs[0] != 101 {
		t.Fatal(scopes)
	}
	if _, err = f.service.SaveGitHubIdentity(t.Context(), f.project, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.RefreshTaskCompletion(t.Context(), source.TaskID); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.InstallationScopes()) != 2 {
		t.Fatal("reconnect reused old-generation token")
	}
}

func TestCompletionCurrentReviewChecksAndVerifiedMerge(t *testing.T) {
	f := githubAppFixture(t)
	f.root = t.TempDir()
	f.config.ArtifactRoot = f.root
	service, err := integrations.New(f.pool, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	source := testsupport.SeedExternalPRReviewSource(t, f.pool, uuid.MustParse(f.project))
	f.provider.SetReviewPR(source.PR, "open")
	f.provider.CompletionChecksEnabled.Store(true)
	if _, err := f.service.SaveGitHubIdentity(t.Context(), f.project, nil); err != nil {
		t.Fatal(err)
	}
	setup, err := f.service.RepositoryCompletionChecks(t.Context(), source.RepositoryID)
	if err != nil || setup.Reason != "" || len(setup.Available) != 1 {
		t.Fatal(setup, err)
	}
	forged := setup.Available[0]
	forged.AppID = "999"
	if _, err = f.service.SetRepositoryCompletionChecks(t.Context(), source.RepositoryID, 1, []completion.RequiredCheck{forged}); err == nil {
		t.Fatal("client chose producer")
	}
	if _, err = f.service.SetRepositoryCompletionChecks(t.Context(), source.RepositoryID, 1, setup.Available); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.SetRepositoryCompletionChecks(t.Context(), source.RepositoryID, 1, setup.Available); err == nil {
		t.Fatal("stale configuration revision")
	}
	prepared, err := f.service.PrepareTaskPRReview(t.Context(), source.TaskID.String(), "")
	if err != nil || !prepared.Ready {
		t.Fatal(prepared, err)
	}
	review, err := f.service.LaunchTaskPRReview(t.Context(), source.TaskID.String(), prreviews.LaunchRequest{RequestKey: uuid.New(), Mode: "normal", ExpectedInputFingerprint: prepared.Snapshot.InputFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || pending.Verdict.MergeReadiness != "blocked" {
		t.Fatal(pending, err)
	}
	testsupport.CompletePRReview(t, f.pool, review, f.root, prreviews.Report{Summary: "Independent checks passed", Coverage: "complete", Findings: []prreviews.Finding{}, Checks: []prreviews.Check{}, Limitations: []string{}})
	ready, err := f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || ready.Verdict.MergeReadiness != "ready" || ready.Verdict.CanComplete || ready.Stage != "in_review" {
		t.Fatal(ready, err)
	}
	f.provider.ReviewReadReject.Store(503)
	failed, err := f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || failed.Verdict.MergeReadiness != "blocked" {
		t.Fatal("provider failure retained green", failed, err)
	}
	f.provider.ReviewReadReject.Store(0)
	f.provider.SetReviewPR(source.PR, "merged")
	merged, err := f.service.RefreshTaskCompletion(t.Context(), source.TaskID)
	if err != nil || !merged.Verdict.CanComplete || merged.Stage != "in_review" {
		t.Fatal(merged, err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var board int64
	if err = tx.QueryRow(t.Context(), `SELECT task_board_revision FROM projects WHERE id=$1`, f.project).Scan(&board); err != nil {
		t.Fatal(err)
	}
	if _, err = postgres.MoveTask(t.Context(), tx, source.TaskID, postgres.TaskMoveInput{ExpectedRevision: merged.Revision, ExpectedBoardRevision: board, Status: "completed", Placement: "end"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
