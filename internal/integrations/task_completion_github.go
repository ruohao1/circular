package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/prreviews"
)

// Every page is bounded; a partial listing must never certify a selected job.
func (s *Service) completionPages(ctx context.Context, token, path, key string, search bool) ([]json.RawMessage, error) {
	u, err := url.Parse(path)
	if err != nil {
		return nil, ErrProvider
	}
	q := u.Query()
	q.Set("per_page", "100")
	var all []json.RawMessage
	total := -1
	for page := 1; page <= 20; page++ {
		q.Set("page", strconv.Itoa(page))
		u.RawQuery = q.Encode()
		var body map[string]json.RawMessage
		status, headers, err := s.reviewRequest(ctx, token, http.MethodGet, u.String(), nil, &body)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			if retry := providerRetryError(status, headers); retry != nil {
				return nil, retry
			}
			return nil, deliveryHTTPError(status)
		}
		var count int
		var values []json.RawMessage
		if json.Unmarshal(body["total_count"], &count) != nil || count < 0 || count > 2000 || search && count >= 1000 || json.Unmarshal(body[key], &values) != nil || len(values) > 100 {
			return nil, ErrProvider
		}
		if total >= 0 && total != count {
			return nil, ErrProvider
		}
		total = count
		all = append(all, values...)
		if len(all) > total {
			return nil, ErrProvider
		}
		next := ""
		for _, part := range strings.Split(headers.Get("Link"), ",") {
			if !strings.Contains(part, `rel="next"`) {
				continue
			}
			left, right := strings.Index(part, "<"), strings.Index(part, ">")
			if next != "" || left < 0 || right <= left {
				return nil, ErrProvider
			}
			next = part[left+1 : right]
		}
		if next == "" {
			if len(all) != total {
				return nil, ErrProvider
			}
			return all, nil
		}
		base, e := url.Parse(s.config.GitHubAPIURL)
		target, e2 := url.Parse(next)
		if e != nil || e2 != nil || target.Scheme != base.Scheme || target.Host != base.Host || target.User != nil || target.Fragment != "" || target.Path != base.Path+u.Path {
			return nil, ErrProvider
		}
		wanted := u.Query()
		wanted.Set("page", strconv.Itoa(page+1))
		if target.Query().Encode() != wanted.Encode() || len(values) == 0 {
			return nil, ErrProvider
		}
	}
	return nil, ErrProvider
}

