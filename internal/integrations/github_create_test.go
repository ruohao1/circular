package integrations_test

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func createInput() integrations.GitHubRepositoryCreate {
	return integrations.GitHubRepositoryCreate{InstallationID: "101", Name: "new-project", Description: "A new project", RequestKey: uuid.NewString()}
}

func githubConfig() testsupport.FixtureGitHubCreation {
	return testsupport.FixtureGitHubCreation{Administration: "write", AccountType: "User", Account: "circular-fixture", Attach: true}
}

func TestGitHubCreatePersonalAndOrganizationRepository(t *testing.T) {
	for _, kind := range []string{"User", "Organization"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.connect(t, "github")
			config := githubConfig()
			config.AccountType = kind
			if kind == "Organization" {
				config.Account = "fixture-org"
			}
			f.provider.SetGitHubCreation(config)
			input := createInput()
			if kind == "Organization" {
				input.Visibility = "public"
			}
			result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if err != nil || result.Status != "attached" || result.RepositoryID == "" || result.GitHubRepositoryID != "300" || result.Name != config.Account+"/"+input.Name {
				t.Fatal(result, err)
			}
			items := f.provider.GitHubCreated()
			if len(items) != 1 || !items[0].AutoInit || items[0].Private != (kind == "User") || items[0].Description != input.Description || items[0].Owner != config.Account {
				t.Fatal("wrong provider create body", items)
			}
			var repoID, installation, clone string
			if err := f.pool.QueryRow(t.Context(), `SELECT external_refs->'github'->>'repository_id',external_refs->'github'->>'installation_id',clone_url FROM repositories WHERE id=$1 AND project_id=$2`, result.RepositoryID, f.project).Scan(&repoID, &installation, &clone); err != nil || repoID != "300" || installation != "101" || clone != "https://github.com/"+result.Name+".git" {
				t.Fatal(repoID, installation, clone, err)
			}
			again, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if err != nil || again != result || f.provider.RepositoryCreates.Load() != 1 {
				t.Fatal("retry repeated creation", again, err)
			}
			input.Description = "changed"
			if _, err = f.service.CreateGitHubRepository(t.Context(), f.project, input); !errors.Is(err, integrations.ErrCreationKeyConflict) {
				t.Fatal("key reused for different settings", err)
			}
		})
	}
}

func TestGitHubCreateRequiresEligibleOwnerAndPermissions(t *testing.T) {
	for _, kind := range []string{"read", "other-person", "unknown-owner", "wrong-installation"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.connect(t, "github")
			config := githubConfig()
			input := createInput()
			switch kind {
			case "read":
				config.Administration = "read"
			case "other-person":
				config.Account = "another-user"
			case "unknown-owner":
				config.AccountType = "Enterprise"
			case "wrong-installation":
				input.InstallationID = "999"
			}
			f.provider.SetGitHubCreation(config)
			accounts, err := f.service.GitHubInstallations(t.Context(), f.project, 1)
			if err != nil || len(accounts.Items) != 1 {
				t.Fatal(accounts, err)
			}
			if kind != "wrong-installation" && (accounts.Items[0].CanCreate || accounts.Items[0].PermissionMessage == "") {
				t.Fatal("ineligible owner allowed", accounts)
			}
			_, err = f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if !errors.Is(err, integrations.ErrRepositoryPermission) && !errors.Is(err, integrations.ErrAccess) {
				t.Fatal(err)
			}
			if f.provider.RepositoryCreates.Load() != 0 {
				t.Fatal("ineligible request reached create")
			}
		})
	}
}

func TestGitHubCreateValidatesBeforeProviderAndScopesProject(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	for _, name := range []string{"", ".", "..", "...", "hello.git", "hello.GIT", "../repo", "repo?private=false", "name with spaces", strings.Repeat("x", 101)} {
		input := createInput()
		input.Name = name
		if _, err := f.service.CreateGitHubRepository(t.Context(), f.project, input); !errors.Is(err, integrations.ErrRepositoryInput) {
			t.Fatal(name, err)
		}
	}
	for _, change := range []func(*integrations.GitHubRepositoryCreate){func(i *integrations.GitHubRepositoryCreate) { i.Description = strings.Repeat("é", 351) }, func(i *integrations.GitHubRepositoryCreate) { i.Visibility = "internal" }, func(i *integrations.GitHubRepositoryCreate) { i.RequestKey = "" }, func(i *integrations.GitHubRepositoryCreate) { i.InstallationID = "../101" }} {
		input := createInput()
		change(&input)
		if _, err := f.service.CreateGitHubRepository(t.Context(), f.project, input); !errors.Is(err, integrations.ErrRepositoryInput) {
			t.Fatal(err)
		}
	}
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Other')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateGitHubRepository(t.Context(), other, createInput()); !errors.Is(err, integrations.ErrReconnect) {
		t.Fatal("used another project's connection", err)
	}
	if f.provider.RepositoryCreates.Load() != 0 {
		t.Fatal("invalid input reached create")
	}
}

