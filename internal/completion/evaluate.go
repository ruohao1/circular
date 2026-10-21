package completion

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
	"time"
)

func Evaluate(in Input, now time.Time) Verdict {
	v := Verdict{MergeReadiness: "not_applicable", Blockers: []Blocker{}, Advisories: []string{}}
	block := func(code, message, action string) {
		v.Blockers = append(v.Blockers, Blocker{Code: code, Message: message, Action: action})
	}
	if in.Contract == nil || ValidateContract(*in.Contract) != nil {
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
	if in.SessionID == uuid.Nil || in.SessionID != in.CurrentSessionID {
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
	if in.Binding == nil || in.Binding.Number < 1 {
		block("pr_required", "No pull request is bound", "Publish the PR manually and bind its number to this Task")
	}
	o := in.Observation
	if o == nil {
		block("provider_unavailable", "GitHub evidence has not been collected", "Refresh completion evidence")
	} else {
		if o.ErrorCode != "" {
			switch o.ErrorCode {
			case "ci_revision_unverified":
				block(o.ErrorCode, "The tested base and head could not be proved", "Update the branch and rerun required CI")
			case "merge_revision_unverified":
				block(o.ErrorCode, "The merge could not be matched to the accepted revision", "Inspect the merged commit and refresh its lineage and tree proof")
			case "ci_required":
				block(o.ErrorCode, "Required CI evidence could not be verified", "Inspect the selected workflow jobs and rerun CI")
			case "evidence_changed":
				block(o.ErrorCode, "The PR changed during collection", "Refresh evidence for the current revision")
			default:
				block("provider_unavailable", "GitHub evidence is unavailable", "Check the GitHub App connection and read permissions, then refresh")
			}
		}
		if o.CheckedAt.IsZero() || o.CheckedAt.After(now) || now.Sub(o.CheckedAt) >= EvidenceTTL {
			block("evidence_stale", "The GitHub observation is stale", "Refresh completion evidence")
		}
		if in.Identity == nil || in.Identity.ID == uuid.Nil || in.Identity.Generation < 1 || *in.Identity != o.Identity {
			block("provider_changed", "The GitHub installation identity changed", "Reconnect the correct GitHub App installation and refresh")
		}
		if o.SessionID != in.SessionID || o.ContractFingerprint != in.Contract.Fingerprint || o.ReviewRevision != in.ReviewRevision || in.Binding == nil || o.BindingRevision != in.Binding.Revision || o.PR.Number != in.Binding.Number || o.PR.GitHubRepositoryID != o.Identity.RepositoryID || o.PR.InstallationID != o.Identity.InstallationID {
			block("evidence_changed", "The observed completion identity changed", "Refresh evidence for the current attempt and PR")
		}
		if !o.Revision.Valid() || o.PR.HeadSHA != o.Revision.HeadSHA || o.PRState == "open" && o.PR.BaseSHA != o.Revision.BaseSHA {
			block("ci_revision_unverified", "CI is not proven for this base and head", "Update the branch and rerun required CI")
		}
		if o.PRState != "open" && o.PRState != "merged" {
			block("pr_closed", "The PR is not open or verified as merged", "Reopen the PR or bind the current change")
		}
		checks := map[RequiredCheck]int{}
		good := len(o.Checks) == len(in.Contract.CheckConfig.Checks)
		for _, e := range o.Checks {
			checks[e.Check]++
			if !decimalID(e.RunID) || !decimalID(e.JobID) || !decimalID(e.CheckRunID) || e.Attempt < 1 || e.TestedSHA != o.Revision.TestedSHA || e.RunStatus != "completed" || e.RunConclusion != "success" || e.Status != "completed" || e.Conclusion != "success" {
				good = false
			}
		}
		for _, required := range in.Contract.CheckConfig.Checks {
			if checks[required] != 1 {
				good = false
			}
		}
		if !good {
			block("ci_required", "Every selected job must pass for the verified revision", "Wait for or rerun the required GitHub Actions jobs")
		}
	}
	r := in.Review
	if r == nil || r.ID == uuid.Nil || r.RunID == uuid.Nil || r.State != "succeeded" || !r.Retained || r.Coverage != "complete" || !prreviews.DigestPattern.MatchString(r.ReportSHA) || o == nil || o.ReviewID != r.ID || o.ReviewReportSHA != r.ReportSHA || o.ReviewerFingerprint != r.ReviewerFingerprint || !prreviews.DigestPattern.MatchString(r.ReviewerFingerprint) || r.BaseSHA != o.Revision.BaseSHA || r.HeadSHA != o.Revision.HeadSHA {
		block("review_required", "A complete independent review of this revision is required", "Request or finish the latest independent review")
	} else {
		blocking := false
		for _, f := range r.Findings {
			if f.Severity == "low" {
				v.Advisories = append(v.Advisories, f.Title)
			} else {
				blocking = true
			}
		}
		if blocking {
			block("review_findings", "The independent review has blocking findings", "Fix critical, high and medium findings and request a new review")
		}
	}
	if len(v.Blockers) > 0 {
		return v
	}
	v.MergeReadiness = "ready"
	if o.PRState == "merged" {
		if !o.Merged || !o.MergeVerified || !prreviews.CommitPattern.MatchString(o.MergeSHA) {
			block("merge_revision_unverified", "The merged result is not proven", "Refresh and verify the merged commit's lineage and exact tree")
			return v
		}
		v.CanComplete = true
	} else {
		block("merge_required", "The verified PR has not been merged", "Merge it in GitHub, then request Done in Circular")
	}
	return v
}
