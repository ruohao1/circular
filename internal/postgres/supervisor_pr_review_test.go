package postgres_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func newReviewSupervisorFixture(t *testing.T, output string, exitCode int, slow ...bool) (supervisorFixture, prreviews.Review) {
	t.Helper()
	chunks := []protocolChunk{{"stdout", []byte(output)}}
	if len(slow) > 0 && slow[0] {
		for range 100 {
			chunks = append(chunks, protocolChunk{"stdout", []byte("{\"type\":\"thread.started\"}\n")})
		}
	}
	f := newCodexSupervisorFixture(t, chunks, exitCode)
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='cancelled' WHERE id=$1`, f.id); err != nil {
		t.Fatal(err)
	}
	snap := testsupport.SeedPRReviewSource(t, f.pool, uuid.Nil)
	source := filepath.Join(f.base, "source")
	commit := func() string {
		out, err := exec.CommandContext(t.Context(), "git", "-C", source, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	snap.PR.BaseSHA = commit()
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("introduced by the PR\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "-m", "PR change")
	snap.PR.HeadSHA = commit()
	var err error
	snap, err = prreviews.SealSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	provider := testsupport.NewProviderFixture()
	provider.SetReviewPR(snap.PR, "open")
	server := httptest.NewServer(provider)
	t.Cleanup(server.Close)
	f.config.Integrations = integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, GitHubURL: server.URL, GitHubAPIURL: server.URL}
	service, err := integrations.New(f.pool, f.config.Integrations)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.Begin(t.Context(), snap.ProjectID.String(), "github")
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Complete(t.Context(), "github", auth.State, auth.Browser, callback.Query().Get("code")); err != nil {
		t.Fatal(err)
	}
	// Redirect only fixture clone/fetch transport while preserving the stored
	// GitHub URL and exercising the real trusted credential lookup.
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := filepath.Join(f.base, "fixture-git")
	script := "#!/bin/sh\ncase \" $* \" in *\" clone \"*|*\" fetch \"*) exec " + quote(binary) + " -c " + quote("url."+source+".insteadOf="+snap.CloneURL) + " \"$@\" ;; *) exec " + quote(binary) + " \"$@\" ;; esac\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.config.Git.GitExecutable = wrapper
	review, err := postgres.NewPRReviewStore(f.pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snap, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snap.InputFingerprint, Mode: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	f.id = review.RunID
	f.config.ReviewContextRoot = filepath.Join(f.base, "review-contexts")
	f.config.Docker.ReviewContextRoot = f.config.ReviewContextRoot
	return f, review
}
func reviewProtocol(report *prreviews.Report) string {
	output := codexUsage
	if report != nil {
		raw, _ := json.Marshal(map[string]any{"type": "circular.pr_review.submitted", "report": report})
		output += string(raw) + "\n"
	}
	return output
}
func TestSupervisorReviewRetainsReportsWithoutCodingSideEffects(t *testing.T) {
	for _, scenario := range []string{"clean", "findings", "incomplete", "missing", "failed", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			report := emptyReviewReport()
			report.Summary = "Review complete; secret " + codexTestKey
			if scenario == "findings" {
				report.Findings = []prreviews.Finding{{Severity: "medium", Title: "Requirement missed", Path: "file.txt", Side: "head", StartLine: 1, EndLine: 1, Evidence: "changed line", Consequence: "behavior differs", SuggestedFix: "follow the requirement"}}
			}
			if scenario == "incomplete" {
				report.Coverage = "incomplete"
				report.Limitations = []string{"Required check is unavailable."}
			}
			var submitted *prreviews.Report = &report
			if scenario == "missing" {
				submitted = nil
			}
			exitCode := 0
			if scenario == "failed" {
				exitCode = 23
			}
			f, review := newReviewSupervisorFixture(t, reviewProtocol(submitted), exitCode)
			if scenario == "timeout" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET started_at=now()-interval '16 minutes' WHERE id=$1`, f.id); err != nil {
					t.Fatal(err)
				}
			}
			claim := acquire(t, postgres.NewQueue(f.pool), "review-supervisor")
			supervisor, err := execution.NewSupervisor(f.pool, "review-supervisor", f.config)
			if err != nil {
				t.Fatal(err)
			}
			err = supervisor.Execute(t.Context(), *claim, "review-supervisor")
			failure := scenario == "failed" || scenario == "timeout"
			if (err != nil) != failure {
				t.Fatal("unexpected execution outcome", scenario, err)
			}
			state := testsupport.Observe(t, f.pool, f.id)
			assertCodexResourcesReleased(t, f, state)
			got, err := postgres.NewPRReviewStore(f.pool).Get(t.Context(), review.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := prreviews.AssessmentNoBlocking
			if scenario == "findings" {
				expected = prreviews.AssessmentFindings
			}
			if scenario == "incomplete" || scenario == "missing" || failure {
				expected = prreviews.AssessmentIncomplete
			}
			if got.Assessment != expected {
				t.Fatal(got.Assessment, expected)
			}
			if scenario == "timeout" && (state.Run.Error == nil || !strings.Contains(*state.Run.Error, "15-minute")) {
				t.Fatalf("deadline was reset or hidden: %v / %v", *state.Run.Error, err)
			}
			count := 0
			if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM github_run_deliveries WHERE run_id=$1)+(SELECT count(*) FROM linear_run_updates WHERE run_id=$1 AND phase<>'pr_review')+(SELECT count(*) FROM agent_proposals WHERE run_id=$1)`, f.id).Scan(&count); err != nil || count != 0 {
				t.Fatal("coding side effects", count, err)
			}
			kinds := map[string]bool{}
			for _, a := range state.Artifacts {
				kinds[a.Kind] = true
			}
			if kinds["diff"] || !kinds["pr_review_context"] || !kinds["pr_review_diff"] {
				t.Fatal("wrong review artifacts", kinds)
			}
			if scenario != "missing" && scenario != "timeout" && !kinds["pr_review_report"] {
				t.Fatal("candidate report lost")
			}
			publications := 0
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_review_publications WHERE review_id=$1`, review.ID).Scan(&publications); err != nil {
				t.Fatal(err)
			}
			want := 1
			if scenario == "missing" || failure {
				want = 0
			}
			if publications != want {
				t.Fatal("wrong publication eligibility", publications, want)
			}
			if _, err := os.Lstat(filepath.Join(f.config.ReviewContextRoot, f.id.String())); !os.IsNotExist(err) {
				t.Fatal("input context survived cleanup", err)
			}
			assertCodexCredentialAbsent(t, f, state)
		})
	}
}

