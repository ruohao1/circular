package git_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
)

func TestDeliveryPreservesRunChangesAndOriginalBaseAfterUpstreamAdvances(t *testing.T) {
	base := t.TempDir()
	source := sourceRepository(t, base)
	for name, content := range map[string]string{
		"delete.txt": "delete me\n", "rename.txt": "rename me\n", "mode.sh": "#!/bin/sh\nexit 0\n", ".gitignore": "ignored.txt\n",
	} {
		putFile(t, filepath.Join(source, name), content)
	}
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "--message=delivery base")
	local, repositoryID, runID := localGit(t, base), uuid.New(), uuid.New()
	repository, err := local.Checkout(t.Context(), repositoryID, source)
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), runID, repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	baseCommit := string(gitCommand(t, source, "rev-parse", "HEAD"))
	baseTree := string(gitCommand(t, source, "rev-parse", "HEAD^{tree}"))
	// Future runtimes may permit commits. Their complete result must still be
	// captured against the original base, rather than only their final HEAD.
	putFile(t, filepath.Join(w.Path, "committed.txt"), "committed by agent\n")
	gitCommand(t, w.Path, "add", "committed.txt")
	gitCommand(t, w.Path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--message=agent commit")
	putFile(t, filepath.Join(w.Path, "README.md"), "final result\n")
	putFile(t, filepath.Join(w.Path, "ignored.txt"), "must never be published")
	putFile(t, filepath.Join(w.Path, "space 雪\nname.txt"), "unicode path\n")
	binary := bytes.Repeat([]byte{0, 255, 128, 2}, 500)
	if err := os.WriteFile(filepath.Join(w.Path, "binary.dat"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(w.Path, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(w.Path, "rename.txt"), filepath.Join(w.Path, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(w.Path, "mode.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside-secret", filepath.Join(w.Path, "link")); err != nil {
		t.Fatal(err)
	}
	diff, err := local.Capture(t.Context(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseCommit != baseCommit || diff.BaseRef != "main" || !bytes.Contains(diff.Content, []byte("committed by agent")) {
		t.Fatalf("capture lost its original base: %+v", diff)
	}
	gitCommand(t, w.Path, "add", "--all")
	expectedTree := string(gitCommand(t, w.Path, "write-tree"))
	putFile(t, filepath.Join(source, "README.md"), "later upstream result\n")
	putFile(t, filepath.Join(source, "upstream-only.txt"), "do not publish or delete\n")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "--message=upstream advanced")
	if _, err := local.Checkout(t.Context(), repositoryID, source); err != nil {
		t.Fatal(err)
	}
	before := cacheFiles(t, repository)
	changes, err := local.PrepareDelivery(t.Context(), repositoryID, runID, diff.Content, diff.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if changes.BaseCommit != baseCommit || changes.BaseTree != baseTree || changes.BaseRef != "main" || changes.TreeSHA != expectedTree {
		t.Fatalf("delivery changed the saved result or its ancestry: %+v", changes)
	}
	if after := cacheFiles(t, repository); !reflect.DeepEqual(before, after) {
		t.Fatal("preparation modified the shared cache, run index, or refs")
	}
	files := map[string]git.DeliveryFile{}
	for _, file := range changes.Files {
		files[file.Path] = file
	}
	for name, expected := range map[string][]byte{
		"README.md": []byte("final result\n"), "committed.txt": []byte("committed by agent\n"), "binary.dat": binary,
		"space 雪\nname.txt": []byte("unicode path\n"), "renamed.txt": []byte("rename me\n"), "link": []byte("../../outside-secret"),
	} {
		if file, ok := files[name]; !ok || file.Delete || !bytes.Equal(file.Content, expected) || len(file.SHA) != 40 {
			t.Fatalf("delivery lost %q: %+v", name, file)
		}
	}
	if len(files) != 9 || !files["delete.txt"].Delete || !files["rename.txt"].Delete || files["mode.sh"].Mode != "100755" || files["link"].Mode != "120000" {
		t.Fatalf("delivery lost file modes/deletions or included unrelated files: %+v", files)
	}
}

func TestDeliveryWorksAfterCleanupAndSupportsVerifiedLegacyBranch(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned", true: "legacy"}[legacy], func(t *testing.T) {
			local, w, _ := allocated(t)
			repositoryID := uuid.MustParse(filepath.Base(w.RepositoryPath))
			putFile(t, filepath.Join(w.Path, "new.txt"), "saved\n")
			diff, err := local.Capture(t.Context(), w.Path)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				removeBasePin(t, w)
			}
			if err := local.Release(t.Context(), w, git.ReleaseOptions{DiscardChanges: true}); err != nil {
				t.Fatal(err)
			}
			if !legacy {
				gitCommand(t, w.RepositoryPath, "update-ref", "-d", "refs/heads/"+w.Branch)
			}
			changes, err := local.PrepareDelivery(t.Context(), repositoryID, w.RunID, diff.Content, diff.BaseCommit)
			if err != nil || len(changes.Files) != 1 || changes.Files[0].Path != "new.txt" || changes.BaseCommit != diff.BaseCommit {
				t.Fatalf("saved output could not be reconstructed after cleanup: %+v %v", changes, err)
			}
		})
	}
}

func TestDeliverySupportsEmptyChangesAndQuotedCacheRoots(t *testing.T) {
	base := filepath.Join(t.TempDir(), "colon: quote\" newline\nroot")
	source := sourceRepository(t, base)
	local, repositoryID, runID := localGit(t, base), uuid.New(), uuid.New()
	repository, err := local.Checkout(t.Context(), repositoryID, source)
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), runID, repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := local.Capture(t.Context(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := local.PrepareDelivery(t.Context(), repositoryID, runID, diff.Content, diff.BaseCommit)
	if err != nil || len(changes.Files) != 0 || changes.BaseTree != changes.TreeSHA {
		t.Fatalf("empty output or quoted object path was not handled: %+v %v", changes, err)
	}
}

func TestDeliveryRejectsMissingForeignAndCorruptBaseIdentity(t *testing.T) {
	for _, scenario := range []string{"missing", "foreign-run", "foreign-repository", "wrong-base", "partial-pin", "corrupt-pin", "symlink-pin", "symlink-directory"} {
		t.Run(scenario, func(t *testing.T) {
			local, w, base := allocated(t)
			repositoryID, runID := uuid.MustParse(filepath.Base(w.RepositoryPath)), w.RunID
			pin := filepath.Join(w.RepositoryPath, ".git", "circular-bases", runID.String()+".json")
			baseCommit := ""
			switch scenario {
			case "missing":
				removeBasePin(t, w)
				gitCommand(t, w.RepositoryPath, "update-ref", "-d", "refs/heads/"+w.Branch)
			case "foreign-run":
				runID = uuid.New()
			case "foreign-repository":
				repositoryID = uuid.New()
			case "wrong-base":
				baseCommit = strings.Repeat("a", 40)
			case "partial-pin":
				if err := os.Remove(pin); err != nil {
					t.Fatal(err)
				}
			case "corrupt-pin":
				putFile(t, pin, `{}`)
			case "symlink-pin":
				copy := filepath.Join(base, "foreign-pin")
				if err := os.Rename(pin, copy); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(copy, pin); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				copy := filepath.Join(w.RepositoryPath, ".git", "other-pins")
				if err := os.Rename(filepath.Dir(pin), copy); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other-pins", filepath.Dir(pin)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := local.PrepareDelivery(t.Context(), repositoryID, runID, nil, baseCommit); !errors.Is(err, git.ErrDelivery) {
				t.Fatalf("unverified base accepted: %v", err)
			}
		})
	}
}

func TestDeliveryRejectsUnsafeUnsupportedAndOversizedChanges(t *testing.T) {
	for _, name := range []string{"traversal", "absolute", "git-metadata", "workflow", "submodule", "too-many-files", "large-blob", "large-patch", "invalid-patch"} {
		t.Run(name, func(t *testing.T) {
			local, w, _ := allocated(t)
			repositoryID := uuid.MustParse(filepath.Base(w.RepositoryPath))
			var patch []byte
			want := git.ErrDelivery
			switch name {
			case "traversal", "absolute", "git-metadata":
				path := map[string]string{"traversal": "../escape", "absolute": "/escape", "git-metadata": ".git/config"}[name]
				patch = []byte("diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+unsafe\n")
			case "invalid-patch":
				patch = []byte("not a patch")
			case "large-patch":
				patch, want = make([]byte, git.MaxDeliveryPatchBytes+1), git.ErrDeliverySize
			case "workflow":
				if err := os.MkdirAll(filepath.Join(w.Path, ".github", "workflows"), 0700); err != nil {
					t.Fatal(err)
				}
				putFile(t, filepath.Join(w.Path, ".github", "workflows", "build.yml"), "name: workflow\n")
				want = git.ErrDeliveryUnsupported
			case "submodule":
				commit := string(gitCommand(t, w.Path, "rev-parse", "HEAD"))
				patch = []byte("diff --git a/module b/module\nnew file mode 160000\nindex 0000000000000000000000000000000000000000.." + commit + "\n--- /dev/null\n+++ b/module\n@@ -0,0 +1 @@\n+Subproject commit " + commit + "\n")
				want = git.ErrDeliveryUnsupported
			case "too-many-files":
				for i := 0; i <= git.MaxDeliveryFiles; i++ {
					putFile(t, filepath.Join(w.Path, uuid.NewString()), "file\n")
				}
				want = git.ErrDeliverySize
			case "large-blob":
				if err := os.WriteFile(filepath.Join(w.Path, "large.bin"), make([]byte, git.MaxDeliveryBlobBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
				want = git.ErrDeliverySize
			}
			if patch == nil {
				diff, err := local.Capture(t.Context(), w.Path)
				if err != nil {
					t.Fatal(err)
				}
				patch = diff.Content
			}
			before := cacheFiles(t, w.RepositoryPath)
			if _, err := local.PrepareDelivery(t.Context(), repositoryID, w.RunID, patch, ""); !errors.Is(err, want) {
				t.Fatalf("unsafe delivery accepted or misclassified: %v", err)
			}
			if after := cacheFiles(t, w.RepositoryPath); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected delivery modified the cache")
			}
		})
	}
}

func TestDeliveryConcurrentPreparationIsIsolatedAndCancellationStopsIt(t *testing.T) {
	local, w, _ := allocated(t)
	putFile(t, filepath.Join(w.Path, "new.txt"), "saved\n")
	diff, err := local.Capture(t.Context(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := uuid.MustParse(filepath.Base(w.RepositoryPath))
	before := cacheFiles(t, w.RepositoryPath)
	var group sync.WaitGroup
	results := make(chan git.DeliveryChanges, 6)
	for range 6 {
		group.Go(func() {
			changes, err := local.PrepareDelivery(t.Context(), repositoryID, w.RunID, diff.Content, diff.BaseCommit)
			if err != nil {
				t.Error(err)
			}
			results <- changes
		})
	}
	group.Wait()
	close(results)
	var expected git.DeliveryChanges
	for changes := range results {
		if expected.TreeSHA == "" {
			expected = changes
		} else if !reflect.DeepEqual(expected, changes) {
			t.Fatal("concurrent delivery results differed")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := local.PrepareDelivery(ctx, repositoryID, w.RunID, diff.Content, diff.BaseCommit); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	if after := cacheFiles(t, w.RepositoryPath); !reflect.DeepEqual(before, after) {
		t.Fatal("parallel delivery modified cache files")
	}
}

func removeBasePin(t *testing.T, w git.Worktree) {
	t.Helper()
	if err := os.Remove(filepath.Join(w.RepositoryPath, ".git", "circular-bases", w.RunID.String()+".json")); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, w.RepositoryPath, "update-ref", "-d", "refs/circular/bases/"+w.RunID.String())
}

func cacheFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			result[strings.TrimPrefix(path, root)] = sha256.Sum256(data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
