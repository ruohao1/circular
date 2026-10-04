package prreviews

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validContext(t *testing.T) (Context, []byte) {
	t.Helper()
	diff := []byte("diff evidence\n")
	sum := sha256.Sum256(diff)
	snap, err := SealSnapshot(LaunchSnapshot{SourceRunID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), RepositoryID: uuid.New(), Reviewer: ReviewerSnapshot{AgentID: uuid.New(), Backend: "codex", BackendConfig: json.RawMessage(`{}`)}, PR: PRIdentity{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	return Context{ReviewID: uuid.New(), RunID: uuid.New(), Snapshot: snap, MergeBaseSHA: snap.PR.BaseSHA, DiffSHA256: hex.EncodeToString(sum[:]), Files: []ChangedFile{}, Evidence: []Evidence{}, Limitations: []string{}}, diff
}
func TestContextBundleIsImmutableAndRejectsCorruptionAndSymlinks(t *testing.T) {
	v, diff := validContext(t)
	path := filepath.Join(t.TempDir(), v.RunID.String())
	if err := WriteContext(path, v, diff); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteContext(path, v, diff); err != nil {
		t.Fatal("identical retry", err)
	}
	v.Snapshot.TaskTitle = "other"
	if err := WriteContext(path, v, diff); err == nil {
		t.Fatal("changed context replaced")
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "diff.patch")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "diff.patch"), []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(path); err == nil {
		t.Fatal("corrupt diff read")
	}
	if err := os.Remove(filepath.Join(path, "diff.patch")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, diff, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "diff.patch")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(path); err == nil {
		t.Fatal("linked diff read")
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContext(link); err == nil {
		t.Fatal("linked directory read")
	}
}
