package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	ErrRepositoryInput      = errors.New("enter a repository name with 1–100 letters, numbers, dots, hyphens or underscores, a valid visibility, and a request key")
	ErrCreationKeyConflict  = errors.New("this request key was already used for different repository settings")
	ErrRepositoryExists     = errors.New("a GitHub repository already exists with this name; import it or choose another name")
	ErrRepositoryPermission = errors.New("GitHub did not allow repository creation; check Administration: read and write permission, installation approval, and account policies")
	createRepositoryName    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	creationKey             = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

type GitHubRepositoryCreate struct {
	InstallationID string `json:"installation_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Visibility     string `json:"visibility"`
	RequestKey     string `json:"request_key"`
}

type GitHubRepositoryCreation struct {
	Status             string `json:"status"`
	RepositoryID       string `json:"repository_id"`
	GitHubRepositoryID string `json:"github_repository_id"`
	Name               string `json:"name"`
	URL                string `json:"url"`
	ManagementURL      string `json:"management_url"`
	Message            string `json:"message"`
}

type creationReceipt struct {
	fingerprint, installation, owner, name, status, code, repositoryID, management string
	provider                                                                       []byte
}

func (s *Service) creationReceipt(ctx context.Context, project, key string) (creationReceipt, error) {
	var r creationReceipt
	err := s.pool.QueryRow(ctx, `SELECT fingerprint,installation_id,owner,name,status,error_code,provider_repository,COALESCE(repository_id::text,''),management_url FROM github_repository_creations WHERE project_id=$1 AND request_key=$2`, project, key).Scan(&r.fingerprint, &r.installation, &r.owner, &r.name, &r.status, &r.code, &r.provider, &r.repositoryID, &r.management)
	return r, err
}

// CreateGitHubRepository binds one external create attempt to a durable key.
// GitHub provides no create idempotency key. Once submitted, a missing receipt
// requires manual inspection/import: observing owner/name alone is not proof
// that this request created it, and must never attach an unrelated repository.
func (s *Service) CreateGitHubRepository(ctx context.Context, project string, input GitHubRepositoryCreate) (GitHubRepositoryCreation, error) {
	if input.Visibility == "" {
		input.Visibility = "private"
	}
	if !positiveNumber(input.InstallationID) || !creationKey.MatchString(input.RequestKey) || !createRepositoryName.MatchString(input.Name) || strings.Trim(input.Name, ".") == "" || strings.HasSuffix(strings.ToLower(input.Name), ".git") || utf8.RuneCountInString(input.Description) > 350 || strings.ContainsAny(input.Description, "\x00\r\n") || (input.Visibility != "private" && input.Visibility != "public") {
		return GitHubRepositoryCreation{}, ErrRepositoryInput
	}
	encoded, _ := json.Marshal(input)
	fingerprint := digest(string(encoded))
	previous, err := s.creationReceipt(ctx, project, input.RequestKey)
	if err == nil {
		if previous.fingerprint != fingerprint {
			return GitHubRepositoryCreation{}, ErrCreationKeyConflict
		}
		if previous.status != "rejected" || (previous.code != "permission" && previous.code != "reconnect") {
			return s.resumeGitHubCreation(ctx, project, input.RequestKey, previous)
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GitHubRepositoryCreation{}, err
	}
	var result GitHubRepositoryCreation
	err = s.withToken(ctx, project, "github", func(token string) error {
		var selected *GitHubInstallation
		for page := 1; page <= 100; {
			accounts, err := s.githubInstallations(ctx, token, page)
			if err != nil {
				return err
			}
			for _, account := range accounts.Items {
				if account.ID == input.InstallationID {
					selected = &account
					break
				}
			}
			if selected != nil || accounts.NextPage == 0 {
				break
			}
			page = accounts.NextPage
		}
		if selected == nil {
			return ErrAccess
		}
		if !selected.CanCreate {
			return ErrRepositoryPermission
		}
		var existing githubRepository
		status, err := s.githubRequest(ctx, token, http.MethodGet, "/repos/"+selected.Account+"/"+input.Name, nil, &existing)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			// A concurrent request with this key may have just completed.
			receipt, readErr := s.creationReceipt(ctx, project, input.RequestKey)
			if readErr == nil {
				if receipt.fingerprint != fingerprint {
					return ErrCreationKeyConflict
				}
				result, readErr = s.resumeGitHubCreation(ctx, project, input.RequestKey, receipt)
				return readErr
			}
			if !errors.Is(readErr, pgx.ErrNoRows) {
				return readErr
			}
			return ErrRepositoryExists
		}
		if status != http.StatusNotFound {
			return githubCreationHTTPError(status)
		}
		var localExists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE project_id=$1 AND lower(name)=lower($2))`, project, selected.Account+"/"+input.Name).Scan(&localExists); err != nil {
			return err
		}
		if localExists {
			receipt, readErr := s.creationReceipt(ctx, project, input.RequestKey)
			if readErr == nil {
				if receipt.fingerprint != fingerprint {
					return ErrCreationKeyConflict
				}
				result, readErr = s.resumeGitHubCreation(ctx, project, input.RequestKey, receipt)
				return readErr
			}
			if !errors.Is(readErr, pgx.ErrNoRows) {
				return readErr
			}
			return ErrRepositoryExists
		}
		// Commit the intent before touching GitHub. Only the winning insert may
		// issue the POST, even if another process retries at the same time.
		command, err := s.pool.Exec(ctx, `INSERT INTO github_repository_creations(project_id,request_key,fingerprint,installation_id,owner,name,status,management_url) VALUES($1,$2,$3,$4,$5,$6,'submitted',$7) ON CONFLICT(project_id,request_key) DO UPDATE SET status='submitted',error_code='',updated_at=now() WHERE github_repository_creations.fingerprint=EXCLUDED.fingerprint AND github_repository_creations.status='rejected' AND github_repository_creations.error_code IN ('permission','reconnect')`, project, input.RequestKey, fingerprint, input.InstallationID, selected.Account, input.Name, selected.ManagementURL)
		if err != nil {
			return err
		}
		if command.RowsAffected() == 0 {
			receipt, err := s.creationReceipt(ctx, project, input.RequestKey)
			if err != nil {
				return err
			}
			if receipt.fingerprint != fingerprint {
				return ErrCreationKeyConflict
			}
			result, err = s.resumeGitHubCreation(ctx, project, input.RequestKey, receipt)
			return err
		}
		receipt := creationReceipt{fingerprint: fingerprint, installation: input.InstallationID, owner: selected.Account, name: input.Name, status: "submitted", management: selected.ManagementURL}
		path := "/user/repos"
		if selected.AccountType == "Organization" {
			path = "/orgs/" + selected.Account + "/repos"
		}
		body, _ := json.Marshal(map[string]any{"name": input.Name, "description": input.Description, "private": input.Visibility == "private", "auto_init": true})
		var created githubRepository
		status, err = s.githubRequest(ctx, token, http.MethodPost, path, body, &created)
		// A lost/cancelled response may conceal a successful creation. Preserve
		// the submitted receipt and never repeat the POST for this intent.
		if err != nil {
			result = receipt.result()
			return nil
		}
		if status != http.StatusCreated {
			if status == 401 || status == 403 || status == 404 || status == 422 {
				problem := githubCreationHTTPError(status)
				code := "permission"
				if status == 401 {
					code = "reconnect"
				}
				if status == 422 {
					code = "exists"
				}
				persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				_, _ = s.pool.Exec(persist, `UPDATE github_repository_creations SET status='rejected',error_code=$3,updated_at=now() WHERE project_id=$1 AND request_key=$2`, project, input.RequestKey, code)
				return problem
			}
			result = receipt.result()
			return nil
		}
		resource, validation := created.public(input.InstallationID)
		if validation != nil || !strings.EqualFold(resource.Name, selected.Account+"/"+input.Name) || resource.Private != (input.Visibility == "private") {
			result = receipt.result()
			return nil
		}
		receipt.provider, _ = json.Marshal(resource)
		receipt.status = "created"
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, saveErr := s.pool.Exec(persist, `UPDATE github_repository_creations SET status='created',provider_repository=$3,updated_at=now() WHERE project_id=$1 AND request_key=$2`, project, input.RequestKey, receipt.provider)
		cancel()
		if saveErr != nil {
			result = receipt.result()
			result.Message = "GitHub created the repository, but Circular could not save the creation receipt. Open it on GitHub and import it; do not create it again."
			return nil
		}
		result = s.attachGitHubCreation(ctx, project, input.RequestKey, receipt, token)
		return nil
	})
	return result, err
}

