package git_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

func TestCheckoutFailuresArePrivateAndDoNotPoisonRetry(t *testing.T) {
	base := t.TempDir()
	local, id := localGit(t, base), uuid.New()
	secret := filepath.Join(base, "credential-secret-missing")
	if _, err := local.Checkout(t.Context(), id, secret); !errors.Is(err, git.ErrClone) || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("clone error was not typed and redacted: %v", err)
	}
	source := sourceRepository(t, base)
	path, err := local.Checkout(t.Context(), id, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Checkout(t.Context(), id, secret); !errors.Is(err, git.ErrFetch) || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("fetch error was not typed and redacted: %v", err)
	}
	if string(gitCommand(t, path, "remote", "get-url", "origin")) != source {
		t.Fatal("failed refresh did not restore the prior origin")
	}
	if _, err := local.Checkout(t.Context(), id, source); err != nil {
		t.Fatal(err)
	}
}

func TestGitErrorsExposeOnlyNonNilCauses(t *testing.T) {
	for _, fault := range []*git.Error{
		{Kind: git.ErrClone},
		{Kind: git.ErrProvision, Cause: context.Canceled},
		{Kind: git.ErrProvision, CleanupError: git.ErrCleanup},
	} {
		for _, cause := range fault.Unwrap() {
			if cause == nil {
				t.Fatal("error tree contains a nil cause")
			}
		}
		for _, cause := range []error{fault.Kind, fault.Cause, fault.CleanupError} {
			if cause != nil && !errors.Is(fault, cause) {
				t.Fatalf("error tree lost cause %v", cause)
			}
		}
	}
}

func TestPlatformGitCannotEnableExtTransportOrRunRepositoryHooks(t *testing.T) {
	base := t.TempDir()
	local := localGit(t, base)
	marker := filepath.Join(base, "must-not-execute")
	helper := filepath.Join(base, "transport-helper")
	putFile(t, helper, "#!/bin/sh\ntouch \"$CIRCULAR_TEST_MARKER\"\nexit 1\n")
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CIRCULAR_TEST_MARKER", marker)
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")
	config := filepath.Join(base, "hostile-config")
	putFile(t, config, "[protocol \"ext\"]\nallow = always\n")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	if _, err := local.Checkout(t.Context(), uuid.New(), "ext::"+helper); !errors.Is(err, git.ErrClone) {
		t.Fatalf("external helper accepted: %v", err)
	}
	source := sourceRepository(t, base)
	repository, err := local.Checkout(t.Context(), uuid.New(), source)
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repository, ".git", "hooks", "post-checkout")
	putFile(t, hook, "#!/bin/sh\ntouch \"$CIRCULAR_TEST_MARKER\"\n")
	if err := os.Chmod(hook, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Provision(t.Context(), uuid.New(), repository, "main"); err != nil {
		t.Fatal(err)
	}
	if !absent(marker) {
		t.Fatal("platform Git executed an unapproved helper or hook")
	}
}

func TestRepositoryLocksAreSharedBoundedAndIndependent(t *testing.T) {
	local, w, base := allocated(t)
	lockPath := filepath.Join(base, "cache", "."+filepath.Base(w.RepositoryPath)+".lock")
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	bounded := localGit(t, base, func(c *git.Config) { c.LockTimeout = 50 * time.Millisecond })
	if _, err := bounded.Provision(t.Context(), uuid.New(), w.RepositoryPath, "main"); !errors.Is(err, git.ErrLock) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worktrees did not share the Repository lock: %v", err)
	}
	if _, err := bounded.Checkout(t.Context(), uuid.MustParse(filepath.Base(w.RepositoryPath)), filepath.Join(base, "source")); !errors.Is(err, git.ErrLock) {
		t.Fatalf("cache did not share the Repository lock: %v", err)
	}
	if _, err := local.Checkout(t.Context(), uuid.New(), filepath.Join(base, "source")); err != nil {
		t.Fatalf("unrelated Repository was blocked: %v", err)
	}
}

