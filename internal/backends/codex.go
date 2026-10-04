package backends

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/codexconfig"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

// Codex uses the trusted container entrypoint; Agent configuration can select a
// model and reasoning effort, but cannot select commands, credentials, mounts or network policy.
type Codex struct {
	AuthMode string
	APIKey   string
}

func (c Codex) Prepare(input Input) (Invocation, error) {
	bad := func(message string) (Invocation, error) { return Invocation{}, &Failure{Message: message} }
	mode := c.AuthMode
	if mode == "" {
		mode = "chatgpt"
	}
	switch mode {
	case "chatgpt":
		if c.APIKey != "" {
			return bad("ChatGPT subscription mode requires CIRCULAR_CODEX_API_KEY to be unset; API billing requires explicit api_key mode")
		}
	case "api_key":
		if strings.TrimSpace(c.APIKey) == "" || strings.ContainsRune(c.APIKey, 0) || !utf8.ValidString(c.APIKey) {
			return bad("Codex backend requires a worker-configured API key")
		}
	default:
		return bad("CIRCULAR_CODEX_AUTH_MODE must be chatgpt or api_key")
	}
	settings, err := ValidateCodexConfig(input.Config)
	if err != nil {
		return Invocation{}, err
	}
	prompt := "Follow the Agent instructions and Task below in /workspace. Make repository changes only when the task calls for them, and run relevant checks when appropriate. " +
		"The platform captures the final diff. Git metadata is managed outside this container; " +
		"do not modify .git, commit, or push. Finish with the requested deliverable and an accurate account of any changes and checks.\n\n" +
		"Circular provides MCP tools list_models and propose_agent. When your deliverable recommends reusable agents, first call list_models. Choose a model and a supported reasoning_effort separately for each role, considering the expected work, complexity, uncertainty, and user preferences. A generic Astra default is a fallback when no choice is made, not a requirement to use Astra for every role. Honor explicit user model or effort choices. Do not automatically select maximum effort, invent unsupported model capabilities, or claim prices that the catalog does not provide. Use propose_agent for each useful recommendation with its purpose, complete instructions, selected model and reasoning_effort, and a concise model_reason explaining both choices. Include the recommendation and reason in your report. These tools save proposals for user review; they do not create Agents, start Runs, or change repository files. In your report, say the agents are proposed for review, never that they have been created.\n\n" +
		"Agent instructions:\n" + input.Instructions + "\n\nTask:\n" + input.TaskTitle + "\n\n" + input.TaskDescription
	if input.Kind != "" && input.Kind != runstate.Coding && input.Kind != runstate.PRReview {
		return bad("invalid Codex workload purpose")
	}
	if input.Kind == runstate.PRReview {
		if !prreviews.DigestPattern.MatchString(input.ReviewContextSHA256) {
			return bad("PR review requires a verified input context")
		}
		prompt = reviewPrompt(input.Instructions)
	}
	request := map[string]any{"protocol_version": 1, "prompt": prompt, "model": settings.Model, "auth_mode": mode}
	if input.Kind == runstate.PRReview {
		request["purpose"] = "pr_review"
		request["review_context_sha256"] = input.ReviewContextSHA256
	}
	if settings.ReasoningEffort != "" {
		request["reasoning_effort"] = settings.ReasoningEffort
	}
	if mode == "api_key" {
		request["api_key"] = c.APIKey
	}
	stdin, err := json.Marshal(request)
	if err != nil || len(stdin)+1 > 16*1024*1024 {
		return bad("Codex workload input exceeds the supported limit")
	}
	return Invocation{Stdin: append(stdin, '\n'), Decoder: &codexDecoder{secret: c.APIKey, review: input.Kind == runstate.PRReview}, NetworkEnabled: true, UseCredentials: mode == "chatgpt", StderrText: true, TemporaryStorageMB: 128}, nil
}

