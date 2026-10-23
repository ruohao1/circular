package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/prreviews"
)

func completionProvider(t *testing.T, scenario string) (*Service, prreviews.PRIdentity, []completion.RequiredCheck, *completion.Observation) {
	t.Helper()
	base, head, merge, tree, landed := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40), strings.Repeat("e", 40)
	pr := prreviews.PRIdentity{GitHubRepositoryID: "101", InstallationID: "42", RepositoryName: "fixture/private-source", Number: 1, URL: "https://github.com/fixture/private-source/pull/1", BaseRef: "main", HeadRef: "feature", BaseSHA: base, HeadSHA: head}
	selected := []completion.RequiredCheck{{WorkflowID: "123", AppID: "15368", WorkflowPath: ".github/workflows/ci.yml", Name: "Tests"}}
	prior := &completion.Observation{PR: pr, Revision: completion.VerifiedRevision{BaseSHA: base, HeadSHA: head, TestedSHA: merge, TreeSHA: tree}}
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("completion collector attempted a write", r.Method)
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-app-token" {
			t.Error("missing scoped credential")
			w.WriteHeader(403)
			return
		}
		encode := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(value); err != nil {
				t.Error(err)
			}
		}
		prefix := "/repos/" + pr.RepositoryName
		switch {
		case r.URL.Path == prefix+"/actions/workflows":
			if scenario == "permission" {
				w.WriteHeader(403)
				return
			}
			encode(map[string]any{"total_count": 1, "workflows": []any{map[string]any{"id": 123, "path": ".github/workflows/ci.yml", "state": "active"}}})
		case r.URL.Path == "/repositories/101":
			encode(map[string]any{"id": 101, "full_name": pr.RepositoryName, "clone_url": "https://github.com/" + pr.RepositoryName + ".git", "default_branch": "main", "private": true})
		case r.URL.Path == prefix+"/pulls/1":
			n := reads.Add(1)
			if scenario == "permission" {
				w.WriteHeader(403)
				return
			}
			currentHead, currentBase, testMerge, state, merged := head, base, merge, "open", false
			if scenario == "changed" && n > 1 {
				currentHead = strings.Repeat("f", 40)
			}
			if scenario == "head proof" || scenario == "no proof" || strings.HasPrefix(scenario, "merged newer test merge") {
				testMerge = ""
			}
			if strings.HasPrefix(scenario, "merged") {
				state = "closed"
				merged = true
				testMerge = landed
				currentBase = strings.Repeat("f", 40)
			}
			encode(map[string]any{"number": 1, "html_url": pr.URL, "state": state, "merged": merged, "merge_commit_sha": testMerge, "base": map[string]any{"ref": "main", "sha": currentBase, "repo": map[string]any{"id": 101}}, "head": map[string]any{"ref": "feature", "sha": currentHead, "repo": map[string]any{"id": 101}}})
		case strings.HasPrefix(r.URL.Path, prefix+"/git/commits/"):
			sha := strings.TrimPrefix(r.URL.Path, prefix+"/git/commits/")
			parents := []map[string]string{{"sha": base}, {"sha": head}}
			if sha == head || scenario == "merged squash" || scenario == "merged rebase" || scenario == "merged no lineage" {
				parents = []map[string]string{{"sha": base}}
			}
			if scenario == "no proof" {
				parents = []map[string]string{{"sha": strings.Repeat("9", 40)}}
			}
			actualTree := tree
			if scenario == "merged wrong tree" {
				actualTree = strings.Repeat("8", 40)
			}
			if scenario == "merged wrong parents" {
				parents = []map[string]string{{"sha": strings.Repeat("8", 40)}, {"sha": head}}
			}
			encode(map[string]any{"sha": sha, "tree": map[string]string{"sha": actualTree}, "parents": parents})
		case strings.HasPrefix(r.URL.Path, prefix+"/compare/"):
			status, ancestor := "ahead", base
			if scenario == "no proof" || scenario == "merged no lineage" {
				status = "diverged"
				ancestor = strings.Repeat("9", 40)
			}
			encode(map[string]any{"status": status, "behind_by": 0, "base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": ancestor}, "total_commits": 1})
		case r.URL.Path == prefix+"/actions/workflows/123/runs":
			if r.URL.Query().Get("status") != "" {
				t.Error("pending runs were filtered out")
			}
			if scenario == "body limit" {
				fmt.Fprint(w, strings.Repeat(" ", 4<<20+1))
				return
			}
			tested := merge
			if scenario == "head proof" || scenario == "no proof" || strings.HasPrefix(scenario, "merged newer test merge") {
				tested = head
			}

			if strings.HasPrefix(scenario, "merged newer test merge") && r.URL.Query().Get("head_sha") == merge {
				status, conclusion := "queued", ""
				if scenario == "merged newer test merge failed" {
					status, conclusion = "completed", "failure"
				}
				encode(map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 11, "workflow_id": 123, "run_number": 2, "run_attempt": 1, "head_sha": merge, "check_suite_id": 41, "status": status, "conclusion": conclusion, "path": ".github/workflows/ci.yml"}}})
				return
			}
			if strings.HasPrefix(scenario, "merged newer head") && r.URL.Query().Get("head_sha") == head {
				status, conclusion := "queued", ""
				if scenario == "merged newer head failed" {
					status, conclusion = "completed", "failure"
				}
				encode(map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{"id": 11, "workflow_id": 123, "run_number": 2, "run_attempt": 1, "head_sha": head, "check_suite_id": 41, "status": status, "conclusion": conclusion, "path": ".github/workflows/ci.yml"}}})
				return
			}
			if r.URL.Query().Get("head_sha") != "" && r.URL.Query().Get("head_sha") != tested {
				encode(map[string]any{"total_count": 0, "workflow_runs": []any{}})
				return
			}
			run := map[string]any{"id": 10, "workflow_id": 123, "run_number": 1, "run_attempt": 2, "head_sha": tested, "check_suite_id": 40, "status": "completed", "conclusion": "success", "path": ".github/workflows/ci.yml", "created_at": "2026-10-21T10:00:00Z"}
			if scenario == "wrong workflow" {
				run["workflow_id"] = 124
			}
			if scenario == "search cap" {
				encode(map[string]any{"total_count": 1000, "workflow_runs": []any{run}})
				return
			}
			if scenario == "page cap" {
				w.Header().Set("Link", fmt.Sprintf("<http://%s%s?head_sha=%s&per_page=100&page=21>; rel=\"next\"", r.Host, r.URL.Path, tested))
				encode(map[string]any{"total_count": 2001, "workflow_runs": []any{run}})
				return
			}
			if scenario == "new pending" {
				if r.URL.Query().Get("page") == "1" {
					w.Header().Set("Link", fmt.Sprintf("<http://%s%s?head_sha=%s&per_page=100&page=2>; rel=\"next\"", r.Host, r.URL.Path, tested))
					encode(map[string]any{"total_count": 2, "workflow_runs": []any{run}})
					return
				}
				run["id"] = 11
				run["run_number"] = 2
				run["status"] = "queued"
				run["conclusion"] = nil
				encode(map[string]any{"total_count": 2, "workflow_runs": []any{run}})
				return
			}
			encode(map[string]any{"total_count": 1, "workflow_runs": []any{run}})
		case r.URL.Path == prefix+"/actions/runs/10/attempts/2/jobs", r.URL.Path == prefix+"/actions/runs/11/attempts/1/jobs":
			tested := merge
			if scenario == "head proof" || scenario == "no proof" || strings.HasPrefix(scenario, "merged newer test merge") {
				tested = head
			}
			job := map[string]any{"id": 20, "run_id": 10, "head_sha": tested, "name": "Tests", "status": "completed", "conclusion": "success", "check_run_url": "http://" + r.Host + prefix + "/check-runs/30", "html_url": pr.URL}
			if r.URL.Path == prefix+"/actions/runs/11/attempts/1/jobs" {
				job["id"], job["run_id"], job["head_sha"], job["conclusion"] = 21, 11, head, "failure"
				job["check_run_url"] = "http://" + r.Host + prefix + "/check-runs/31"
				if strings.HasPrefix(scenario, "merged newer test merge") {
					job["head_sha"] = merge
				}
			}
			if scenario == "job failed" {
				job["conclusion"] = "failure"
			}
			jobs := []any{job}
			if scenario == "duplicate job" {
				jobs = append(jobs, job)
			}
			encode(map[string]any{"total_count": len(jobs), "jobs": jobs})
		case r.URL.Path == prefix+"/check-suites/40/check-runs", r.URL.Path == prefix+"/check-suites/41/check-runs":
			if r.URL.Query().Get("filter") != "all" {
				t.Error("check attempts were filtered")
			}
			tested := merge
			if scenario == "head proof" || scenario == "no proof" || strings.HasPrefix(scenario, "merged newer test merge") {
				tested = head
			}
			app := 15368
			if scenario == "wrong app" {
				app = 999
			}
			check := map[string]any{"id": 30, "head_sha": tested, "name": "Tests", "status": "completed", "conclusion": "success", "app": map[string]any{"id": app, "slug": "github-actions"}, "check_suite": map[string]any{"id": 40}}
			if r.URL.Path == prefix+"/check-suites/41/check-runs" {
				check["id"], check["head_sha"], check["conclusion"], check["check_suite"] = 31, head, "failure", map[string]any{"id": 41}
				if strings.HasPrefix(scenario, "merged newer test merge") {
					check["head_sha"] = merge
				}
			}
			if scenario == "check skipped" {
				check["conclusion"] = "skipped"
			}
			encode(map[string]any{"total_count": 1, "check_runs": []any{check}})
		default:
			t.Errorf("unexpected provider path %s", r.URL.String())
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return &Service{client: server.Client(), config: Config{GitHubAPIURL: server.URL}}, pr, selected, prior
}

