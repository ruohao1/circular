package completion

import (
	"github.com/google/uuid"
	"time"
)

func Evaluate(in Input, now time.Time) Verdict {
	v := Verdict{MergeReadiness: "not_applicable", Blockers: []Blocker{}, Advisories: []string{}}
	block := func(code, message, action string) {
		v.Blockers = append(v.Blockers, Blocker{Code: code, Message: message, Action: action})
	}
	if in.Contract == nil {
		block("policy_required", "Completion policy is not frozen", "Plan a completion policy, then start tracked work")
		return v
	}
	review := in.Contract.Policy.Mode == "review_required"
	if review {
		v.MergeReadiness = "blocked"
	}
	if in.OwnerActive || !in.Released {
		block("owner_active", "Work still owns this Task", "Finish the current work session and release it safely")
	}
	if in.CleanupPending {
		block("cleanup_pending", "Native cleanup is pending", "Wait for worker and Workspace cleanup")
	}
	if in.NeedsUserResolution {
		block("recovery_required", "Work requires recovery", "Resolve the recovery hold before continuing")
	}
	if in.SessionID != uuid.Nil && in.SessionID != in.CurrentSessionID {
		block("attempt_superseded", "This completion attempt is no longer current", "Inspect the current Task attempt")
	}
	if in.Outcome != "succeeded" {
		block("work_unsuccessful", "Work has not succeeded", "Complete a successful tracked attempt")
	}
	if in.Report == nil {
		block("report_required", "No completion report was accepted", "Submit the required result and evidence during work")
	} else if ValidateReport(in.Contract.Policy, *in.Report) != nil {
		block("evidence_missing", "Completion evidence is incomplete", "Supply evidence for every frozen requirement")
	}
	if len(v.Blockers) > 0 {
		return v
	}
	if !review {
		v.NextStage = "completed"
		v.CanComplete = true
		return v
	}
	v.NextStage = "in_review"
	if in.Report.PullRequestNumber == nil {
		block("pr_required", "No pull request is bound", "Publish the PR manually and bind its number to this Task")
	} else {
		block("provider_unavailable", "GitHub evidence has not been collected", "Refresh completion evidence")
	}
	block("review_required", "An independent review is required", "Request a review of the current pull request revision")
	return v
}
