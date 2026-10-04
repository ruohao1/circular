package agentproposals

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestModelReasonsPreserveExistingProposalIdentityAndValidateText(t *testing.T) {
	input := Input{Name: "Engineer", Purpose: "Changes", Instructions: "Read conventions", Model: "gpt-6-astra", ReasoningEffort: "low"}
	// Version 0006 had no reason field. Its stored fingerprint must still match.
	legacy := sha256.Sum256([]byte(`{"name":"Engineer","purpose":"Changes","instructions":"Read conventions","model":"gpt-6-astra","reasoning_effort":"low"}`))
	input.ModelReason = "  High capability with a low effort baseline for well-defined changes.  "
	normalized, err := Normalize(input)
	if err != nil || normalized.ModelReason != strings.TrimSpace(input.ModelReason) || normalized.Fingerprint() != hex.EncodeToString(legacy[:]) {
		t.Fatal("reason changed legacy identity", err)
	}
	for _, reason := range []string{strings.Repeat("界", 1001), "invalid\x00text", string([]byte{0xff})} {
		input.ModelReason = reason
		if _, err := Normalize(input); err == nil {
			t.Fatal("invalid reason accepted")
		}
	}
}
