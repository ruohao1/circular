package git_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

func TestProvisionCreatesIndependentRunWorktreesWithoutChangingRepositoryBase(t *testing.T) {
	base := t.TempDir()
	source := sourceRepository(t, base)
	gitCommand(t, source, "checkout", "-b", "feature")
	putFile(t, filepath.Join(source, "README.md"), "feature\n")
	gitCommand(t, source, "commit", "--all", "--message=feature")
	featureCommit := gitCommand(t, source, "rev-parse", "HEAD")
	gitCommand(t, source, "checkout", "main")
	mainCommit := gitCommand(t, source, "rev-parse", "HEAD")
	local := localGit(t, base)
	repository, err := local.Checkout(t.Context(), uuid.New(), source)
	if err != nil {
		t.Fatal(err)
	}
	one, err := local.Provision(t.Context(), uuid.New(), repository, "origin/feature")
	if err != nil {
		t.Fatal(err)
	}
	two, err := local.Provision(t.Context(), uuid.New(), repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	if one.Path != filepath.Join(base, "worktrees", one.RunID.String()) || one.Branch != "circular/run/"+one.RunID.String() || one.RepositoryPath != repository {
		t.Fatal("provisioned identity changed")
	}
	if !bytes.Equal(gitCommand(t, one.Path, "rev-parse", "HEAD"), featureCommit) || !bytes.Equal(gitCommand(t, two.Path, "rev-parse", "HEAD"), mainCommit) {
		t.Fatal("Run worktrees did not use their requested refs")
	}
	putFile(t, filepath.Join(one.Path, "README.md"), "first Run changed this\n")
	content, err := os.ReadFile(filepath.Join(two.Path, "README.md"))
	if err != nil || string(content) != "first\n" || !bytes.Equal(gitCommand(t, repository, "rev-parse", "HEAD"), mainCommit) || !absent(filepath.Join(repository, "README.md")) {
		t.Fatal("Run checkout affected another Run or its shared Repository")
	}
	if absent(filepath.Join(base, "worktrees", "."+one.RunID.String()+".owner")) {
		t.Fatal("durable ownership receipt was not installed")
	}
}

func allocated(t *testing.T) (*git.Local, git.Worktree, string) {
	t.Helper()
	base := t.TempDir()
	source := sourceRepository(t, base)
	local := localGit(t, base)
	repository, err := local.Checkout(t.Context(), uuid.New(), source)
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), uuid.New(), repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	return local, w, base
}