type completionRun struct {
	ID         json.Number `json:"id"`
	WorkflowID json.Number `json:"workflow_id"`
	Number     int64       `json:"run_number"`
	Attempt    int64       `json:"run_attempt"`
	HeadSHA    string      `json:"head_sha"`
	SuiteID    json.Number `json:"check_suite_id"`
	Path       string      `json:"path"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
}
type completionJob struct {
	ID         json.Number `json:"id"`
	RunID      json.Number `json:"run_id"`
	HeadSHA    string      `json:"head_sha"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
	CheckURL   string      `json:"check_run_url"`
	URL        string      `json:"html_url"`
}
type completionCheck struct {
	ID         json.Number `json:"id"`
	HeadSHA    string      `json:"head_sha"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
	App        struct {
		ID   json.Number `json:"id"`
		Slug string      `json:"slug"`
	} `json:"app"`
	Suite struct {
		ID json.Number `json:"id"`
	} `json:"check_suite"`
}
type completionCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

func (s *Service) completionGet(ctx context.Context, token, path string, out any) error {
	status, headers, err := s.reviewRequest(ctx, token, http.MethodGet, path, nil, out)
	if err != nil {
		return err
	}
	if status != 200 {
		if retry := providerRetryError(status, headers); retry != nil {
			return retry
		}
		return deliveryHTTPError(status)
	}
	return nil
}
func (s *Service) completionCommit(ctx context.Context, token, prefix, sha string) (completionCommit, error) {
	var c completionCommit
	if !prreviews.CommitPattern.MatchString(sha) {
		return c, ErrProvider
	}
	err := s.completionGet(ctx, token, prefix+"/git/commits/"+sha, &c)
	if err == nil && (c.SHA != sha || !prreviews.CommitPattern.MatchString(c.Tree.SHA)) {
		err = ErrProvider
	}
	return c, err
}
func (s *Service) completionAncestor(ctx context.Context, token, prefix, base, head string) (bool, error) {
	var v struct {
		Status string `json:"status"`
		Behind int64  `json:"behind_by"`
		Base   struct {
			SHA string `json:"sha"`
		} `json:"base_commit"`
		Ancestor struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	err := s.completionGet(ctx, token, prefix+"/compare/"+base+"..."+head+"?per_page=1", &v)
	return err == nil && (v.Status == "ahead" || v.Status == "identical") && v.Behind == 0 && v.Base.SHA == base && v.Ancestor.SHA == base, err
}

func (s *Service) completionLatestRun(ctx context.Context, token, prefix string, selected completion.RequiredCheck, shas []string) (completionRun, error) {
	var latest completionRun
	seen := map[string]completionRun{}
	for _, sha := range shas {
		values, err := s.completionPages(ctx, token, prefix+"/actions/workflows/"+selected.WorkflowID+"/runs?head_sha="+sha, "workflow_runs", true)
		if err != nil {
			return latest, err
		}
		for _, raw := range values {
			var v completionRun
			if json.Unmarshal(raw, &v) != nil || !positiveNumber(string(v.ID)) || string(v.WorkflowID) != selected.WorkflowID || v.Path != selected.WorkflowPath || v.HeadSHA != sha || v.Number < 1 || v.Attempt < 1 {
				return latest, ErrProvider
			}
			if old, ok := seen[string(v.ID)]; ok && old != v {
				return latest, ErrProvider
			}
			seen[string(v.ID)] = v
			if latest.ID != "" && v.Number == latest.Number && v.ID != latest.ID {
				return latest, ErrProvider
			}
			if v.Number > latest.Number || v.ID == latest.ID && v.Attempt > latest.Attempt {
				latest = v
			}
		}
	}
	return latest, nil
}
func (s *Service) completionRunChecks(ctx context.Context, token, prefix string, run completionRun) ([]completionJob, []completionCheck, error) {
	jobsRaw, err := s.completionPages(ctx, token, prefix+"/actions/runs/"+string(run.ID)+"/attempts/"+strconv.FormatInt(run.Attempt, 10)+"/jobs", "jobs", false)
	if err != nil {
		return nil, nil, err
	}
	if !positiveNumber(string(run.SuiteID)) {
		return nil, nil, ErrProvider
	}
	checksRaw, err := s.completionPages(ctx, token, prefix+"/check-suites/"+string(run.SuiteID)+"/check-runs?filter=all", "check_runs", false)
	if err != nil {
		return nil, nil, err
	}
	jobs := make([]completionJob, len(jobsRaw))
	checks := make([]completionCheck, len(checksRaw))
	for i, raw := range jobsRaw {
		if json.Unmarshal(raw, &jobs[i]) != nil {
			return nil, nil, ErrProvider
		}
	}
	for i, raw := range checksRaw {
		if json.Unmarshal(raw, &checks[i]) != nil {
			return nil, nil, ErrProvider
		}
	}
	return jobs, checks, nil
}
func (s *Service) completionSelectedCheck(prefix string, selected completion.RequiredCheck, run completionRun, jobs []completionJob, checks []completionCheck) (completion.CheckEvidence, error) {
	e := completion.CheckEvidence{Check: selected, RunID: string(run.ID), Attempt: run.Attempt, TestedSHA: run.HeadSHA, RunStatus: run.Status, RunConclusion: run.Conclusion}
	var job *completionJob
	for i := range jobs {
		if jobs[i].Name == selected.Name {
			if job != nil {
				return e, ErrProvider
			}
			job = &jobs[i]
		}
	}
	if job == nil {
		return e, nil
	}
	if !positiveNumber(string(job.ID)) || job.RunID != run.ID || job.HeadSHA != run.HeadSHA {
		return e, ErrProvider
	}
	checkPrefix := s.config.GitHubAPIURL + prefix + "/check-runs/"
	if !strings.HasPrefix(job.CheckURL, checkPrefix) {
		return e, ErrProvider
	}
	id := strings.TrimPrefix(job.CheckURL, checkPrefix)
	if !positiveNumber(id) {
		return e, ErrProvider
	}
	var check *completionCheck
	for i := range checks {
		if string(checks[i].ID) == id {
			if check != nil {
				return e, ErrProvider
			}
			check = &checks[i]
		}
	}
	if check == nil || string(check.App.ID) != selected.AppID || check.App.Slug != "github-actions" || check.Suite.ID != run.SuiteID || check.HeadSHA != run.HeadSHA || check.Name != selected.Name {
		return e, ErrProvider
	}
	e.JobID, e.CheckRunID, e.Status, e.Conclusion = string(job.ID), id, job.Status, job.Conclusion
	if check.Status != "completed" || check.Conclusion != "success" {
		e.Status, e.Conclusion = check.Status, check.Conclusion
	}
	// Persist only a canonical GitHub link, never an arbitrary provider-supplied URL.
	e.URL = "https://github.com/" + strings.TrimPrefix(prefix, "/repos/") + "/actions/runs/" + e.RunID + "/job/" + e.JobID
	return e, nil
}

func (s *Service) collectCompletionGitHub(ctx context.Context, token string, pr prreviews.PRIdentity, selected []completion.RequiredCheck, accepted *completion.Observation) (completion.Observation, error) {
	o := completion.Observation{Checks: []completion.CheckEvidence{}}
	var err error
	o.PR, o.PRState, o.MergeSHA, err = s.inspectCompletionPR(ctx, token, prreviews.LaunchSnapshot{PR: pr}, PublisherIdentity{Mode: "app"})
	if err != nil {
		return o, err
	}
	o.Merged = o.PRState == "merged"
	prefix := "/repos/" + o.PR.RepositoryName
	proofs := map[string]completion.VerifiedRevision{}
	shas := []string{o.PR.HeadSHA}
	if o.Merged {
		if accepted == nil || !accepted.Revision.Valid() || accepted.PR.Number != o.PR.Number || accepted.PR.GitHubRepositoryID != o.PR.GitHubRepositoryID || accepted.PR.InstallationID != o.PR.InstallationID || accepted.Revision.HeadSHA != o.PR.HeadSHA {
			o.ErrorCode = "merge_revision_unverified"
			return o, nil
		}
		o.Revision = accepted.Revision
		proofs[o.Revision.TestedSHA] = o.Revision
		shas = []string{o.Revision.TestedSHA}
		merged, e := s.completionCommit(ctx, token, prefix, o.MergeSHA)
		if e != nil {
			return o, e
		}
		if merged.Tree.SHA == o.Revision.TreeSHA {
			switch len(merged.Parents) {
			case 2:
				o.MergeVerified = merged.Parents[0].SHA == o.Revision.BaseSHA && merged.Parents[1].SHA == o.Revision.HeadSHA
			case 1:
				o.MergeVerified, err = s.completionAncestor(ctx, token, prefix, o.Revision.BaseSHA, o.MergeSHA)
			}
		}
		if err != nil {
			return o, err
		}
	} else if o.PRState == "open" {
		if prreviews.CommitPattern.MatchString(o.MergeSHA) && o.MergeSHA != o.PR.HeadSHA {
			shas = append(shas, o.MergeSHA)
			merge, e := s.completionCommit(ctx, token, prefix, o.MergeSHA)
			if e != nil {
				return o, e
			}
			if len(merge.Parents) == 2 && merge.Parents[0].SHA == o.PR.BaseSHA && merge.Parents[1].SHA == o.PR.HeadSHA {
				proofs[o.MergeSHA] = completion.VerifiedRevision{BaseSHA: o.PR.BaseSHA, HeadSHA: o.PR.HeadSHA, TestedSHA: o.MergeSHA, TreeSHA: merge.Tree.SHA}
			}
		}
		contains, e := s.completionAncestor(ctx, token, prefix, o.PR.BaseSHA, o.PR.HeadSHA)
		if e != nil {
			return o, e
		}
		if contains {
			head, e := s.completionCommit(ctx, token, prefix, o.PR.HeadSHA)
			if e != nil {
				return o, e
			}
			proofs[o.PR.HeadSHA] = completion.VerifiedRevision{BaseSHA: o.PR.BaseSHA, HeadSHA: o.PR.HeadSHA, TestedSHA: o.PR.HeadSHA, TreeSHA: head.Tree.SHA}
		}
	} else {
		return o, nil
	}
	if len(proofs) == 0 {
		o.ErrorCode = "ci_revision_unverified"
		return o, nil
	}
	runs := map[string]completionRun{}
	jobs := map[string][]completionJob{}
	checks := map[string][]completionCheck{}
	for _, check := range selected {
		run, ok := runs[check.WorkflowID]
		if !ok {
			run, err = s.completionLatestRun(ctx, token, prefix, check, shas)
			if err != nil {
				return o, err
			}
			runs[check.WorkflowID] = run
			if run.ID != "" && run.Status == "completed" {
				jobs[check.WorkflowID], checks[check.WorkflowID], err = s.completionRunChecks(ctx, token, prefix, run)
				if err != nil {
					return o, err
				}
			}
		}
		if run.ID == "" {
			o.ErrorCode = "ci_required"
			continue
		}
		proof, proven := proofs[run.HeadSHA]
		if !proven || o.Revision.Valid() && o.Revision != proof {
			o.ErrorCode = "ci_revision_unverified"
			continue
		}
		o.Revision = proof
		e, err := s.completionSelectedCheck(prefix, check, run, jobs[check.WorkflowID], checks[check.WorkflowID])
		if err != nil {
			return o, err
		}
		o.Checks = append(o.Checks, e)
	}
	for _, check := range selected {
		if old, ok := runs[check.WorkflowID]; ok {
			latest, e := s.completionLatestRun(ctx, token, prefix, check, shas)
			if e != nil {
				return o, e
			}
			if latest != old {
				o.ErrorCode = "evidence_changed"
			}
			delete(runs, check.WorkflowID)
		}
	}
	last, state, merge, err := s.inspectCompletionPR(ctx, token, prreviews.LaunchSnapshot{PR: pr}, PublisherIdentity{Mode: "app"})
	if err != nil {
		return o, err
	}
	if last != o.PR || state != o.PRState || merge != o.MergeSHA {
		o.ErrorCode = "evidence_changed"
	}
	return o, nil
}
