package git_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestReviewGitIsIndependentOfCacheAndPreservesWorktreeCleanup(t *testing.T) {
	f := testsupport.PrepareReviewWorkspace(t)
	cache := f.Worktree.RepositoryPath
	gitCommand(t, cache, "config", "credential.helper", "!echo must-not-copy")
	gitCommand(t, cache, "config", "http.extraHeader", "must-not-copy")
	putFile(t, filepath.Join(cache, ".git", "hooks", "post-checkout"), "must-not-copy\n")
	if err := f.Git.PreparePRReviewGit(t.Context(), f.Directory); err != nil {
		t.Fatal(err)
	}
	if err := f.Git.PreparePRReviewGit(t.Context(), f.Directory); err != nil {
		t.Fatal("immutable retry", err)
	}
	metadata := filepath.Join(f.Directory, "git")
	probe := func(args ...string) ([]byte, error) {
		t.Helper()
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Env = append(os.Environ(), "GIT_DIR="+metadata, "GIT_WORK_TREE="+f.Worktree.Path, "GIT_OPTIONAL_LOCKS=0")
		return c.CombinedOutput()
	}
	// Moving the cache makes an accidental alternate, commondir, or linked
	// worktree dependency fail, even on a host that could normally read it.
	if err := os.Rename(cache, cache+"-hidden"); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, f.Context.Snapshot.PR.HeadSHA},
		{[]string{"status", "--porcelain"}, ""},
		{[]string{"merge-base", "refs/review/base", "HEAD"}, f.Context.MergeBaseSHA},
		{[]string{"diff", "--name-only", "refs/review/merge-base", "HEAD", "--"}, "changed.txt"},
		{[]string{"show", "refs/review/merge-base:changed.txt"}, "before"},
		{[]string{"remote"}, ""},
	} {
		output, err := probe(check.args...)
		if err != nil || strings.TrimSpace(string(output)) != check.want {
			t.Fatalf("isolated Git %v: %v %s", check.args, err, output)
		}
	}
	if output, err := probe("cat-file", "-e", f.UnrelatedSHA); err == nil {
		t.Fatalf("copied unrelated cache object: %s", output)
	}
	if err := filepath.WalkDir(metadata, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			t.Fatal("linked metadata", path)
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("must-not-copy")) || bytes.Contains(data, []byte(cache)) {
				t.Fatal("shared cache configuration leaked", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cache+"-hidden", cache); err != nil {
		t.Fatal(err)
	}
	if err := f.Git.Release(t.Context(), f.Worktree, git.ReleaseOptions{}); err != nil {
		t.Fatal("host worktree cleanup broken", err)
	}
}

func TestReviewGitRejectsModifiedOrLinkedMetadata(t *testing.T) {
	for _, mutation := range []string{"head", "config", "config symlink", "object alternate", "extra ref", "corrupt object"} {
		t.Run(mutation, func(t *testing.T) {
			f := testsupport.PrepareReviewWorkspace(t)
			if err := f.Git.PreparePRReviewGit(t.Context(), f.Directory); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(f.Directory, "git")
			switch mutation {
			case "head":
				putFile(t, filepath.Join(metadata, "HEAD"), f.Context.Snapshot.PR.BaseSHA+"\n")
			case "config":
				putFile(t, filepath.Join(metadata, "config"), "[include]\npath=/untrusted\n")
			case "config symlink":
				path := filepath.Join(metadata, "config")
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("config.original", path); err != nil {
					t.Fatal(err)
				}
			case "object alternate":
				putFile(t, filepath.Join(metadata, "objects", "info", "alternates"), filepath.Join(f.Worktree.RepositoryPath, ".git", "objects")+"\n")
			case "extra ref":
				putFile(t, filepath.Join(metadata, "refs", "review", "extra"), f.Context.Snapshot.PR.HeadSHA+"\n")
			case "corrupt object":
				id := string(gitCommand(t, metadata, "rev-parse", "HEAD:changed.txt"))
				path := filepath.Join(metadata, "objects", id[:2], id[2:])
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				putFile(t, path, "corrupt object\n")
			}
			if err := f.Git.PreparePRReviewGit(t.Context(), f.Directory); !errors.Is(err, prreviews.ErrSourceInvalid) {
				t.Fatal("mutated metadata was accepted", err)
			}
		})
	}
}
