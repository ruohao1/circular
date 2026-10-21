package completion

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestCompletionTransitions(t *testing.T) {
	contract, err := SealContract(Policy{Mode: "direct_completion", Requirements: []Requirement{{Key: "tests", Description: "Tests"}}}, CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	now := time.Now().UTC()
	valid := Input{Contract: &contract, Report: &Report{Result: "Done", Evidence: []Evidence{{Key: "tests", Detail: "Passed"}}}, SessionID: id, CurrentSessionID: id, Outcome: "succeeded", Released: true, Stage: "in_progress"}
	got := Evaluate(valid, now)
	if got.NextStage != "completed" || !got.CanComplete || got.MergeReadiness != "not_applicable" || len(got.Blockers) != 0 {
		t.Fatal(got)
	}
	for _, c := range []struct {
		name, code string
		change     func(*Input)
	}{
		{"unset", "policy_required", func(i *Input) { i.Contract = nil }},
		{"missing report", "report_required", func(i *Input) { i.Report = nil }},
		{"missing evidence", "evidence_missing", func(i *Input) { i.Report = &Report{Result: "Done"} }},
		{"owner", "owner_active", func(i *Input) { i.OwnerActive = true; i.Released = false }},
		{"cleanup", "cleanup_pending", func(i *Input) { i.CleanupPending = true }},
		{"recovery", "recovery_required", func(i *Input) { i.NeedsUserResolution = true }},
		{"superseded", "attempt_superseded", func(i *Input) { i.CurrentSessionID = uuid.New() }},
		{"failed", "work_unsuccessful", func(i *Input) { i.Outcome = "failed" }},
		{"cancelled", "work_unsuccessful", func(i *Input) { i.Outcome = "cancelled" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := valid
			c.change(&in)
			v := Evaluate(in, now)
			if v.CanComplete || v.NextStage != "" {
				t.Fatal("promoted", v)
			}
			found := false
			for _, b := range v.Blockers {
				if b.Code == c.code && b.Action != "" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing actionable blocker", v)
			}
		})
	}
	review, err := SealContract(Policy{Mode: "review_required"}, CheckConfig{Revision: 1, Checks: []RequiredCheck{{WorkflowID: "1", AppID: "2", WorkflowPath: ".github/workflows/ci.yml", Name: "Tests"}}})
	if err != nil {
		t.Fatal(err)
	}
	valid.Contract = &review
	valid.Report = &Report{Result: "Ready for review"}
	got = Evaluate(valid, now)
	if got.NextStage != "in_review" || got.CanComplete || got.MergeReadiness != "blocked" {
		t.Fatal("review finish requires publication too early", got)
	}
}
