package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthRequiresExactlyOneSupportedAction(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{nil, {"login", "extra"}, {"private-value"}, {"--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "usage: circular-codex-auth login|status|logout\n" {
			t.Fatalf("invalid command: %d %s", code, stderr.String())
		}
	}
}

func TestAuthDotenvErrorsDoNotReflectPrivateConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("PRIVATE_VALUE=\"private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"status"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "cannot read worker .env configuration\n" {
		t.Fatalf("dotenv error: %d %s", code, stderr.String())
	}
}

func TestAuthAPIKeyModeFailsClearlyWithoutPreparingCredentials(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	auth := filepath.Join(directory, "must-not-create")
	t.Setenv("CIRCULAR_CODEX_AUTH_MODE", "api_key")
	t.Setenv("CIRCULAR_CODEX_AUTH_ROOT", auth)
	t.Setenv("CIRCULAR_CODEX_ENABLED", "false")
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"status"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "Codex authentication commands require CIRCULAR_CODEX_AUTH_MODE=chatgpt\n" {
		t.Fatalf("API-key mode authentication command: %d %s", code, stderr.String())
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatal("API-key mode prepared a credential directory")
	}
}

func TestAuthChatGPTModeRejectsAPIKeyAmbiguity(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	auth := filepath.Join(directory, "must-not-create")
	t.Setenv("CIRCULAR_CODEX_AUTH_MODE", "chatgpt")
	t.Setenv("CIRCULAR_CODEX_API_KEY", "private-key-never-reflect")
	t.Setenv("CIRCULAR_CODEX_AUTH_ROOT", auth)
	t.Setenv("CIRCULAR_CODEX_ENABLED", "false")
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"login"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "ChatGPT authentication cannot be combined with CIRCULAR_CODEX_API_KEY\n" {
		t.Fatalf("ambiguous authentication: %d %s", code, stderr.String())
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatal("ambiguous authentication prepared a credential directory")
	}
}

func TestAuthRejectsDaemonCredentialSymlinksAndWorktreeOverlap(t *testing.T) {
	for _, scenario := range []string{"symlink", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			t.Chdir(directory)
			auth := filepath.Join(directory, "must-not-create")
			worktrees := filepath.Join(directory, "host-worktrees")
			hostAuth := worktrees
			if scenario == "symlink" {
				hostAuth = filepath.Join(directory, "host-auth-link")
				if err := os.Symlink(t.TempDir(), hostAuth); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CIRCULAR_CODEX_AUTH_MODE", "chatgpt")
			t.Setenv("CIRCULAR_CODEX_API_KEY", "")
			t.Setenv("CIRCULAR_CODEX_AUTH_ROOT", auth)
			t.Setenv("CIRCULAR_DOCKER_CODEX_AUTH_ROOT", hostAuth)
			t.Setenv("CIRCULAR_DOCKER_WORKTREE_ROOT", worktrees)
			t.Setenv("CIRCULAR_CODEX_ENABLED", "false")
			var stdout, stderr bytes.Buffer
			if code := run(t.Context(), []string{"status"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.String() != "invalid Codex authentication Docker configuration\n" {
				t.Fatalf("invalid daemon authentication root: %d %s", code, stderr.String())
			}
			if _, err := os.Stat(auth); !os.IsNotExist(err) {
				t.Fatal("invalid daemon authentication root prepared local credentials")
			}
		})
	}
}
