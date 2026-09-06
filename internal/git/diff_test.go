package git_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestCaptureProducesApplicableBinaryPatchWithoutChangingLiveIndex(t *testing.T) {
	local, w, base := allocated(t)
	putFile(t, filepath.Join(w.Path, "README.md"), "staged change\n")
	gitCommand(t, w.Path, "add", "README.md")
	putFile(t, filepath.Join(w.Path, "README.md"), "final unstaged change\n")
	putFile(t, filepath.Join(w.Path, "new file.txt"), "new\n")
	binary := bytes.Repeat([]byte{0, 255, 1, 2, 3}, 256)
	if err := os.WriteFile(filepath.Join(w.Path, "asset.bin"), binary, 0600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(gitMetadata(t, w), "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := local.Capture(t.Context(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if diff.ChangedFiles != 3 || !diff.ContainsBinary || diff.Empty() || !bytes.Contains(diff.Content, []byte("GIT binary patch")) || !bytes.Contains(diff.Content, []byte("+final unstaged change")) {
		t.Fatalf("incomplete final patch: %+v", diff)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("diff capture mutated the Run's live index")
	}
	target := filepath.Join(base, "independent-checkout")
	gitCommand(t, base, "clone", "--no-hardlinks", w.RepositoryPath, target)
	patch := filepath.Join(base, "retained.patch")
	putFile(t, patch, string(diff.Content))
	gitCommand(t, target, "apply", "--binary", patch)
	for name, want := range map[string][]byte{"README.md": []byte("final unstaged change\n"), "new file.txt": []byte("new\n"), "asset.bin": binary} {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("retained patch did not reproduce %s: %v", name, err)
		}
	}
}

func TestCaptureSupportsCanonicalRootsContainingNewlines(t *testing.T) {
	base := filepath.Join(t.TempDir(), "newline\nroot")
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
	diff, err := local.Capture(t.Context(), w.Path)
	if err != nil || !diff.Empty() || len(diff.Content) != 0 {
		t.Fatalf("canonical path was rejected: %v", err)
	}
}