func githubCreationHTTPError(status int) error {
	switch status {
	case 401:
		return ErrReconnect
	case 403, 404:
		return ErrRepositoryPermission
	case 409, 422:
		return ErrRepositoryExists
	default:
		return ErrProvider
	}
}

func (r creationReceipt) result() GitHubRepositoryCreation {
	result := GitHubRepositoryCreation{Status: "uncertain", Name: r.owner + "/" + r.name, URL: "https://github.com/" + r.owner + "/" + r.name, ManagementURL: r.management, Message: "Circular could not confirm whether GitHub created this repository. Check GitHub and import it if it exists. This request will not create it again."}
	var resource GitHubRepository
	if json.Unmarshal(r.provider, &resource) == nil && resource.ID != "" {
		result.GitHubRepositoryID = resource.ID
		result.Status = "needs_access"
		result.Message = "GitHub created the repository. Allow this repository in the GitHub App installation, then check access again to attach it to Circular."
	}
	if r.status == "attached" && r.repositoryID != "" {
		result.Status = "attached"
		result.RepositoryID = r.repositoryID
		result.Message = "Repository created with a README and attached to this Circular project."
	}
	return result
}

func (s *Service) resumeGitHubCreation(ctx context.Context, project, key string, r creationReceipt) (GitHubRepositoryCreation, error) {
	if r.status == "rejected" {
		switch r.code {
		case "reconnect":
			return GitHubRepositoryCreation{}, ErrReconnect
		case "exists":
			return GitHubRepositoryCreation{}, ErrRepositoryExists
		default:
			return GitHubRepositoryCreation{}, ErrRepositoryPermission
		}
	}
	result := r.result()
	if result.Status == "attached" {
		return result, nil
	}
	// A read may locate the requested name after a lost response, but cannot
	// prove who created it. Report its ID for manual recovery without adopting it.
	if r.status == "submitted" {
		_ = s.withToken(ctx, project, "github", func(token string) error {
			var found githubRepository
			status, err := s.githubRequest(ctx, token, http.MethodGet, "/repos/"+r.owner+"/"+r.name, nil, &found)
			if err == nil && status == 200 {
				if resource, e := found.public(r.installation); e == nil && strings.EqualFold(resource.Name, result.Name) {
					result.GitHubRepositoryID = resource.ID
				}
			}
			return err
		})
		return result, nil
	}
	err := s.withToken(ctx, project, "github", func(token string) error { result = s.attachGitHubCreation(ctx, project, key, r, token); return nil })
	if err != nil {
		result.Message = "GitHub created the repository. Reconnect GitHub or restore its access, then check access again to attach it."
	}
	return result, nil
}