func TestCompletionCheckDiscovery(t *testing.T) {
	for _, scenario := range []string{"merge proof", "wrong workflow", "wrong app", "duplicate job", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			s, pr, checks, _ := completionProvider(t, scenario)
			got, err := s.discoverCompletionChecks(t.Context(), "fixture-app-token", pr)
			if scenario == "merge proof" {
				if err != nil || len(got) != 1 || got[0] != checks[0] {
					t.Fatal(got, err)
				}
			} else if scenario != "wrong app" && err == nil {
				t.Fatal("invalid provider identity accepted", got)
			}
			if scenario == "wrong app" && (err != nil || len(got) != 1 || got[0].AppID != "999") {
				t.Fatal("app ID was hardcoded", got, err)
			}
		})
	}
}

func TestCompletionGitHubEvidence(t *testing.T) {
	for _, scenario := range []string{"merge proof", "head proof", "new pending", "wrong workflow", "wrong app", "duplicate job", "job failed", "check skipped", "search cap", "page cap", "body limit", "permission", "no proof", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			s, pr, checks, _ := completionProvider(t, scenario)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			got, err := s.collectCompletionGitHub(ctx, "fixture-app-token", pr, checks, nil)
			green := err == nil && got.ErrorCode == "" && got.Revision.Valid() && len(got.Checks) == 1 && got.Checks[0].Status == "completed" && got.Checks[0].Conclusion == "success" && got.Checks[0].RunStatus == "completed" && got.Checks[0].RunConclusion == "success"
			if green != (scenario == "merge proof" || scenario == "head proof") {
				t.Fatal(scenario, got, err)
			}
			if scenario == "changed" && got.ErrorCode != "evidence_changed" {
				t.Fatal("mixed PR snapshot", got, err)
			}
			if scenario == "no proof" && got.ErrorCode != "ci_revision_unverified" {
				t.Fatal(got, err)
			}
		})
	}
}
func TestCompletionGitHubMergeProof(t *testing.T) {
	for _, scenario := range []string{"merged normal", "merged squash", "merged rebase", "merged wrong tree", "merged wrong parents", "merged no lineage"} {
		t.Run(scenario, func(t *testing.T) {
			s, pr, checks, prior := completionProvider(t, scenario)

			got, err := s.collectCompletionGitHub(t.Context(), "fixture-app-token", pr, checks, prior)
			want := scenario == "merged normal" || scenario == "merged squash" || scenario == "merged rebase"
			if err != nil || got.MergeVerified != want || !got.Merged {
				t.Fatal(got, err)
			}
			if got.Revision.BaseSHA != prior.Revision.BaseSHA {
				t.Fatal("advanced branch replaced pre-merge base", got)
			}
		})
	}
}