func TestReleasePreservesBranchAndRefusesUnretainedOutput(t *testing.T) {
	local, w, _ := allocated(t)
	branchCommit := gitCommand(t, w.RepositoryPath, "rev-parse", w.Branch)
	putFile(t, filepath.Join(w.Path, "README.md"), "must retain\n")
	if err := local.Release(t.Context(), w, git.ReleaseOptions{}); !errors.Is(err, git.ErrRelease) {
		t.Fatalf("dirty worktree was not protected: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(w.Path, "README.md")); err != nil || string(content) != "must retain\n" {
		t.Fatal("release discarded unretained output")
	}
	if err := local.Release(t.Context(), w, git.ReleaseOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	if !absent(w.Path) || !bytes.Equal(gitCommand(t, w.RepositoryPath, "rev-parse", w.Branch), branchCommit) {
		t.Fatal("release removed the Run branch or retained the live worktree")
	}
	if err := local.Release(t.Context(), w, git.ReleaseOptions{}); err != nil {
		t.Fatalf("repeat release was not idempotent: %v", err)
	}
}

func gitMetadata(t *testing.T, w git.Worktree) string {
	t.Helper()
	return string(gitCommand(t, w.Path, "rev-parse", "--path-format=absolute", "--git-dir"))
}

func TestReleaseReconcilesInterruptedDirectoryAndMetadataCleanup(t *testing.T) {
	for _, loss := range []string{"directory", "metadata", "backpointer", "both", "legacy-metadata"} {
		t.Run(loss, func(t *testing.T) {
			local, w, base := allocated(t)
			active, err := local.Provision(t.Context(), uuid.New(), w.RepositoryPath, "main")
			if err != nil {
				t.Fatal(err)
			}
			metadata := gitMetadata(t, w)
			if loss != "directory" {
				if err := os.RemoveAll(metadata); err != nil {
					t.Fatal(err)
				}
			}
			if loss == "directory" || loss == "both" {
				if err := os.RemoveAll(w.Path); err != nil {
					t.Fatal(err)
				}
			}
			if loss == "backpointer" {
				if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			if loss == "legacy-metadata" {
				if err := os.Remove(filepath.Join(base, "worktrees", "."+w.RunID.String()+".owner")); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := local.Release(t.Context(), w, git.ReleaseOptions{}); err != nil {
					t.Fatalf("interrupted %s cleanup was not recovered: %v", loss, err)
				}
			}
			registrations := string(gitCommand(t, w.RepositoryPath, "worktree", "list", "--porcelain"))
			if !absent(w.Path) || !absent(metadata) || strings.Contains(registrations, w.Path) || !strings.Contains(registrations, active.Path) || absent(active.Path) {
				t.Fatal("release lost exact resource scoping")
			}
			gitCommand(t, w.RepositoryPath, "rev-parse", w.Branch)
		})
	}
}

func TestReleaseReconcilesOnlyItsRegisteredPrivateStaging(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory-present", true: "metadata-only"}[missing], func(t *testing.T) {
			local, active, base := allocated(t)
			id := uuid.New()
			w := git.Worktree{RunID: id, RepositoryPath: active.RepositoryPath, Path: filepath.Join(base, "worktrees", id.String()), Branch: "circular/run/" + id.String()}
			staging := filepath.Join(base, "worktrees", "."+id.String()+".worktree-crash")
			gitCommand(t, w.RepositoryPath, "worktree", "add", "-b", w.Branch, staging, "main")
			if missing {
				if err := os.RemoveAll(staging); err != nil {
					t.Fatal(err)
				}
			}
			if err := local.Release(t.Context(), w, git.ReleaseOptions{DiscardChanges: true}); err != nil {
				t.Fatal(err)
			}
			registrations := string(gitCommand(t, w.RepositoryPath, "worktree", "list", "--porcelain"))
			if !absent(staging) || strings.Contains(registrations, staging) || !strings.Contains(registrations, active.Path) {
				t.Fatal("private staging was not removed with exact ownership scoping")
			}
			gitCommand(t, w.RepositoryPath, "rev-parse", w.Branch)
		})
	}
}

func TestProvisionSupportsContainerFileOwnershipWithoutFollowingSymlinks(t *testing.T) {
	base := t.TempDir()
	source := sourceRepository(t, base)
	outside := filepath.Join(base, "outside")
	putFile(t, outside, "do not chown outside\n")
	if err := os.Symlink(outside, filepath.Join(source, "outside-link")); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, source, "add", "outside-link")
	gitCommand(t, source, "commit", "--message=symlink")
	owner := git.FileOwner{UID: os.Getuid(), GID: os.Getgid()}
	if os.Geteuid() == 0 {
		owner = git.FileOwner{UID: 65532, GID: 65532}
	}
	local := localGit(t, base, func(c *git.Config) { c.Owner = &owner })
	repository, err := local.Checkout(t.Context(), uuid.New(), source)
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), uuid.New(), repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{w.Path, filepath.Join(w.Path, "README.md"), filepath.Join(w.Path, "outside-link")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != owner.UID || int(stat.Gid) != owner.GID {
			t.Fatal("container file ownership was not applied")
		}
	}
	info, err := os.Stat(outside)
	if err != nil || int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatal("provision followed an outside symlink during chown")
	}
}