func TestConcurrentProvisionHasExactlyOneOwner(t *testing.T) {
	local, active, _ := allocated(t)
	id := uuid.New()
	done := make(chan error, 12)
	for range cap(done) {
		go func() { _, err := local.Provision(t.Context(), id, active.RepositoryPath, "main"); done <- err }()
	}
	winners := 0
	for range cap(done) {
		err := <-done
		if err == nil {
			winners++
		} else if !errors.Is(err, git.ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("one Run received %d worktrees", winners)
	}
}

func TestInvalidProvisioningNeverConsumesExistingResources(t *testing.T) {
	local, w, base := allocated(t)
	for _, ref := range []string{"", "--help", "secret-missing-ref", "bad\x00ref", "bad\nref", string([]byte{255})} {
		if _, err := local.Provision(t.Context(), uuid.New(), w.RepositoryPath, ref); !errors.Is(err, git.ErrRef) || strings.Contains(err.Error(), "secret-missing-ref") {
			t.Fatalf("invalid ref not safely rejected: %v", err)
		}
	}
	if _, err := local.Provision(t.Context(), w.RunID, w.RepositoryPath, "main"); !errors.Is(err, git.ErrConflict) {
		t.Fatalf("live Run was reused: %v", err)
	}
	if _, err := local.Provision(t.Context(), uuid.New(), filepath.Join(w.RepositoryPath, "nested"), "main"); !errors.Is(err, git.ErrRepository) {
		t.Fatalf("non-UUID Repository accepted: %v", err)
	}
	id := uuid.New()
	target := filepath.Join(base, "worktrees", id.String())
	if err := os.Symlink(w.Path, target); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Provision(t.Context(), id, w.RepositoryPath, "main"); !errors.Is(err, git.ErrConflict) {
		t.Fatalf("symlink target accepted: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(w.Path, "README.md")); err != nil || string(data) != "first\n" {
		t.Fatal("invalid provision changed existing output")
	}
}

func TestFailedGitCreationRollsBackOnlyItsOwnAllocation(t *testing.T) {
	for _, operation := range []string{"add", "move"} {
		t.Run(operation, func(t *testing.T) {
			_, active, base := allocated(t)
			metadata := gitMetadata(t, active)
			if err := os.RemoveAll(active.Path); err != nil {
				t.Fatal(err)
			}
			wrapper := gitWrapper(t, base, operation, "\"$CIRCULAR_REAL_GIT\" \"$@\" || exit $?\nexit 17")
			local := localGit(t, base, func(c *git.Config) { c.GitExecutable = wrapper })
			id := uuid.New()
			if _, err := local.Provision(t.Context(), id, active.RepositoryPath, "main"); !errors.Is(err, git.ErrProvision) {
				t.Fatalf("failed %s was acknowledged: %v", operation, err)
			}
			if !absent(filepath.Join(base, "worktrees", id.String())) || absent(metadata) {
				t.Fatal("rollback lost exact allocation scoping")
			}
			if registrations := string(gitCommand(t, active.RepositoryPath, "worktree", "list", "--porcelain")); strings.Contains(registrations, id.String()) || !strings.Contains(registrations, active.Path) {
				t.Fatal("rollback pruned unrelated metadata or retained its own")
			}
			if _, err := localGit(t, base).Provision(t.Context(), id, active.RepositoryPath, "main"); err != nil {
				t.Fatalf("failed creation poisoned retry: %v", err)
			}
		})
	}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for absent(path) {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("external Git process did not reach its fault point")
		}
	}
}

func TestCancellationStopsGitAndSettlesRollbackBeforeUnlocking(t *testing.T) {
	_, active, base := allocated(t)
	marker := filepath.Join(base, "started")
	t.Setenv("CIRCULAR_TEST_MARKER", marker)
	wrapper := gitWrapper(t, base, "add", "\"$CIRCULAR_REAL_GIT\" \"$@\" || exit $?\nprintf started > \"$CIRCULAR_TEST_MARKER\"\nexec sleep 30")
	local := localGit(t, base, func(c *git.Config) { c.GitExecutable = wrapper })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	id := uuid.New()
	done := make(chan error, 1)
	go func() { _, err := local.Provision(ctx, id, active.RepositoryPath, "main"); done <- err }()
	waitFile(t, marker)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
	if !absent(filepath.Join(base, "worktrees", id.String())) {
		t.Fatal("cancelled creation leaked")
	}
	if _, err := localGit(t, base, func(c *git.Config) { c.LockTimeout = 100 * time.Millisecond }).Provision(t.Context(), id, active.RepositoryPath, "main"); err != nil {
		t.Fatalf("cancelled Git retained the lock or branch: %v", err)
	}
}

func TestReleaseRefusesUnverifiedOrReplacedOwnershipEvidence(t *testing.T) {
	for _, kind := range []string{"wrong-path", "wrong-branch", "missing-branch", "missing-receipt-and-backpointer", "replacement-directory", "marker-symlink", "marker-fifo", "marker-malformed", "git-fifo", "foreign-backpointer", "foreign-staging"} {
		t.Run(kind, func(t *testing.T) {
			local, w, base := allocated(t)
			marker := filepath.Join(base, "worktrees", "."+w.RunID.String()+".owner")
			outside := filepath.Join(base, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			putFile(t, filepath.Join(outside, "keep"), "preserve\n")
			handle := w
			switch kind {
			case "wrong-path":
				handle.Path = outside
			case "wrong-branch":
				handle.Branch = "circular/run/foreign"
			case "missing-branch":
				if err := os.RemoveAll(w.Path); err != nil {
					t.Fatal(err)
				}
				gitCommand(t, w.RepositoryPath, "update-ref", "-d", "refs/heads/"+w.Branch)
			case "missing-receipt-and-backpointer":
				if err := os.RemoveAll(gitMetadata(t, w)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			case "replacement-directory":
				if err := os.RemoveAll(gitMetadata(t, w)); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(w.Path, w.Path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(w.Path, 0700); err != nil {
					t.Fatal(err)
				}
				putFile(t, filepath.Join(w.Path, "keep"), "new owner\n")
			case "marker-symlink", "marker-fifo", "marker-malformed":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				if kind == "marker-symlink" {
					if err := os.Symlink(filepath.Join(outside, "keep"), marker); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "marker-fifo" {
					if err := syscall.Mkfifo(marker, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "marker-malformed" {
					putFile(t, marker, "partial receipt")
				}
			case "git-fifo", "foreign-backpointer":
				if err := os.RemoveAll(gitMetadata(t, w)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(w.Path, ".git")); err != nil {
					t.Fatal(err)
				}
				if kind == "git-fifo" {
					if err := syscall.Mkfifo(filepath.Join(w.Path, ".git"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					putFile(t, filepath.Join(w.Path, ".git"), "gitdir: "+outside+"\n")
				}
			case "foreign-staging":
				if err := os.Mkdir(filepath.Join(base, "worktrees", "."+w.RunID.String()+".worktree-foreign"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := local.Release(ctx, handle, git.ReleaseOptions{DiscardChanges: true}); !errors.Is(err, git.ErrRelease) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unverified ownership was not safely rejected: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || !bytes.Equal(data, []byte("preserve\n")) {
				t.Fatal("release touched unowned data")
			}
			if kind != "missing-branch" && absent(w.Path) {
				t.Fatal("unverified Run directory was removed")
			}
		})
	}
}

func TestStaleDirectoryCleanupNeverFollowsNestedSymlinks(t *testing.T) {
	local, w, base := allocated(t)
	if err := os.RemoveAll(gitMetadata(t, w)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(outside, "keep"), "preserve\n")
	if err := os.Symlink(outside, filepath.Join(w.Path, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := local.Release(t.Context(), w, git.ReleaseOptions{}); err != nil {
		t.Fatal(err)
	}
	if !absent(w.Path) {
		t.Fatal("stale allocation was not removed")
	}
	if content, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(content) != "preserve\n" {
		t.Fatal("cleanup followed a nested symlink")
	}
}

func TestFileOwnerCannotWrapIntoAnotherUnixIdentity(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("overflow requires a 64-bit Go int")
	}
	base := t.TempDir()
	for _, invalid := range []uint64{(1 << 32) - 1, 1 << 32, (1 << 32) + 65532} {
		_, err := git.NewLocal(git.Config{RepositoryCacheRoot: filepath.Join(base, "cache"), WorktreeRoot: filepath.Join(base, "worktrees"), Owner: &git.FileOwner{UID: int(invalid), GID: 1000}})
		if !errors.Is(err, git.ErrConfiguration) {
			t.Fatalf("invalid Unix identity %d accepted: %v", invalid, err)
		}
	}
}
