// Package codexconfig defines Circular's Codex model choices and defaults.
package codexconfig

import (
	"errors"
	"regexp"
	"slices"
)

const DefaultModel = "gpt-6-astra"

type Settings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type Model struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort"`
	ReasoningEfforts       []string `json:"reasoning_efforts"`
}

// Models is the picker catalog verified against the pinned Codex 0.153.4 runner.
// Account access can differ; custom model identifiers remain supported.
func Models() []Model {
	return []Model{
		{DefaultModel, "GPT-6-Astra", "low", []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{"gpt-5.6-sol", "GPT-5.6-Sol", "low", []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{"gpt-5.6-terra", "GPT-5.6-Terra", "medium", []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{"gpt-5.6-luna", "GPT-5.6-Luna", "medium", []string{"low", "medium", "high", "xhigh", "max"}},
		{"gpt-5.5", "GPT-5.5", "medium", []string{"low", "medium", "high", "xhigh"}},
		{"gpt-5.2", "GPT-5.2", "medium", []string{"low", "medium", "high", "xhigh"}},
	}
}

var modelIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,199}$`)

// Resolve pins an omitted model to Astra and validates model/effort pairs.
// An omitted effort uses a known model's default. Custom models can retain the
// CLI's effort default by leaving it empty.
func Resolve(model, effort string) (Settings, error) {
	if model == "" {
		model = DefaultModel
	}
	if !modelIdentifier.MatchString(model) {
		return Settings{}, errors.New("Codex model must be a valid model identifier of at most 200 characters")
	}
	if effort != "" && !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}, effort) {
		return Settings{}, errors.New("invalid Codex reasoning effort")
	}
	for _, option := range Models() {
		if option.ID != model {
			continue
		}
		if effort == "" {
			effort = option.DefaultReasoningEffort
		} else if !slices.Contains(option.ReasoningEfforts, effort) {
			return Settings{}, errors.New("reasoning effort is not supported by the selected Codex model")
		}
		break
	}
	return Settings{Model: model, ReasoningEffort: effort}, nil
}
