package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/ruohao1/circular/internal/artifacts"
	"os"
	"path/filepath"
	"testing"
)

func TestDelegationWorkspaceDetectsChanges(t *testing.T) {
	path := t.TempDir()
	file := filepath.Join(path, "source.txt")
	if err := os.WriteFile(file, []byte("checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if err := artifacts.Archive(t.Context(), path, h); err != nil {
		t.Fatal(err)
	}
	source := artifacts.Content{SHA256: hex.EncodeToString(h.Sum(nil))}
	if err := verifyDelegationWorkspace(t.Context(), path, source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyDelegationWorkspace(t.Context(), path, source); err == nil {
		t.Fatal("changed workspace accepted")
	}
}