func (s *Service) attachGitHubCreation(ctx context.Context, project, key string, r creationReceipt, token string) GitHubRepositoryCreation {
	result := r.result()
	var resource GitHubRepository
	if json.Unmarshal(r.provider, &resource) != nil {
		return result
	}
	for page := 1; page <= 100; {
		items, err := s.githubRepositories(ctx, token, r.installation, page)
		if err != nil {
			return result
		}
		for _, item := range items.Items {
			if item.ID != resource.ID {
				continue
			}
			if strings.TrimSpace(item.DefaultBranch) == "" {
				result.Message = "GitHub created the repository, but its initial README is not ready yet. Check access again shortly."
				return result
			}
			id, _, err := s.attachGitHubRepository(ctx, project, item)
			if err != nil {
				result.Message = "GitHub created the repository, but Circular could not attach it. Check for a repository with the same name in this project, then check access again."
				return result
			}
			persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, err = s.pool.Exec(persist, `UPDATE github_repository_creations SET status='attached',repository_id=$3,updated_at=now() WHERE project_id=$1 AND request_key=$2`, project, key, id)
			cancel()
			if err != nil {
				result.Message = "The repository was attached, but its creation receipt could not be updated. Check access again to confirm."
				return result
			}
			r.status = "attached"
			r.repositoryID = id
			return r.result()
		}
		if items.NextPage == 0 {
			return result
		}
		page = items.NextPage
	}
	return result
}

// Unlike the general GET helper, creation needs exact HTTP status to distinguish
// a rejected request from an ambiguous outcome. Errors and provider bodies are
// never exposed because they can include credentials or untrusted diagnostics.
func (s *Service) githubRequest(ctx context.Context, token, method, path string, body []byte, output any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.config.GitHubAPIURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrProvider
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Circular")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(req)
	if err != nil {
		return 0, ErrProvider
	}
	defer response.Body.Close()
	if limited := providerRetryError(response.StatusCode, response.Header); limited != nil {
		return response.StatusCode, limited
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 || json.Unmarshal(data, output) != nil {
		return response.StatusCode, fmt.Errorf("%w: invalid repository response", ErrProvider)
	}
	return response.StatusCode, nil
}
