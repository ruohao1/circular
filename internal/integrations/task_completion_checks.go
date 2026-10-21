package integrations

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (s *Service) discoverCompletionChecks(ctx context.Context, token string, pr prreviews.PRIdentity) ([]completion.RequiredCheck, error) {
	d := githubDelivery{GitHubRepository: pr.GitHubRepositoryID, Installation: pr.InstallationID, Name: pr.RepositoryName, CredentialApp: true}
	if err := s.deliveryRepository(ctx, token, &d); err != nil {
		return nil, err
	}
	prefix := "/repos/" + pr.RepositoryName
	workflows, err := s.completionPages(ctx, token, prefix+"/actions/workflows", "workflows", false)
	if err != nil {
		return nil, err
	}
	result := []completion.RequiredCheck{}
	for _, raw := range workflows {
		var workflow struct {
			ID    json.Number `json:"id"`
			Path  string      `json:"path"`
			State string      `json:"state"`
		}
		if json.Unmarshal(raw, &workflow) != nil || !positiveNumber(string(workflow.ID)) {
			return nil, ErrProvider
		}
		if workflow.State != "active" || !strings.HasPrefix(workflow.Path, ".github/workflows/") {
			continue
		}
		// Discovery offers jobs from the latest run; readiness separately scans every applicable run.
		var page struct {
			Runs []completionRun `json:"workflow_runs"`
		}
		if err = s.completionGet(ctx, token, prefix+"/actions/workflows/"+string(workflow.ID)+"/runs?per_page=1&page=1", &page); err != nil {
			return nil, err
		}
		if len(page.Runs) == 0 {
			continue
		}
		if len(page.Runs) != 1 {
			return nil, ErrProvider
		}
		run := page.Runs[0]
		if run.WorkflowID != workflow.ID || run.Path != workflow.Path || !positiveNumber(string(run.ID)) || run.Attempt < 1 || !prreviews.CommitPattern.MatchString(run.HeadSHA) {
			return nil, ErrProvider
		}
		jobs, checks, err := s.completionRunChecks(ctx, token, prefix, run)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			var selected *completion.RequiredCheck
			for _, check := range checks {
				if job.CheckURL == s.config.GitHubAPIURL+prefix+"/check-runs/"+string(check.ID) && check.App.Slug == "github-actions" {
					if selected != nil {
						return nil, ErrProvider
					}
					selected = &completion.RequiredCheck{WorkflowID: string(workflow.ID), WorkflowPath: workflow.Path, AppID: string(check.App.ID), Name: job.Name}
				}
			}
			if selected == nil {
				continue
			}
			if _, err = s.completionSelectedCheck(prefix, *selected, run, jobs, checks); err != nil {
				return nil, err
			}
			if completion.ValidateChecks(completion.CheckConfig{Revision: 1, Checks: []completion.RequiredCheck{*selected}}) != nil {
				return nil, ErrProvider
			}
			if slices.Contains(result, *selected) {
				return nil, ErrProvider
			}
			result = append(result, *selected)
			if len(result) > 2000 {
				return nil, ErrProvider
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].WorkflowPath != result[j].WorkflowPath {
			return result[i].WorkflowPath < result[j].WorkflowPath
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *Service) repositoryCheckSetup(ctx context.Context, repo uuid.UUID) (completion.CheckSetup, uuid.UUID, completion.ProviderIdentity, error) {
	v := completion.CheckSetup{Available: []completion.RequiredCheck{}}
	var project uuid.UUID
	var identity completion.ProviderIdentity
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT project_id,name,completion_check_config FROM repositories WHERE id=$1`, repo).Scan(&project, &v.RepositoryName, &raw)
	if err != nil {
		return v, project, identity, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &v.Config); err != nil {
			return v, project, identity, err
		}
	}
	err = s.pool.QueryRow(ctx, `SELECT i.id,i.generation,i.account_id,r.external_refs->'github'->>'repository_id' FROM repositories r JOIN integration_identity_bindings b ON b.project_id=r.project_id AND b.provider='github' JOIN provider_identities i ON i.id=b.identity_id WHERE r.id=$1 AND b.enabled AND i.enabled AND i.status='available' AND i.account_id=r.external_refs->'github'->>'installation_id'`, repo).Scan(&identity.ID, &identity.Generation, &identity.InstallationID, &identity.RepositoryID)
	if err != nil {
		v.Reason = "Connect the GitHub App installation with Actions and Checks read access."
		return v, project, identity, nil
	}
	operation, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	err = s.withGitHubRepository(operation, project.String(), repo.String(), GitHubCompletionRead, func(token string, actor PublisherIdentity) error {
		if actor.IdentityID != identity.ID.String() {
			return ErrIdentityConflict
		}
		var err error
		v.Available, err = s.discoverCompletionChecks(operation, token, prreviews.PRIdentity{RepositoryName: v.RepositoryName, GitHubRepositoryID: identity.RepositoryID, InstallationID: identity.InstallationID})
		return err
	})
	if err != nil {
		v.Available = []completion.RequiredCheck{}
		v.Reason = "GitHub checks are unavailable. Check the App's Actions, Checks, Contents and Pull requests read permissions, then retry."
	}
	return v, project, identity, nil
}
func (s *Service) RepositoryCompletionChecks(ctx context.Context, repo uuid.UUID) (completion.CheckSetup, error) {
	v, _, _, err := s.repositoryCheckSetup(ctx, repo)
	return v, err
}
func (s *Service) SetRepositoryCompletionChecks(ctx context.Context, repo uuid.UUID, expected int64, checks []completion.RequiredCheck) (completion.CheckConfig, error) {
	wanted := completion.CheckConfig{Revision: expected + 1, Checks: checks}
	if completion.ValidateChecks(wanted) != nil {
		return completion.CheckConfig{}, postgres.ErrTaskPlanningInput
	}
	setup, _, identity, err := s.repositoryCheckSetup(ctx, repo)
	if err != nil {
		return completion.CheckConfig{}, err
	}
	if setup.Reason != "" {
		return completion.CheckConfig{}, ErrIdentityUnavailable
	}
	for _, check := range checks {
		if !slices.Contains(setup.Available, check) {
			return completion.CheckConfig{}, postgres.ErrTaskPlanningInput
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return completion.CheckConfig{}, err
	}
	defer rollback(ctx, tx)
	result, err := postgres.SetVerifiedRepositoryChecks(ctx, tx, repo, expected, wanted, identity)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
