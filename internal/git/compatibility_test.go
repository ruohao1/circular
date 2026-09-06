package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

// Only the external Git process is fault-injected. All domain operations still
// enter the same public interfaces used by production callers.
func gitWrapper(t *testing.T, base, operation, body string) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CIRCULAR_REAL_GIT", real)
	directory := filepath.Join(base, "git-wrapper-bin")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "git")
	putFile(t, path, "#!/bin/sh\nprevious=\nfor argument in \"$@\"; do\n  if [ \"$previous\" = worktree ] && [ \"$argument\" = "+operation+" ]; then\n"+body+"\n  fi\n  previous=$argument\ndone\nexec \"$CIRCULAR_REAL_GIT\" \"$@\"\n")
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorktreeProcessHelper(t *testing.T) {
	if os.Getenv("CIRCULAR_GIT_PROCESS_HELPER") != "1" {
		t.Skip("separate worker-process fixture")
	}
	base, repository := os.Getenv("CIRCULAR_GIT_PROCESS_ROOT"), os.Getenv("CIRCULAR_GIT_PROCESS_REPOSITORY")
	id, err := uuid.Parse(os.Getenv("CIRCULAR_GIT_PROCESS_RUN"))
	if err != nil {
		t.Fatal(err)
	}
	local := localGit(t, base)
	if _, err := local.Provision(context.Background(), id, repository, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRecoversWorkerProcessCrashesDuringProvisioning(t *testing.T) {
	for _, language := range []string{"go"} {
		for _, point := range []string{"before-add", "after-add", "after-move"} {
			t.Run(language+"/"+point, func(t *testing.T) {
				base := t.TempDir()
				local := localGit(t, base)
				repository, err := local.Checkout(t.Context(), uuid.New(), sourceRepository(t, base))
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.New()
				operation, body := "add", ""
				if point == "after-move" {
					operation = "move"
				}
				if point != "before-add" {
					body = "\"$CIRCULAR_REAL_GIT\" \"$@\" || exit $?\n"
				}
				body += "kill -KILL \"$PPID\"\nexit 77"
				wrapper := gitWrapper(t, base, operation, body)
				t.Setenv("PATH", filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
				var command *exec.Cmd
				if language == "go" {
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					command = exec.CommandContext(t.Context(), executable, "-test.run=^TestWorktreeProcessHelper$")
					command.Env = append(os.Environ(), "CIRCULAR_GIT_PROCESS_HELPER=1", "CIRCULAR_GIT_PROCESS_ROOT="+base, "CIRCULAR_GIT_PROCESS_REPOSITORY="+repository, "CIRCULAR_GIT_PROCESS_RUN="+id.String())
					command.WaitDelay = 3 * time.Second
				}
				if output, err := command.CombinedOutput(); err == nil {
					t.Fatalf("worker did not crash: %s", output)
				}
				w := git.Worktree{RunID: id, RepositoryPath: repository, Path: filepath.Join(base, "worktrees", id.String()), Branch: "circular/run/" + id.String()}
				for range 2 {
					if err := local.Release(t.Context(), w, git.ReleaseOptions{DiscardChanges: true}); err != nil {
						t.Fatalf("recover %s crash %s: %v", language, point, err)
					}
				}
				entries, err := os.ReadDir(filepath.Join(base, "worktrees"))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.Contains(entry.Name(), ".worktree-") || entry.Name() == id.String() || strings.HasSuffix(entry.Name(), ".owner") {
						t.Fatalf("crash recovery left %s", entry.Name())
					}
				}
				if got := string(gitCommand(t, repository, "worktree", "list", "--porcelain")); strings.Count(got, "worktree ") != 1 {
					t.Fatal("crash recovery left registered staging")
				}
			})
		}
	}
}
