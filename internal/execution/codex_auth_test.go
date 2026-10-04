package execution

import (
	"os"
	"path/filepath"
	"testing"
)

func authConfig(t *testing.T) Config {
	t.Helper()
	t.Chdir(t.TempDir())
	config, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestCodexAuthDefaultsAndPreparation(t *testing.T) {
	config := authConfig(t)
	if config.CodexAuthMode != "chatgpt" || config.CodexAPIKey != "" || config.CodexAuthRoot != config.Docker.CredentialRoot {
		t.Fatal("subscription must be the default and use a dedicated root")
	}
	if _, err := os.Lstat(config.CodexAuthRoot); !os.IsNotExist(err) {
		t.Fatal("config loading must not create auth directories")
	}
	for range 2 {
		if err := PrepareCodexAuth(config); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(config.CodexAuthRoot)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("auth directory must be private")
	}
	entries, err := os.ReadDir(config.CodexAuthRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("preparation must not create or borrow credentials")
	}
}

func TestCodexAuthRejectsSharedOrLinkedPaths(t *testing.T) {
	config := authConfig(t)
	for _, root := range []string{config.ArtifactRoot, filepath.Join(config.ArtifactRoot, "auth"), filepath.Dir(config.ArtifactRoot), config.Git.WorktreeRoot, config.Git.RepositoryCacheRoot} {
		candidate := config
		candidate.CodexAuthRoot = root
		if err := PrepareCodexAuth(candidate); err == nil {
			t.Fatal("shared auth root was accepted")
		}
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.CodexAuthRoot), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, config.CodexAuthRoot); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCodexAuth(config); err == nil {
		t.Fatal("symlink auth root accepted")
	}
}

func TestCodexAuthNeverRepairsAnExistingPublicDirectory(t *testing.T) {
	config := authConfig(t)
	if err := os.MkdirAll(config.CodexAuthRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config.CodexAuthRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCodexAuth(config); err == nil {
		t.Fatal("public auth directory accepted")
	}
	info, _ := os.Stat(config.CodexAuthRoot)
	if info.Mode().Perm() != 0755 {
		t.Fatal("existing directory permissions were changed")
	}
}
