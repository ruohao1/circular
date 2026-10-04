package prreviews

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Fingerprint canonicalizes nested raw JSON without losing integer identities.
func Fingerprint(value any) (string, error) {
	data, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON gives retained JSON and its semantic fingerprint the same bytes.
func CanonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var canonical any
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}
