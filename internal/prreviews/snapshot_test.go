package prreviews

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestReviewerSnapshotFreezesDefaultsAndFingerprint(t *testing.T) {
	value := ReviewerSnapshot{AgentID: uuid.New(), Backend: "codex", Name: "Reviewer", Instructions: "Review", BackendConfig: json.RawMessage(`{}`)}
	normal, err := NormalizeReviewer(value)
	if err != nil || normal.Model != "gpt-6-astra" || normal.ReasoningEffort != "low" || normal.Fingerprint == "" {
		t.Fatal("unresolved reviewer settings", normal, err)
	}
	replay, err := NormalizeReviewer(normal)
	if err != nil || replay.Fingerprint != normal.Fingerprint {
		t.Fatal("fingerprint included itself", err)
	}
	value.BackendConfig = json.RawMessage(`{"model":"gpt-5.6-terra","reasoning_effort":"high"}`)
	other, err := NormalizeReviewer(value)
	if err != nil || other.Fingerprint == normal.Fingerprint || other.Model != "gpt-5.6-terra" {
		t.Fatal("model choice changed", err)
	}
	value.BackendConfig = json.RawMessage(`{"command":"untrusted"}`)
	if _, err := NormalizeReviewer(value); err == nil {
		t.Fatal("backend commands accepted")
	}
}
