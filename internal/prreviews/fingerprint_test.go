package prreviews

import (
	"encoding/json"
	"testing"
)

func TestFingerprintNormalizesConfigurationKeyOrderAndTracksChanges(t *testing.T) {
	a, err := Fingerprint(json.RawMessage(`{"model":"astra","reasoning_effort":"low"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(json.RawMessage(`{ "reasoning_effort": "low", "model": "astra" }`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("configuration key order changed review identity")
	}
	c, _ := Fingerprint(json.RawMessage(`{"model":"astra","reasoning_effort":"high"}`))
	if a == c {
		t.Fatal("configuration change did not change review identity")
	}
}