func TestSupervisorReviewCancellationAndLeaseRecoveryKeepPartialReport(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "lease_loss"}[takeover], func(t *testing.T) {
			report := emptyReviewReport()
			f, review := newReviewSupervisorFixture(t, reviewProtocol(&report), 0, true)
			queue := postgres.NewQueue(f.pool)
			claim := acquire(t, queue, "review-owner")
			attempt := launchSupervisor(t, f, *claim, "review-owner")
			deadline := time.Now().Add(5 * time.Second)
			for {
				var submitted bool
				if err := f.pool.QueryRow(t.Context(), `SELECT candidate_report IS NOT NULL FROM pr_reviews WHERE id=$1`, review.ID).Scan(&submitted); err != nil {
					t.Fatal(err)
				}
				if submitted {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("report never arrived")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if takeover {
				if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET worker_id='replacement-owner' WHERE id=$1`, f.id); err != nil {
					t.Fatal(err)
				}
				if err := attempt.wait(t); !errors.Is(err, postgres.ErrLeaseLost) {
					t.Fatal("stale worker kept ownership", err)
				}
				expire(t, f.pool, f.id)
				recovery := acquire(t, queue, "cleanup-owner")
				if recovery == nil || !recovery.Recovery {
					t.Fatal("review not recoverable")
				}
				cleaner, err := execution.NewSupervisor(f.pool, "cleanup-owner", f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := cleaner.Execute(t.Context(), *recovery, "cleanup-owner"); err != nil {
					t.Fatal(err)
				}
			} else {
				testsupport.Cancel(t, f.pool, f.id)
				if err := attempt.wait(t); err != nil {
					t.Fatal(err)
				}
			}
			state := testsupport.Observe(t, f.pool, f.id)
			assertCodexResourcesReleased(t, f, state)
			got, err := postgres.NewPRReviewStore(f.pool).Get(t.Context(), review.ID)
			if err != nil || got.Assessment != prreviews.AssessmentIncomplete || got.Report == nil {
				t.Fatal("partial report lost", got.Assessment, err)
			}
			if state.Count("run.started") != 1 || state.Count("run.completed") != 0 {
				t.Fatal("review executed twice", state.Types())
			}
			var publications int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_review_publications WHERE review_id=$1`, review.ID).Scan(&publications); err != nil || publications != 0 {
				t.Fatal("partial report queued as complete", publications, err)
			}
		})
	}
}

func TestSupervisorReviewArtifactCrashAndCorruptionRecoverWithoutReexecution(t *testing.T) {
	report := emptyReviewReport()
	f, review := newReviewSupervisorFixture(t, reviewProtocol(&report), 0)
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE artifacts ADD CONSTRAINT reject_review_artifacts CHECK (kind NOT LIKE 'pr_review_%')`); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewQueue(f.pool)
	claim := acquire(t, queue, "review-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "review-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Execute(t.Context(), *claim, "review-owner"); err == nil {
		t.Fatal("artifact outage hidden")
	}
	path := filepath.Join(f.config.ArtifactRoot, f.id.String(), "pr-review-context.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("file was not durably written before metadata", err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE artifacts DROP CONSTRAINT reject_review_artifacts`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		expire(t, f.pool, f.id)
		recovery := acquire(t, queue, "cleanup-owner")
		if recovery == nil || !recovery.Recovery {
			t.Fatal("missing cleanup claim")
		}
		cleaner, err := execution.NewSupervisor(f.pool, "cleanup-owner", f.config)
		if err != nil {
			t.Fatal(err)
		}
		err = cleaner.Execute(t.Context(), *recovery, "cleanup-owner")
		if attempt == 0 {
			if err == nil {
				t.Fatal("corruption permitted cleanup")
			}
			if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); err != nil {
				t.Fatal("corruption discarded source", err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	state := testsupport.Observe(t, f.pool, f.id)
	assertCodexResourcesReleased(t, f, state)
	got, err := postgres.NewPRReviewStore(f.pool).Get(t.Context(), review.ID)
	if err != nil || got.Report == nil || got.Assessment != prreviews.AssessmentIncomplete || state.Count("run.started") != 1 || len(state.Artifacts) != 3 {
		t.Fatal("recovery lost report or repeated execution", got.Assessment, len(state.Artifacts), state.Types(), err)
	}
}
