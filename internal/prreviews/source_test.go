package prreviews

import (
	"bytes"
	"os"
	"testing"

	"github.com/google/uuid"
)

const legacyContextHash = "929677fedb152f9ebd824b3047fad5f044d6bfbb8eec3d9f5c5e6f977b1c29c2"

func TestReviewSourceExactlyOne(t *testing.T) {
	id, zero := uuid.New(), uuid.Nil
	for _, source := range []Source{{}, {RunID: &zero}, {WorkSessionID: &zero}, {RunID: &id, WorkSessionID: &id}} {
		if source.Validate() == nil {
			t.Fatal("invalid source", source)
		}
	}
	a, b := Source{RunID: &id}, Source{WorkSessionID: &id}
	if a.Validate() != nil || b.Validate() != nil || a.Key() == b.Key() {
		t.Fatal("source identity collision")
	}
	v, _ := validContext(t)
	v.Snapshot.SourceRunID = uuid.Nil
	v.Snapshot.Version = 2
	v.Snapshot.Source = &b
	var err error
	v.Snapshot, err = SealSnapshot(v.Snapshot)
	if err != nil || ValidateContext(v) != nil {
		t.Fatal("external source rejected", err)
	}
}

func TestReviewLegacyContextBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/native-context-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var value Context
	if DecodeStrict(raw, &value) != nil || ValidateContext(value) != nil {
		t.Fatal("legacy context rejected")
	}
	got, err := CanonicalJSON(value)
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatal("native context bytes changed", err)
	}
	hash, err := Fingerprint(value)
	if err != nil || hash != legacyContextHash {
		t.Fatal("legacy hash changed", hash, err)
	}
}
