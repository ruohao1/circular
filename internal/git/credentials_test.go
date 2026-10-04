package git_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

func TestGitCredentialsReachOnlyCloneAndFetchWithoutPersistence(t *testing.T) {
	base := t.TempDir()
	source := sourceRepository(t, base)
	executable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(base, "calls.log")
	wrapper := filepath.Join(base, "git-fixture")
	remote := "https://github.com/fixture/private-source.git"
	// Redirect only the fixture remote into an owned local repository. Record
	// synthetic credentials to verify process boundaries without contacting GitHub.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'CALL\n'
  for value do printf 'ARG %%s\n' "$value"; done
  printf 'HEADER_KEY %%s\nHEADER_VALUE %%s\n' "$GIT_CONFIG_KEY_3" "$GIT_CONFIG_VALUE_3"
  printf 'REDIRECT_KEY %%s\nREDIRECT_VALUE %%s\n' "$GIT_CONFIG_KEY_4" "$GIT_CONFIG_VALUE_4"
} >> %s
exec %s -c %s "$@"
`, quote(log), quote(executable), quote("url.file://"+source+".insteadOf="+remote))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	token := "synthetic-first-token"
	id := uuid.New()
	local := localGit(t, base, func(config *git.Config) {
		config.GitExecutable = wrapper
		config.Credential = func(ctx context.Context, got uuid.UUID, url string) (string, error) {
			if got != id || url != remote {
				t.Fatal("credential request lost repository identity")
			}
			return token, nil
		}
	})
	checkout, err := local.Checkout(t.Context(), id, remote)
	if err != nil {
		t.Fatal(err)
	}
	token = "synthetic-rotated-token"
	if _, err := local.Checkout(t.Context(), id, remote); err != nil {
		t.Fatal(err)
	}
	if got := string(gitCommand(t, checkout, "remote", "get-url", "origin")); got != remote {
		t.Fatal("remote contains credentials or fixture rewrite")
	}
	persisted, err := os.ReadFile(filepath.Join(checkout, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "synthetic") || strings.Contains(string(persisted), "Authorization") {
		t.Fatal("credentials persisted in Git config")
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	networkCalls := 0
	for _, block := range strings.Split(string(raw), "CALL\n")[1:] {
		var arguments []string
		values := map[string]string{}
		for line := range strings.SplitSeq(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			if key == "ARG" {
				arguments = append(arguments, value)
			} else {
				values[key] = value
			}
		}
		args := " " + strings.Join(arguments, " ") + " "
		for _, secret := range []string{"synthetic-first-token", "synthetic-rotated-token", base64.StdEncoding.EncodeToString([]byte("x-access-token:synthetic-first-token")), base64.StdEncoding.EncodeToString([]byte("x-access-token:synthetic-rotated-token"))} {
			if strings.Contains(args, secret) {
				t.Fatal("token present in process arguments")
			}
		}
		header := values["HEADER_VALUE"]
		network := strings.Contains(args, " clone ") || strings.Contains(args, " fetch ")
		if !network && header != "" {
			t.Fatal("credential reached a local Git operation")
		}
		if network {
			expected := "synthetic-first-token"
			if networkCalls > 0 {
				expected = "synthetic-rotated-token"
			}
			networkCalls++
			if header != "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+expected)) || values["HEADER_KEY"] != "http."+remote+".extraHeader" || values["REDIRECT_KEY"] != "http."+remote+".followRedirects" || values["REDIRECT_VALUE"] != "false" {
				t.Fatal("network operation did not receive scoped current credentials")
			}
		}
	}
	if networkCalls != 2 {
		t.Fatal("did not verify both clone and fetch")
	}
}

func TestCredentialFailureIsSanitizedBeforeGitStarts(t *testing.T) {
	for _, remote := range []string{"https://github.com/fixture/repository.git", "https://other.example/repository.git", "https://github.com/fixture/repository.git?redirect=1"} {
		local := localGit(t, t.TempDir(), func(config *git.Config) {
			config.GitExecutable = "/missing/git"
			config.Credential = func(context.Context, uuid.UUID, string) (string, error) {
				if remote == "https://github.com/fixture/repository.git" {
					return "", errors.New("synthetic-provider-secret")
				}
				return "synthetic-token", nil
			}
		})
		_, err := local.Checkout(t.Context(), uuid.New(), remote)
		if !errors.Is(err, git.ErrAuthentication) || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("credential failure was leaked or ignored", err)
		}
	}
}
