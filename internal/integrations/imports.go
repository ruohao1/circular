package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func positiveNumber(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func (s *Service) ImportGitHub(ctx context.Context, project, installation, repository string, page int) (string, bool, error) {
	if !positiveNumber(installation) || !positiveNumber(repository) || page < 1 || page > 10000 {
		return "", false, ErrAccess
	}
	var selected *GitHubRepository
	err := s.withToken(ctx, project, "github", func(token string) error {
		resources, err := s.githubRepositories(ctx, token, installation, page)
		if err != nil {
			return err
		}
		for _, item := range resources.Items {
			if item.ID == repository {
				selected = &item
				break
			}
		}
		if selected == nil {
			return ErrAccess
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return s.attachGitHubRepository(ctx, project, *selected)
}

func (s *Service) attachGitHubRepository(ctx context.Context, project string, selected GitHubRepository) (string, bool, error) {
	if strings.TrimSpace(selected.DefaultBranch) == "" {
		return "", false, ErrEmptyRepository
	}
	refs, _ := json.Marshal(map[string]any{"github": map[string]string{"repository_id": selected.ID, "installation_id": selected.InstallationID, "url": selected.URL}})
	var id string
	var inserted bool
	err := s.pool.QueryRow(ctx, `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,$3,$4,$5,$6)
        ON CONFLICT (project_id,(external_refs->'github'->>'repository_id')) WHERE external_refs->'github'->>'repository_id' IS NOT NULL
        DO UPDATE SET clone_url=EXCLUDED.clone_url,default_branch=EXCLUDED.default_branch,external_refs=EXCLUDED.external_refs,updated_at=now()
        RETURNING id,(xmax=0)`, uuid.NewString(), project, selected.Name, selected.CloneURL, selected.DefaultBranch, refs).Scan(&id, &inserted)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return "", false, ErrConflict
	}
	return id, inserted, err
}

func (s *Service) ImportLinear(ctx context.Context, project, repository, issueID string) (string, bool, error) {
	var issue LinearIssue
	var organization account
	err := s.withToken(ctx, project, "linear", func(token string) error {
		var err error
		organization, err = s.identity(ctx, "linear", token)
		if err != nil {
			return err
		}
		issue, err = s.linearIssue(ctx, token, issueID)
		return err
	})
	if err != nil {
		return "", false, err
	}
	link, err := url.Parse(issue.URL)
	if err != nil || link.Scheme != "https" || link.Host != "linear.app" || link.User != nil || link.RawQuery != "" || link.Fragment != "" {
		return "", false, ErrProvider
	}
	if strings.TrimSpace(issue.Title) == "" || utf8.RuneCountInString(issue.Title) > 500 {
		return "", false, ErrIssueTitle
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer rollback(ctx, tx)
	var repositoryProject string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM repositories WHERE id=$1`, repository).Scan(&repositoryProject); err != nil {
		return "", false, ErrAccess
	}
	if repositoryProject != project {
		return "", false, ErrAccess
	}
	refs, _ := json.Marshal(map[string]any{"linear": map[string]string{"issue_id": issue.ID, "identifier": issue.Identifier, "url": issue.URL, "account_id": organization.ID}})
	var id string
	var inserted bool
	err = tx.QueryRow(ctx, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($1,$2,$3,$4,$5,'open',$6)
        ON CONFLICT (project_id,(external_refs->'linear'->>'issue_id')) WHERE external_refs->'linear'->>'issue_id' IS NOT NULL
        DO UPDATE SET external_refs=tasks.external_refs RETURNING id,(xmax=0)`, uuid.NewString(), project, repository, issue.Title, issue.Description, refs).Scan(&id, &inserted)
	if err != nil {
		return "", false, err
	}
	return id, inserted, tx.Commit(ctx)
}

// GitCredential is called only by trusted clone/fetch operations. Repository
// identity and the exact HTTPS URL are checked again before any token is used.
func (s *Service) GitCredential(ctx context.Context, repository uuid.UUID, cloneURL string) (string, error) {
	var project string
	var references []byte
	err := s.pool.QueryRow(ctx, `SELECT project_id,external_refs FROM repositories WHERE id=$1`, repository).Scan(&project, &references)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var refs struct {
		GitHub *struct {
			RepositoryID string `json:"repository_id"`
		} `json:"github"`
	}
	if json.Unmarshal(references, &refs) != nil {
		return "", ErrAccess
	}
	if refs.GitHub == nil {
		return "", nil
	}
	if !positiveNumber(refs.GitHub.RepositoryID) {
		return "", ErrAccess
	}
	var credential string
	err = s.withGitHubRepository(ctx, project, repository.String(), GitHubClone, func(token string, _ PublisherIdentity) error {
		var response githubRepository
		if err := s.github(ctx, token, "/repositories/"+refs.GitHub.RepositoryID, &response); err != nil {
			return err
		}
		resource, err := response.public("")
		if err != nil || resource.ID != refs.GitHub.RepositoryID || resource.CloneURL != cloneURL {
			return ErrAccess
		}
		credential = token
		return nil
	})
	return credential, err
}
