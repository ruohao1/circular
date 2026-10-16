// Package delegations defines bounded, untrusted handoff requests and their outcomes.
package delegations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

const MaxRequestBytes = 128 * 1024

var ErrInvalid = errors.New("invalid delegation request")

type DelegationObjective struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type Request struct {
	AgentID    uuid.UUID           `json:"agent_id"`
	RequestKey string              `json:"request_key"`
	Objective  DelegationObjective `json:"objective"`
}

type Target struct {
	AgentID uuid.UUID `json:"agent_id"`
	Name    string    `json:"name"`
}

func Decode(raw []byte) (Request, error) {
	var request Request
	if len(raw) > MaxRequestBytes || prreviews.DecodeStrict(raw, &request) != nil {
		return Request{}, ErrInvalid
	}
	return Normalize(request)
}

func Normalize(request Request) (Request, error) {
	if request.AgentID == uuid.Nil {
		return Request{}, ErrInvalid
	}
	for _, field := range []struct {
		value *string
		limit int
	}{{&request.RequestKey, 200}, {&request.Objective.Title, 500}, {&request.Objective.Description, 20000}} {
		if !utf8.ValidString(*field.value) || strings.ContainsRune(*field.value, 0) {
			return Request{}, ErrInvalid
		}
		*field.value = strings.TrimSpace(*field.value)
		if *field.value == "" || utf8.RuneCountInString(*field.value) > field.limit {
			return Request{}, ErrInvalid
		}
	}
	raw, _ := json.Marshal(request)
	if len(raw) > MaxRequestBytes {
		return Request{}, ErrInvalid
	}
	return request, nil
}

func (r Request) Fingerprint() string {
	// RequestKey is the replay identity; only the normalized payload is hashed.
	r.RequestKey = ""
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func ValidateTargets(targets []Target) error {
	if len(targets) > 100 {
		return ErrInvalid
	}
	seen := make(map[uuid.UUID]bool, len(targets))
	for _, target := range targets {
		if target.AgentID == uuid.Nil || seen[target.AgentID] || !utf8.ValidString(target.Name) || strings.ContainsRune(target.Name, 0) || strings.TrimSpace(target.Name) == "" || utf8.RuneCountInString(target.Name) > 500 {
			return ErrInvalid
		}
		seen[target.AgentID] = true
	}
	return nil
}

func Allows(targets []Target, id uuid.UUID) bool {
	for _, target := range targets {
		if target.AgentID == id {
			return true
		}
	}
	return false
}
