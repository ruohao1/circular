package completion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

func readyCompletion(t *testing.T, now time.Time) Input {
	t.Helper()
	contract, err := SealContract(Policy{Mode: "review_required"}, CheckConfig{Revision: 1, Checks: []RequiredCheck{
		{WorkflowID: "1", AppID: "2", WorkflowPath: ".github/workflows/ci.yml", Name: "Go / PostgreSQL / Docker"},
		{WorkflowID: "1", AppID: "2", WorkflowPath: ".github/workflows/ci.yml", Name: "Frontend / contracts"},
		{WorkflowID: "1", AppID: "2", WorkflowPath: ".github/workflows/ci.yml", Name: "Browser / local and Compose"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, reviewID := uuid.New(), uuid.New()
	identity := ProviderIdentity{ID: uuid.New(), Generation: 1, InstallationID: "42", RepositoryID: "101"}
	base, head, tested, tree := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	reviewer, report := strings.Repeat("e", 64), strings.Repeat("f", 64)
	observation := Observation{SessionID: id, BindingRevision: 1, ReviewRevision: 1, ContractFingerprint: contract.Fingerprint, Identity: identity, ReviewID: reviewID, ReviewerFingerprint: reviewer, ReviewReportSHA: report, CheckedAt: now, PR: prreviews.PRIdentity{Number: 1, GitHubRepositoryID: "101", InstallationID: "42", BaseSHA: base, HeadSHA: head}, PRState: "open", Revision: VerifiedRevision{BaseSHA: base, HeadSHA: head, TestedSHA: tested, TreeSHA: tree}}
	for _, check := range contract.CheckConfig.Checks {
		observation.Checks = append(observation.Checks, CheckEvidence{Check: check, RunID: "10", Attempt: 1, JobID: "20", CheckRunID: "30", TestedSHA: tested, RunStatus: "completed", RunConclusion: "success", Status: "completed", Conclusion: "success"})
	}
	return Input{Contract: &contract, Report: &Report{Result: "Done"}, SessionID: id, CurrentSessionID: id, Outcome: "succeeded", Released: true, Stage: "in_review", Binding: &Binding{Number: 1, Revision: 1}, Identity: &identity, Observation: &observation, ReviewRevision: 1, Review: &ReviewEvidence{ID: reviewID, RunID: uuid.New(), State: "succeeded", BaseSHA: base, HeadSHA: head, ReviewerFingerprint: reviewer, ReportSHA: report, Coverage: "complete", Retained: true, Findings: []prreviews.Finding{}}}
}
func cloneCompletion(t *testing.T, in Input) Input {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Input
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func hasCompletionBlock(v Verdict, code string) bool {
	for _, b := range v.Blockers {
		if b.Code == code && b.Action != "" {
			return true
		}
	}
	return false
}

func TestCompletionReadiness(t *testing.T) {
	now := time.Now().UTC()
	ready := readyCompletion(t, now)
	verdict := Evaluate(ready, now)
	if verdict.MergeReadiness != "ready" || verdict.CanComplete || !hasCompletionBlock(verdict, "merge_required") {
		t.Fatal(verdict)
	}
	for _, test := range []struct {
		name, code string
		change     func(*Input)
	}{
		{"no provider", "provider_unavailable", func(i *Input) { i.Observation = nil }},
		{"error", "provider_unavailable", func(i *Input) { i.Observation.ErrorCode = "provider_unavailable" }},
		{"stale", "evidence_stale", func(i *Input) { i.Observation.CheckedAt = now.Add(-time.Minute) }},
		{"future", "evidence_stale", func(i *Input) { i.Observation.CheckedAt = now.Add(time.Second) }},
		{"identity", "provider_changed", func(i *Input) { i.Identity.Generation++ }},
		{"scope", "evidence_changed", func(i *Input) { i.Observation.SessionID = uuid.New() }},
		{"binding", "evidence_changed", func(i *Input) { i.Binding.Revision++ }},
		{"contract", "evidence_changed", func(i *Input) { i.Observation.ContractFingerprint = "changed" }},
		{"base advanced", "ci_revision_unverified", func(i *Input) { i.Observation.PR.BaseSHA = strings.Repeat("f", 40) }},
		{"head advanced", "ci_revision_unverified", func(i *Input) { i.Observation.PR.HeadSHA = strings.Repeat("f", 40) }},
		{"unknown tree", "ci_revision_unverified", func(i *Input) { i.Observation.Revision.TreeSHA = "" }},
		{"no checks", "ci_required", func(i *Input) { i.Observation.Checks = nil }},
		{"duplicate job", "ci_required", func(i *Input) { i.Observation.Checks = append(i.Observation.Checks, i.Observation.Checks[0]) }},
		{"wrong producer", "ci_required", func(i *Input) { i.Observation.Checks[0].Check.AppID = "999" }},
		{"wrong workflow", "ci_required", func(i *Input) { i.Observation.Checks[0].Check.WorkflowID = "999" }},
		{"wrong tested sha", "ci_required", func(i *Input) { i.Observation.Checks[0].TestedSHA = i.Observation.PR.HeadSHA }},
		{"new workflow pending", "ci_required", func(i *Input) { i.Observation.Checks[0].RunStatus = "queued" }},
		{"no review", "review_required", func(i *Input) { i.Review = nil }},
		{"new intent", "evidence_changed", func(i *Input) { i.ReviewRevision++ }},
		{"pending review", "review_required", func(i *Input) { i.Review.State = "queued" }},
		{"failed review", "review_required", func(i *Input) { i.Review.State = "failed" }},
		{"cancelled review", "review_required", func(i *Input) { i.Review.State = "cancelled" }},
		{"incomplete review", "review_required", func(i *Input) { i.Review.Coverage = "incomplete" }},
		{"unretained review", "review_required", func(i *Input) { i.Review.Retained = false }},
		{"unverified review", "review_required", func(i *Input) { i.Observation.ReviewReportSHA = "" }},
		{"medium finding", "review_findings", func(i *Input) { i.Review.Findings = []prreviews.Finding{{Severity: "medium", Title: "Defect"}} }},
		{"closed", "pr_closed", func(i *Input) { i.Observation.PRState = "closed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := cloneCompletion(t, ready)
			test.change(&in)
			v := Evaluate(in, now)
			if v.CanComplete || v.MergeReadiness != "blocked" || !hasCompletionBlock(v, test.code) {
				t.Fatal(v)
			}
		})
	}
	for _, conclusion := range []string{"", "neutral", "skipped", "failure", "cancelled", "timed_out", "action_required"} {
		in := cloneCompletion(t, ready)
		in.Observation.Checks[0].Conclusion = conclusion
		if v := Evaluate(in, now); v.MergeReadiness != "blocked" || !hasCompletionBlock(v, "ci_required") {
			t.Fatal(conclusion, v)
		}
	}
	low := cloneCompletion(t, ready)
	low.Review.Findings = []prreviews.Finding{{Severity: "low", Title: "Small improvement"}}
	if v := Evaluate(low, now); v.MergeReadiness != "ready" || len(v.Advisories) != 1 {
		t.Fatal(v)
	}
}

func TestCompletionMergeProof(t *testing.T) {
	now := time.Now().UTC()
	in := readyCompletion(t, now)
	in.Observation.PRState = "merged"
	in.Observation.Merged = true
	in.Observation.MergeSHA = strings.Repeat("9", 40)
	in.Observation.MergeVerified = true
	in.Observation.PR.BaseSHA = strings.Repeat("8", 40)
	if v := Evaluate(in, now); !v.CanComplete || v.MergeReadiness != "ready" {
		t.Fatal("today's base invalidated accepted revision", v)
	}
	for _, change := range []func(*Input){func(i *Input) { i.Observation.MergeVerified = false }, func(i *Input) { i.Observation.MergeSHA = "" }, func(i *Input) { i.Observation.Merged = false }} {
		copy := cloneCompletion(t, in)
		change(&copy)
		if v := Evaluate(copy, now); v.CanComplete || !hasCompletionBlock(v, "merge_revision_unverified") {
			t.Fatal(v)
		}
	}
}

func TestCompletedReviewKeepsHistoricalDecision(t *testing.T) {
	now := time.Now().UTC()
	in := readyCompletion(t, now)
	in.Observation.PRState = "merged"
	in.Observation.Merged = true
	in.Observation.MergeSHA = strings.Repeat("9", 40)
	in.Observation.MergeVerified = true
	if v := Evaluate(in, now); !v.CanComplete {
		t.Fatal("completion was not eligible", v)
	}
	in.Stage = "completed"
	for _, change := range []struct {
		name  string
		apply func(*Input)
	}{
		{"expired", func(*Input) {}},
		{"disconnected", func(i *Input) { i.Identity = nil }},
		{"review artifact expired", func(i *Input) { i.Review.Retained = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			copy := cloneCompletion(t, in)
			change.apply(&copy)
			v := Evaluate(copy, now.Add(2*time.Minute))
			if !v.CanComplete || v.NextStage != "completed" || v.MergeReadiness != "not_applicable" || len(v.Blockers) != 0 {
				t.Fatal("accepted completion was reevaluated", v)
			}
			copy.Stage, copy.CurrentSessionID = "ready", uuid.Nil
			if v := Evaluate(copy, now.Add(2*time.Minute)); v.CanComplete || !hasCompletionBlock(v, "attempt_superseded") {
				t.Fatal("reopening reused the accepted completion", v)
			}
		})
	}
}