func TestGitHubCreateConcurrentRetriesOnlySendOnePOST(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	input := createInput()
	var group sync.WaitGroup
	errorsCh := make(chan error, 12)
	for range 12 {
		group.Go(func() { _, err := f.service.CreateGitHubRepository(t.Context(), f.project, input); errorsCh <- err })
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
	if err != nil || result.Status != "attached" || f.provider.RepositoryCreates.Load() != 1 {
		t.Fatal(result, err, f.provider.RepositoryCreates.Load())
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM repositories WHERE project_id=$1`, f.project).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestGitHubCreateRecoversAttachmentWithoutCreatingAgain(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	config := githubConfig()
	config.Attach = false
	f.provider.SetGitHubCreation(config)
	input := createInput()
	result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
	if err != nil || result.Status != "needs_access" || result.GitHubRepositoryID == "" || result.RepositoryID != "" || result.ManagementURL == "" {
		t.Fatal(result, err)
	}
	config.Attach = true
	f.provider.SetGitHubCreation(config)
	result, err = f.service.CreateGitHubRepository(t.Context(), f.project, input)
	if err != nil || result.Status != "attached" || f.provider.RepositoryCreates.Load() != 1 {
		t.Fatal("partial success was recreated", result, err)
	}
}

func TestGitHubCreateAmbiguousResponsesNeverRepeatOrAdoptRepository(t *testing.T) {
	for _, kind := range []string{"lost", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.connect(t, "github")
			config := githubConfig()
			config.LoseResponse = kind == "lost"
			config.Malformed = kind == "malformed"
			f.provider.SetGitHubCreation(config)
			input := createInput()
			result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if err != nil || result.Status != "uncertain" || result.RepositoryID != "" {
				t.Fatal(result, err)
			}
			for range 2 {
				result, err = f.service.CreateGitHubRepository(t.Context(), f.project, input)
				if err != nil || result.Status != "uncertain" || result.RepositoryID != "" || result.GitHubRepositoryID != "300" {
					t.Fatal(result, err)
				}
			}
			if f.provider.RepositoryCreates.Load() != 1 {
				t.Fatal("repeated ambiguous POST")
			}
			var count int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM repositories WHERE project_id=$1`, f.project).Scan(&count); err != nil || count != 0 {
				t.Fatal("adopted unproven repository", count, err)
			}
		})
	}
}

func TestGitHubCreateExplicitRejectionsCanRecoverWithSameKey(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			f := setup(t)
			f.connect(t, "github")
			config := githubConfig()
			config.RejectStatus = status
			f.provider.SetGitHubCreation(config)
			input := createInput()
			_, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if !errors.Is(err, integrations.ErrReconnect) && !errors.Is(err, integrations.ErrRepositoryPermission) {
				t.Fatal(err)
			}
			config.RejectStatus = 0
			f.provider.SetGitHubCreation(config)
			if status == 401 {
				f.connect(t, "github")
			}
			result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
			if err != nil || result.Status != "attached" || f.provider.RepositoryCreates.Load() != 2 || len(f.provider.GitHubCreated()) != 1 {
				t.Fatal(result, err)
			}
		})
	}
}

func TestGitHubCreateDoesNotReplaceExistingRepositories(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	input := createInput()
	result, err := f.service.CreateGitHubRepository(t.Context(), f.project, input)
	if err != nil || result.Status != "attached" {
		t.Fatal(result, err)
	}
	input.RequestKey = uuid.NewString()
	if _, err := f.service.CreateGitHubRepository(t.Context(), f.project, input); !errors.Is(err, integrations.ErrRepositoryExists) {
		t.Fatal(err)
	}
	input.Name = "local-only"
	input.RequestKey = uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'circular-fixture/local-only','https://github.com/elsewhere/repo.git','main','{}')`, uuid.NewString(), f.project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateGitHubRepository(t.Context(), f.project, input); !errors.Is(err, integrations.ErrRepositoryExists) {
		t.Fatal(err)
	}
	if f.provider.RepositoryCreates.Load() != 1 {
		t.Fatal("created over existing repository")
	}
}
