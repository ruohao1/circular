// Package agentproposals validates reviewable Agent recommendations. A proposal
// never grants authority to create Agents or start Runs.
package agentproposals

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/codexconfig"
)

const MaxProposals = 12
const MaxDraftBytes = 256 * 1024

var ErrInvalid = errors.New("agent proposal requires a name (1–200 characters), purpose (1–2000), and instructions (1–20000)")
var ErrLimit = errors.New("this Run already has the maximum of 12 agent proposals")

type Input struct {
	Name            string `json:"name" jsonschema:"Short unique role name, at most 200 characters"`
	Purpose         string `json:"purpose" jsonschema:"Why the repository needs this agent and when to use it, at most 2000 characters"`
	Instructions    string `json:"instructions" jsonschema:"Complete reusable agent instructions grounded in the repository, at most 20000 characters"`
	Model           string `json:"model,omitempty" jsonschema:"Model selected for this role from list_models; omitted choices fall back to gpt-6-astra"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"Reasoning effort selected for this role from the chosen model's supported efforts"`
	ModelReason     string `json:"model_reason,omitempty" jsonschema:"Briefly explain why the selected model and reasoning effort fit this role, at most 1000 characters"`
}

type Draft struct {
	ID string `json:"id"`
	Input
}

func Normalize(input Input) (Input, error) {
	for _, field := range []struct {
		text *string
		max  int
	}{{&input.Name, 200}, {&input.Purpose, 2000}, {&input.Instructions, 20000}} {
		*field.text = strings.TrimSpace(*field.text)
		if *field.text == "" || !utf8.ValidString(*field.text) || strings.ContainsRune(*field.text, 0) || utf8.RuneCountInString(*field.text) > field.max {
			return Input{}, ErrInvalid
		}
	}
	input.ModelReason = strings.TrimSpace(input.ModelReason)
	if !utf8.ValidString(input.ModelReason) || strings.ContainsRune(input.ModelReason, 0) || utf8.RuneCountInString(input.ModelReason) > 1000 {
		return Input{}, errors.New("model recommendation reason must be at most 1000 characters of valid text")
	}
	settings, err := codexconfig.Resolve(input.Model, input.ReasoningEffort)
	if err != nil {
		return Input{}, err
	}
	input.Model, input.ReasoningEffort = settings.Model, settings.ReasoningEffort
	return input, nil
}

func (input Input) Config() codexconfig.Settings {
	return codexconfig.Settings{Model: input.Model, ReasoningEffort: input.ReasoningEffort}
}

func (input Input) Fingerprint() string {
	// The explanation is review metadata. Rephrasing it must not duplicate the
	// same configuration, including proposals saved before reasons were supported.
	input.ModelReason = ""
	encoded, _ := json.Marshal(input)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func Decode(raw []byte) (Draft, error) {
	var draft Draft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > MaxDraftBytes || !utf8.Valid(raw) || decoder.Decode(&draft) != nil || decoder.Decode(new(any)) != io.EOF {
		return Draft{}, ErrInvalid
	}
	id, err := uuid.Parse(draft.ID)
	if err != nil || id == uuid.Nil {
		return Draft{}, ErrInvalid
	}
	draft.ID = id.String()
	draft.Input, err = Normalize(draft.Input)
	return draft, err
}