func TestCompletionGitHubMergedNewerHeadRun(t *testing.T) {
	for _, scenario := range []string{"merged newer head queued", "merged newer head failed"} {
		t.Run(scenario, func(t *testing.T) {
			s, pr, checks, prior := completionProvider(t, scenario)
			got, err := s.collectCompletionGitHub(t.Context(), "fixture-app-token", pr, checks, prior)
			if err != nil || !got.Merged || !got.MergeVerified || got.Revision != prior.Revision {
				t.Fatal("accepted merge proof changed", got, err)
			}
			if got.ErrorCode == "" && len(got.Checks) == 1 && got.Checks[0].RunStatus == "completed" && got.Checks[0].RunConclusion == "success" && got.Checks[0].Conclusion == "success" {
				t.Fatal("newer head run ignored in favor of old test-merge success", got)
			}
		})
	}
}

func TestCompletionGitHubMergedNewerTestMergeRun(t *testing.T) {
	for _, scenario := range []string{"merged newer test merge queued", "merged newer test merge failed"} {
		t.Run(scenario, func(t *testing.T) {
			s, pr, checks, prior := completionProvider(t, scenario)
			prior.Revision.TestedSHA = pr.HeadSHA
			prior.MergeSHA = strings.Repeat("c", 40)
			got, err := s.collectCompletionGitHub(t.Context(), "fixture-app-token", pr, checks, prior)
			if err != nil || !got.Merged || !got.MergeVerified || got.Revision != prior.Revision {
				t.Fatal("accepted merge proof changed", got, err)
			}
			if got.ErrorCode == "" && len(got.Checks) == 1 && got.Checks[0].RunStatus == "completed" && got.Checks[0].RunConclusion == "success" && got.Checks[0].Conclusion == "success" {
				t.Fatal("newer test-merge run ignored in favor of old head success", got.Checks[0])
			}
		})
	}
}
