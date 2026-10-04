package integrations

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var githubName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

type GitHubInstallation struct {
	ID                string `json:"id"`
	Account           string `json:"account"`
	ManagementURL     string `json:"management_url"`
	AccountType       string `json:"account_type"`
	CanCreate         bool   `json:"can_create"`
	PermissionMessage string `json:"permission_message"`
}

type GitHubRepository struct {
	ID             string `json:"id"`
	InstallationID string `json:"installation_id"`
	Name           string `json:"name"`
	CloneURL       string `json:"clone_url"`
	URL            string `json:"url"`
	DefaultBranch  string `json:"default_branch"`
	Private        bool   `json:"private"`
}

type Page[T any] struct {
	Items    []T `json:"items"`
	NextPage int `json:"next_page"`
}

func (s *Service) GitHubInstallations(ctx context.Context, project string, page int) (Page[GitHubInstallation], error) {
	result := Page[GitHubInstallation]{Items: []GitHubInstallation{}}
	err := s.withToken(ctx, project, "github", func(token string) error {
		var err error
		result, err = s.githubInstallations(ctx, token, page)
		return err
	})
	return result, err
}

func (s *Service) githubInstallations(ctx context.Context, token string, page int) (Page[GitHubInstallation], error) {
	result := Page[GitHubInstallation]{Items: []GitHubInstallation{}}
	identity, err := s.identity(ctx, "github", token)
	if err != nil {
		return result, err
	}
	var response struct {
		Total         int `json:"total_count"`
		Installations []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
			Permissions map[string]string `json:"permissions"`
			SuspendedAt *string           `json:"suspended_at"`
		} `json:"installations"`
	}
	if err := s.github(ctx, token, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), &response); err != nil {
		return result, err
	}
	for _, item := range response.Installations {
		if item.ID <= 0 || !organizationName.MatchString(item.Account.Login) {
			return result, ErrProvider
		}
		id := strconv.FormatInt(item.ID, 10)
		value := GitHubInstallation{ID: id, Account: item.Account.Login, AccountType: item.Account.Type}
		switch item.Account.Type {
		case "User":
			value.ManagementURL = s.config.GitHubURL + "/settings/installations/" + id
			if !strings.EqualFold(item.Account.Login, identity.Name) {
				value.PermissionMessage = "Connect this account directly to create repositories for it."
			}
		case "Organization":
			value.ManagementURL = s.config.GitHubURL + "/organizations/" + item.Account.Login + "/settings/installations/" + id
		default:
			value.PermissionMessage = "This account does not support repository creation."
		}
		if value.PermissionMessage == "" {
			switch {
			case item.SuspendedAt != nil:
				value.PermissionMessage = "Resume this GitHub App installation before creating a repository."
			case item.Permissions["administration"] != "write":
				value.PermissionMessage = "Allow Administration: read and write in the GitHub App, then approve the updated installation permissions."
			case item.Permissions["contents"] != "read" && item.Permissions["contents"] != "write":
				value.PermissionMessage = "Allow repository Contents: read access in the GitHub App."
			default:
				value.CanCreate = true
			}
		}
		result.Items = append(result.Items, value)
	}
	if page*100 < response.Total {
		result.NextPage = page + 1
	}
	return result, nil
}

type githubRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (item githubRepository) public(installation string) (GitHubRepository, error) {
	if item.ID <= 0 || !githubName.MatchString(item.FullName) || len(item.FullName) > 200 || len(item.DefaultBranch) > 200 {
		return GitHubRepository{}, ErrProvider
	}
	return GitHubRepository{ID: strconv.FormatInt(item.ID, 10), InstallationID: installation, Name: item.FullName, CloneURL: "https://github.com/" + item.FullName + ".git", URL: "https://github.com/" + item.FullName, DefaultBranch: item.DefaultBranch, Private: item.Private}, nil
}

func (s *Service) githubRepositories(ctx context.Context, token, installation string, page int) (Page[GitHubRepository], error) {
	result := Page[GitHubRepository]{Items: []GitHubRepository{}}
	var response struct {
		Total        int                `json:"total_count"`
		Repositories []githubRepository `json:"repositories"`
	}
	if err := s.github(ctx, token, fmt.Sprintf("/user/installations/%s/repositories?per_page=100&page=%d", installation, page), &response); err != nil {
		return result, err
	}
	for _, item := range response.Repositories {
		repository, err := item.public(installation)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, repository)
	}
	if page*100 < response.Total {
		result.NextPage = page + 1
	}
	return result, nil
}

func (s *Service) GitHubRepositories(ctx context.Context, project, installation string, page int) (Page[GitHubRepository], error) {
	var result Page[GitHubRepository]
	err := s.withToken(ctx, project, "github", func(token string) error {
		var err error
		result, err = s.githubRepositories(ctx, token, installation, page)
		return err
	})
	return result, err
}
