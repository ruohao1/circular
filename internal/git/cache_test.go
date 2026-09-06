package git_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

func gitCommand(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %v: %v\n%s", args, err, output)
	}
	return bytes.TrimSpace(output)
}

func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func sourceRepository(t *testing.T, base string) string {
	t.Helper()
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, source, "init", "--initial-branch=main")
	gitCommand(t, source, "config", "user.name", "Circular Tests")
	gitCommand(t, source, "config", "user.email", "circular@example.invalid")
	putFile(t, filepath.Join(source, "README.md"), "first\n")
	gitCommand(t, source, "add", "README.md")
	gitCommand(t, source, "commit", "--message=first")
	return source
}

func localGit(t *testing.T, base string, options ...func(*git.Config)) *git.Local {
	t.Helper()
	config := git.Config{RepositoryCacheRoot: filepath.Join(base, "cache"), WorktreeRoot: filepath.Join(base, "worktrees")}
	for _, option := range options {
		option(&config)
	}
	local, err := git.NewLocal(config)
	if err != nil {
		t.Fatal(err)
	}
	return local
}

func absent(path string) bool { _, err := os.Lstat(path); return os.IsNotExist(err) }

func TestCheckoutPublishesOneManagedRepositoryAndRefreshesItsBaseRef(t *testing.T) {
	base := t.TempDir()
	source := sourceRepository(t, base)
	local, id := localGit(t, base), uuid.New()
	checkout, err := local.Checkout(context.Background(), id, source)
	if err != nil {
		t.Fatal(err)
	}
	if checkout != filepath.Join(base, "cache", id.String()) || !bytes.Equal(gitCommand(t, checkout, "rev-parse", "HEAD"), gitCommand(t, source, "rev-parse", "HEAD")) {
		t.Fatal("checkout did not publish the requested Repository")
	}
	if !absent(filepath.Join(checkout, "README.md")) {
		t.Fatal("cache checked out files instead of reserving working trees for Runs")
	}
	putFile(t, filepath.Join(source, "README.md"), "second\n")
	gitCommand(t, source, "commit", "--all", "--message=second")
	refreshed, err := local.Checkout(t.Context(), id, source)
	if err != nil || refreshed != checkout {
		t.Fatalf("checkout reuse failed: %v", err)
	}
	if !bytes.Equal(gitCommand(t, refreshed, "rev-parse", "main"), gitCommand(t, source, "rev-parse", "HEAD")) {
		t.Fatal("cache did not refresh the base ref")
	}
	if names, err := os.ReadDir(filepath.Dir(checkout)); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range names {
			if strings.Contains(entry.Name(), ".clone-") {
				t.Fatal("published checkout left private staging behind")
			}
		}
	}
}
