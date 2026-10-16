package delegations

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/runstate"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegationContextChecksEveryFile(t *testing.T) {
	id, child := uuid.New(), uuid.New()
	content := func(owner uuid.UUID, name, value string) artifacts.Content {
		uri, _ := artifacts.URI(owner, name)
		sum := sha256.Sum256([]byte(value))
		return artifacts.Content{URI: uri, SizeBytes: int64(len(value)), SHA256: hex.EncodeToString(sum[:])}
	}
	report, patch := content(child, "delegation-report.txt", "report"), content(child, "git-diff.patch", "patch")
	value := Context{RunID: id, Checkpoint: Checkpoint{Source: content(id, "delegation-source.tar", "source"), Progress: content(id, "delegation-progress.json", "[]"), BaseCommit: strings.Repeat("a", 40), SourceCommit: strings.Repeat("b", 40), BaseRef: "main", InputSHA256: strings.Repeat("c", 64)}, Result: Result{ChildRunID: child, Status: runstate.Succeeded, Report: &report, Patch: &patch}}
	for _, name := range []string{"progress.json", "report.txt", "diff.patch"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), id.String())
			files := map[string]io.Reader{"progress.json": strings.NewReader("[]"), "report.txt": strings.NewReader("report"), "diff.patch": strings.NewReader("patch")}
			if err := WriteContext(dir, value, files); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadContext(dir); err != nil {
				t.Fatal(err)
			}
			if err := WriteContext(dir, value, files); err != nil {
				t.Fatal("identical replay", err)
			}
			path := filepath.Join(dir, name)
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadContext(dir); err == nil {
				t.Fatal("changed file accepted")
			}
			os.Remove(path)
			if err := os.Symlink("context.json", path); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadContext(dir); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}
