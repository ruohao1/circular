package integrations_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
)

func githubAppFixture(t *testing.T) fixture {
	t.Helper()
	f, config := setupApps(t)
	f.config = config
	registration, err := f.service.BeginGitHubRegistration(t.Context(), f.project, "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := f.provider.ManifestCode(registration.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.CompleteGitHubRegistration(t.Context(), registration.State, registration.Browser, code); err != nil {
		t.Fatal(err)
	}
	f.connect(t, "github")
	return f
}

func TestInstallationTokenFailuresNeverUseHumanCredential(t *testing.T) {
	for _, problem := range []string{"suspended", "missing_repository", "expired_token"} {
		t.Run(problem, func(t *testing.T) {
			f := githubAppFixture(t)
			repo, _, err := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.SaveGitHubIdentity(t.Context(), f.project, nil); err != nil {
				t.Fatal(err)
			}
			switch problem {
			case "suspended":
				f.provider.SuspendedInstallation.Store(true)
			case "missing_repository":
				f.provider.MissingInstallationRepository.Store(true)
			case "expired_token":
				f.provider.ExpiredInstallationToken.Store(true)
			}
			if token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/fixture/private-source.git"); err == nil || token != "" {
				t.Fatal("failed app credential fell back", err)
			}
		})
	}
}

func TestGitHubIdentityUsesScopedInstallationTokenAndNoFallback(t *testing.T) {
	f := githubAppFixture(t)
	repo, _, err := f.service.ImportGitHub(t.Context(), f.project, "101", "202", 1)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.service.SaveGitHubIdentity(t.Context(), f.project, f.provider.GitHubPrivateKey())
	if err != nil || status.Mode != "app" || status.ActorID != "501" {
		t.Fatal(status, err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/fixture/private-source.git")
			if err == nil && !strings.HasPrefix(token, "ghs_") {
				err = errors.New("used human token")
			}
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	scopes := f.provider.InstallationScopes()
	if len(scopes) != 1 || len(scopes[0].RepositoryIDs) != 1 || scopes[0].RepositoryIDs[0] != 202 || scopes[0].Permissions["contents"] != "read" || len(scopes[0].Permissions) != 2 {
		t.Fatal("credential was not narrowly scoped or cached", scopes)
	}
	if token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/foreign/repo.git"); token != "" || !errors.Is(err, integrations.ErrAccess) {
		t.Fatal("supplied credential to wrong remote", err)
	}
	if err := f.service.DetachIdentity(t.Context(), f.project, "github"); err != nil {
		t.Fatal(err)
	}
	if token, err := f.service.GitCredential(t.Context(), uuid.MustParse(repo), "https://github.com/fixture/private-source.git"); token != "" || !errors.Is(err, integrations.ErrIdentityUnavailable) {
		t.Fatal("app failure fell back to user", err)
	}
}

func TestGitHubIdentityRejectsInvalidReplacementAndTracksRename(t *testing.T) {
	f := githubAppFixture(t)
	before := func() []byte {
		var value []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integration_apps WHERE provider='github'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}()
	for _, key := range [][]byte{[]byte("not a key"), bytes.Repeat([]byte{'x'}, 65537)} {
		if _, err := f.service.SaveGitHubIdentity(t.Context(), f.project, key); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	f.provider.WrongGitHubApp.Store(true)
	if _, err := f.service.SaveGitHubIdentity(t.Context(), f.project, f.provider.GitHubPrivateKey()); !errors.Is(err, integrations.ErrAppIdentity) {
		t.Fatal("different app accepted", err)
	}
	var after []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integration_apps WHERE provider='github'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid replacement changed stored credentials")
	}
	f.provider.WrongGitHubApp.Store(false)
	f.provider.RenameGitHubApp("renamed-circular")
	status, err := f.service.SaveGitHubIdentity(t.Context(), f.project, f.provider.GitHubPrivateKey())
	if err != nil || status.ActorLogin != "renamed-circular[bot]" {
		t.Fatal("hardcoded bot identity", status, err)
	}
}
