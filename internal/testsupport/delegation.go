package testsupport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/runstate"
)

func DelegationConfig(t *testing.T, target uuid.UUID) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"delegation_request": delegations.Request{AgentID: target, RequestKey: "inspect", Objective: delegations.DelegationObjective{Title: "Inspect source", Description: "Check the parent checkpoint"}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func DelegationContext(t *testing.T, root string, parent uuid.UUID) string {
	t.Helper()
	child := uuid.New()
	content := func(id uuid.UUID, name, data string) artifacts.Content {
		uri, _ := artifacts.URI(id, name)
		sum := sha256.Sum256([]byte(data))
		return artifacts.Content{URI: uri, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	progress, report, patch := `{"summary":"Parent checkpoint"}`, "Child report: source inspected", "diff --git a/child.txt b/child.txt\n"
	r, p := content(child, "delegation-report.txt", report), content(child, "git-diff.patch", patch)
	value := delegations.Context{RunID: parent, Checkpoint: delegations.Checkpoint{BaseRef: "main", BaseCommit: strings.Repeat("a", 40), SourceCommit: strings.Repeat("b", 40), InputSHA256: strings.Repeat("c", 64), Source: content(parent, "delegation-source.tar", "source"), Progress: content(parent, "delegation-progress.json", progress)}, Result: delegations.Result{ChildRunID: child, Status: runstate.Succeeded, Report: &r, Patch: &p}}
	if err := delegations.WriteContext(filepath.Join(root, ".delegations", parent.String()), value, map[string]io.Reader{"progress.json": strings.NewReader(progress), "report.txt": strings.NewReader(report), "diff.patch": strings.NewReader(patch)}); err != nil {
		t.Fatal(err)
	}
	digest, err := delegations.ContextFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