// ValidateCodexConfig checks public Agent settings without requiring credentials.
// Empty configurations resolve to Circular's pinned Astra default.
func ValidateCodexConfig(raw json.RawMessage) (codexconfig.Settings, error) {
	bad := func(message string) (codexconfig.Settings, error) {
		return codexconfig.Settings{}, &Failure{Message: message}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := strictValue(decoder, raw, 0)
	var trailing any
	config, ok := value.(map[string]any)
	if !utf8.Valid(raw) || err != nil || !ok || decoder.Decode(&trailing) != io.EOF || !validSurrogates(raw) {
		return bad("invalid Codex backend configuration")
	}
	model, effort := "", ""
	for name, value := range config {
		text, valid := value.(string)
		switch name {
		case "model":
			if !valid || text == "" {
				return bad("Codex model must be a valid model identifier of at most 200 characters")
			}
			model = text
		case "reasoning_effort":
			if !valid || text == "" {
				return bad("invalid Codex reasoning effort")
			}
			effort = text
		default:
			return bad("Codex backend configuration supports only model and reasoning_effort; credentials belong in worker configuration")
		}
	}
	settings, err := codexconfig.Resolve(model, effort)
	if err != nil {
		return bad(err.Error())
	}
	return settings, nil
}

type codexDecoder struct {
	secret    string
	completed bool
	review    bool
}

func (d *codexDecoder) Decode(line []byte, stream runtimes.Stream, number int) ([]Event, error) {
	fail := func(reason string) ([]Event, error) {
		return nil, &Failure{Message: fmt.Sprintf("Codex backend %s at %s line %d", reason, stream, number)}
	}
	// stderr is diagnostic text, not the event protocol. Do not persist it: it
	// can contain authentication diagnostics, prompts or repository-controlled text.
	if stream == runtimes.Stderr {
		return nil, nil
	}
	if stream != runtimes.Stdout || !utf8.Valid(line) {
		return fail("emitted invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	value, err := strictValue(decoder, line, 0)
	var trailing any
	doc, ok := value.(map[string]any)
	if err != nil || !ok || decoder.Decode(&trailing) != io.EOF || !validSurrogates(line) {
		return fail("emitted invalid JSON")
	}
	doc = redactCodex(doc, d.secret).(map[string]any)
	kind, ok := doc["type"].(string)
	if !ok || kind == "" {
		return fail("emitted an event without a type")
	}
	// The trusted workload publishes MCP drafts after the CLI has finished,
	// including after turn.completed. These remain proposals until user review.
	if kind == "circular.pr_review.submitted" {
		if !d.review {
			return fail("emitted review output from a coding workload")
		}
		return decodeReviewRecord(doc), nil
	}
	if kind == "circular.agent.proposed" {
		if d.review {
			return fail("emitted agent proposals from a review workload")
		}
		raw, err := json.Marshal(doc["proposal"])
		if err != nil {
			return fail("emitted an invalid agent proposal")
		}
		draft, err := agentproposals.Decode(raw)
		if err != nil {
			return fail("emitted an invalid agent proposal")
		}
		raw, _ = json.Marshal(draft)
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		return []Event{{Type: "agent.proposed", Source: "circular", Data: data, Raw: doc}}, nil
	}
	if d.completed {
		return fail("emitted an event after turn completion")
	}
	event := func(kind string, data map[string]any) ([]Event, error) {
		return []Event{{Type: kind, Source: "codex", Data: data, Raw: doc}}, nil
	}
	switch kind {
	case "thread.started", "turn.started", "item.started", "item.updated":
		return nil, nil
	case "item.completed":
		item, ok := doc["item"].(map[string]any)
		if !ok {
			return fail("emitted an invalid item")
		}
		itemType, ok := item["type"].(string)
		if !ok || itemType == "" {
			return fail("emitted an invalid item")
		}
		if itemType != "agent_message" {
			return nil, nil
		}
		text, ok := item["text"].(string)
		if !ok {
			return fail("emitted an invalid agent message")
		}
		return event("agent.message.completed", map[string]any{"content": text})
	case "turn.completed":
		usage, ok := doc["usage"].(map[string]any)
		if !ok || !nonnegativeInteger(usage["input_tokens"]) || !nonnegativeInteger(usage["output_tokens"]) {
			return fail("emitted invalid usage")
		}
		d.completed = true
		return event("usage.updated", map[string]any{"input_tokens": usage["input_tokens"], "output_tokens": usage["output_tokens"]})
	case "turn.failed", "error":
		if kind == "error" && doc["code"] == "circular_chatgpt_auth_required" {
			return nil, &Failure{Message: "Codex ChatGPT login is unavailable; run the Circular Codex login command", Raw: doc}
		}
		return nil, &Failure{Message: "Codex backend reported " + kind, Raw: doc}
	default:
		return fail("emitted an unsupported event type")
	}
}

func (d *codexDecoder) Finish() error {
	if !d.completed {
		return &Failure{Message: "Codex backend ended without a completed turn"}
	}
	return nil
}

// Redact the configured credential before either normalized or raw event data
// can reach durable storage, including nested diagnostic fields and keys.
func redactCodex(value any, secret string) any {
	switch value := value.(type) {
	case string:
		if secret != "" {
			return strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[redactCodex(key, secret).(string)] = redactCodex(child, secret)
		}
		return result
	case []any:
		for i, child := range value {
			value[i] = redactCodex(child, secret)
		}
		return value
	}
	return value
}
