package backends

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runtimes"
)

const testCodexKey = "sk-circular-test-credential"

func TestCodexDraftsAfterCompletionRemainValidatedAndRedacted(t *testing.T) {
	d := &codexDecoder{secret: testCodexKey}
	if _, err := d.Decode([]byte(`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":20}}`), runtimes.Stdout, 1); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"circular.agent.proposed","proposal":{"id":"345927de-00b5-4a8f-ad8d-2a2dcd21f144","name":"Engineer","purpose":"Test","instructions":"Never disclose ` + testCodexKey + `"}}`
	events, err := d.Decode([]byte(line), runtimes.Stdout, 2)
	if err != nil || len(events) != 1 || events[0].Type != "agent.proposed" || events[0].Source != "circular" || events[0].Data["model"] != "gpt-6-astra" {
		t.Fatal("proposal not emitted", err, events)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), testCodexKey) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatal("proposal disclosed credential")
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{`{"type":"circular.agent.proposed","proposal":{}}`, strings.Replace(line, `"name":"Engineer"`, `"name":""`, 1), strings.Replace(line, `"purpose":"Test"`, `"purpose":"Test","project_id":"other"`, 1), `{"type":"item.completed","item":{"type":"agent_message","text":"after completion"}}`} {
		if _, err := d.Decode([]byte(malformed), runtimes.Stdout, 3); err == nil {
			t.Fatal("invalid or out-of-order event accepted", malformed)
		}
	}
}

func TestCodexSubscriptionDefaultHasNoAPIKeyOrFallback(t *testing.T) {
	for _, mode := range []string{"", "chatgpt"} {
		invocation, err := (Codex{AuthMode: mode}).Prepare(Input{Config: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]any
		if err := json.Unmarshal(invocation.Stdin, &request); err != nil || !invocation.UseCredentials || request["auth_mode"] != "chatgpt" {
			t.Fatal("subscription mode must request dedicated auth mount")
		}
		if _, exists := request["api_key"]; exists {
			t.Fatal("subscription request contains API credentials")
		}
		if request["model"] != "gpt-6-astra" || request["reasoning_effort"] != "low" {
			t.Fatal("unconfigured agents must pin Astra with low reasoning", request)
		}
		if _, err := (Codex{AuthMode: mode, APIKey: testCodexKey}).Prepare(Input{Config: json.RawMessage(`{}`)}); err == nil || strings.Contains(err.Error(), testCodexKey) {
			t.Fatal("subscription mode must reject ambiguous API key configuration")
		}
	}
	if _, err := (Codex{AuthMode: "invalid-secret"}).Prepare(Input{Config: json.RawMessage(`{}`)}); err == nil || strings.Contains(err.Error(), "invalid-secret") {
		t.Fatal("invalid authentication mode was accepted or disclosed")
	}
}

func TestCodexPrepare(t *testing.T) {
	invocation, err := (Codex{AuthMode: "api_key", APIKey: testCodexKey}).Prepare(Input{
		RunID: uuid.New(), TaskTitle: "Fix encoding", TaskDescription: "Preserve Unicode",
		Instructions: "Run the relevant checks", Config: json.RawMessage(`{"model":"gpt-5.4"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(invocation.Command) != 0 || !invocation.NetworkEnabled || invocation.UseCredentials || invocation.TemporaryStorageMB != 128 || invocation.Decoder == nil {
		t.Fatalf("unexpected invocation: %+v", invocation)
	}
	if len(invocation.Stdin) == 0 || invocation.Stdin[len(invocation.Stdin)-1] != '\n' {
		t.Fatal("workload request must end with a newline")
	}
	var request map[string]any
	if err := json.Unmarshal(invocation.Stdin, &request); err != nil {
		t.Fatal(err)
	}
	if !fields(request, "protocol_version", "prompt", "model", "auth_mode", "api_key") || request["protocol_version"] != float64(1) || request["model"] != "gpt-5.4" || request["api_key"] != testCodexKey {
		t.Fatalf("request = %#v", request)
	}
	prompt, ok := request["prompt"].(string)
	if !ok || !strings.Contains(prompt, "Agent instructions:\nRun the relevant checks") || !strings.Contains(prompt, "Task:\nFix encoding\n\nPreserve Unicode") {
		t.Fatalf("task context missing from prompt: %q", prompt)
	}
	if strings.Contains(prompt, testCodexKey) {
		t.Fatal("API key must not enter task prompt")
	}
}

func TestCodexPrepareConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, config, message string
	}{
		{"defaults", `{}`, ""},
		{"model identifier", `{"model":"provider/model.v2:latest"}`, ""},
		{"worker credential cannot be supplied by agent", `{"api_key":"agent-key"}`, "Codex backend configuration supports only model and reasoning_effort; credentials belong in worker configuration"},
		{"unknown field", `{"command":"echo injected"}`, "Codex backend configuration supports only model and reasoning_effort; credentials belong in worker configuration"},
		{"network option", `{"network":false}`, "Codex backend configuration supports only model and reasoning_effort; credentials belong in worker configuration"},
		{"effort injection", `{"reasoning_effort":"high\nmodel=evil"}`, "invalid Codex reasoning effort"},
		{"effort type", `{"reasoning_effort":true}`, "invalid Codex reasoning effort"},
		{"effort empty", `{"reasoning_effort":""}`, "invalid Codex reasoning effort"},
		{"unsupported variant", `{"model":"gpt-5.6-luna","reasoning_effort":"ultra"}`, "reasoning effort is not supported by the selected Codex model"},
		{"duplicate fields", `{"model":"one","model":"two"}`, "invalid Codex backend configuration"},
		{"malformed JSON", `{`, "invalid Codex backend configuration"},
		{"trailing JSON", `{} {}`, "invalid Codex backend configuration"},
		{"null", `null`, "invalid Codex backend configuration"},
		{"array", `[]`, "invalid Codex backend configuration"},
		{"invalid Unicode", `{"model":"\ud800"}`, "invalid Codex backend configuration"},
		{"option injection", `{"model":"--dangerously-bypass-approvals-and-sandbox"}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"shell injection", `{"model":"gpt-5.4; echo injected"}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"command substitution", `{"model":"$(echo injected)"}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"newline injection", `{"model":"gpt-5.4\n--option"}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"nonstring model", `{"model":true}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"empty model", `{"model":""}`, "Codex model must be a valid model identifier of at most 200 characters"},
		{"long model", `{"model":"` + strings.Repeat("x", 201) + `"}`, "Codex model must be a valid model identifier of at most 200 characters"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation, err := (Codex{AuthMode: "api_key", APIKey: testCodexKey}).Prepare(Input{Config: json.RawMessage(test.config)})
			if test.message != "" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Message != test.message || failure.Raw != nil {
					t.Fatalf("error = %v, want %q with no raw data", err, test.message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var request map[string]any
			if err := json.Unmarshal(invocation.Stdin, &request); err != nil || request["api_key"] != testCodexKey {
				t.Fatalf("worker credential missing: %#v, %v", request, err)
			}
		})
	}
	for _, key := range []string{"", " \t\n ", "key\x00value", string([]byte{0xff})} {
		_, err := (Codex{AuthMode: "api_key", APIKey: key}).Prepare(Input{Config: json.RawMessage(`{}`)})
		if err == nil || err.Error() != "Codex backend requires a worker-configured API key" {
			t.Fatalf("invalid worker key accepted: %v", err)
		}
	}
	_, err := (Codex{AuthMode: "api_key"}).Prepare(Input{Config: json.RawMessage(`{"api_key":"agent-key"}`)})
	if err == nil || err.Error() != "Codex backend requires a worker-configured API key" {
		t.Fatalf("agent credential bypassed missing worker credential: %v", err)
	}
}

func TestCodexModelAndEffortReachWorkload(t *testing.T) {
	for _, test := range []struct{ config, model, effort string }{
		{`{"model":"gpt-6-astra","reasoning_effort":"ultra"}`, "gpt-6-astra", "ultra"},
		{`{"model":"gpt-5.6-terra"}`, "gpt-5.6-terra", "medium"},
		{`{"model":"custom-model","reasoning_effort":"high"}`, "custom-model", "high"},
	} {
		invocation, err := (Codex{}).Prepare(Input{Config: json.RawMessage(test.config)})
		if err != nil {
			t.Fatal(err)
		}
		var request map[string]any
		if err := json.Unmarshal(invocation.Stdin, &request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != test.model || request["reasoning_effort"] != test.effort {
			t.Fatal("settings did not reach workload request", request)
		}
	}
}

func TestCodexPrepareInputBound(t *testing.T) {
	backend := Codex{AuthMode: "api_key", APIKey: testCodexKey}
	input := Input{Config: json.RawMessage(`{}`)}
	baseline, err := backend.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	input.TaskDescription = strings.Repeat("x", 16*1024*1024-len(baseline.Stdin))
	invocation, err := backend.Prepare(input)
	if err != nil || len(invocation.Stdin) != 16*1024*1024 {
		t.Fatalf("exact input limit rejected: length = %d, error = %v", len(invocation.Stdin), err)
	}
	input.TaskDescription += "x"
	if _, err := backend.Prepare(input); err == nil || err.Error() != "Codex workload input exceeds the supported limit" {
		t.Fatalf("oversized input accepted: %v", err)
	}
}

func TestCodexDecodeTranscript(t *testing.T) {
	decoder := &codexDecoder{secret: testCodexKey}
	transcript := []string{
		`{"type":"thread.started","thread_id":"0195-test"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"go test ./...","status":"in_progress"}}`,
		`{"type":"item.updated","item":{"id":"item_0","type":"command_execution","aggregated_output":"ok"}}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"go test ./...","aggregated_output":"ok","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"Checked the output"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"I fixed the encoding."}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Relevant checks passed."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":9999999999999999999999,"cached_input_tokens":256,"output_tokens":31}}`,
	}
	var events []Event
	for i, line := range transcript {
		decoded, err := decoder.Decode([]byte(line), runtimes.Stdout, i+1)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		events = append(events, decoded...)
	}
	if err := decoder.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "agent.message.completed" || events[1].Type != "agent.message.completed" || events[2].Type != "usage.updated" {
		t.Fatalf("normalized transcript = %#v", events)
	}
	if events[0].Data["content"] != "I fixed the encoding." || events[1].Data["content"] != "Relevant checks passed." || !reflect.DeepEqual(events[2].Data, map[string]any{"input_tokens": json.Number("9999999999999999999999"), "output_tokens": json.Number("31")}) {
		t.Fatalf("normalized data = %#v", events)
	}
	for _, event := range events {
		if event.Source != "codex" || event.Raw["type"] == nil {
			t.Fatalf("original event identity lost: %#v", event)
		}
	}
	if events[2].Raw["usage"].(map[string]any)["cached_input_tokens"] != json.Number("256") {
		t.Fatalf("raw usage was not retained: %#v", events[2].Raw)
	}
}

func TestCodexRequiresSuccessfulTurn(t *testing.T) {
	for _, line := range []string{
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"not proof of completion"}}`,
		`{"type":"turn.failed","error":{"message":"request failed"}}`,
		`{"type":"error","message":"request failed"}`,
	} {
		decoder := &codexDecoder{}
		_, decodeErr := decoder.Decode([]byte(line), runtimes.Stdout, 1)
		if strings.Contains(line, `"type":"turn.failed"`) || strings.Contains(line, `"type":"error"`) {
			var failure *Failure
			if !errors.As(decodeErr, &failure) || failure.Raw == nil {
				t.Fatalf("backend failure lost: %v", decodeErr)
			}
		} else if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		// An otherwise successful process exit cannot substitute for the
		// backend's successful terminal protocol record.
		if err := decoder.Finish(); err == nil || err.Error() != "Codex backend ended without a completed turn" {
			t.Fatalf("incomplete turn accepted: %v", err)
		}
	}
	decoder := &codexDecoder{}
	line := []byte(`{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`)
	if _, err := decoder.Decode(line, runtimes.Stdout, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Decode(line, runtimes.Stdout, 2); err == nil || err.Error() != "Codex backend emitted an event after turn completion at stdout line 2" {
		t.Fatalf("post-completion event accepted: %v", err)
	}
}

func TestCodexProjectsActionableLoginFailure(t *testing.T) {
	decoder := &codexDecoder{}
	_, err := decoder.Decode([]byte(`{"type":"error","code":"circular_chatgpt_auth_required","message":"untrusted details"}`), runtimes.Stdout, 1)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Message != "Codex ChatGPT login is unavailable; run the Circular Codex login command" || failure.Raw == nil {
		t.Fatalf("login failure lost safe action: %v", err)
	}
}

func TestCodexRejectsInvalidUsage(t *testing.T) {
	for _, field := range []string{"input_tokens", "output_tokens"} {
		for _, value := range []string{"true", "1.0", "1e2", "-1", `"12"`, "null", "[]"} {
			t.Run(field+"="+value, func(t *testing.T) {
				usage := `{"input_tokens":0,"output_tokens":0}`
				usage = strings.Replace(usage, `"`+field+`":0`, `"`+field+`":`+value, 1)
				decoder := &codexDecoder{}
				events, err := decoder.Decode([]byte(`{"type":"turn.completed","usage":`+usage+`}`), runtimes.Stdout, 3)
				if len(events) != 0 || err == nil || err.Error() != "Codex backend emitted invalid usage at stdout line 3" || decoder.Finish() == nil {
					t.Fatalf("invalid usage accepted: %#v, %v", events, err)
				}
			})
		}
	}
}

func TestCodexRejectsInvalidRecords(t *testing.T) {
	for _, test := range []struct{ name, line, reason string }{
		{"malformed JSON", `{`, "emitted invalid JSON"},
		{"trailing JSON", `{"type":"turn.started"}{}`, "emitted invalid JSON"},
		{"array", `[]`, "emitted invalid JSON"},
		{"invalid UTF-8", string([]byte{0xff}), "emitted invalid JSON"},
		{"invalid Unicode", `{"type":"item.completed","item":{"type":"agent_message","text":"\ud800"}}`, "emitted invalid JSON"},
		{"duplicate field", `{"type":"turn.started","type":"turn.completed"}`, "emitted invalid JSON"},
		{"nested duplicate", `{"type":"item.completed","item":{"type":"agent_message","text":"one","text":"two"}}`, "emitted invalid JSON"},
		{"missing type", `{}`, "emitted an event without a type"},
		{"type is number", `{"type":1}`, "emitted an event without a type"},
		{"unsupported type", `{"type":"unsupported"}`, "emitted an unsupported event type"},
		{"missing item", `{"type":"item.completed"}`, "emitted an invalid item"},
		{"invalid item", `{"type":"item.completed","item":{"type":false}}`, "emitted an invalid item"},
		{"invalid message", `{"type":"item.completed","item":{"type":"agent_message","text":1}}`, "emitted an invalid agent message"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := &codexDecoder{}
			events, err := decoder.Decode([]byte(test.line), runtimes.Stdout, 9)
			var failure *Failure
			if len(events) != 0 || !errors.As(err, &failure) || failure.Message != "Codex backend "+test.reason+" at stdout line 9" || failure.Raw != nil {
				t.Fatalf("record result = %#v, %v", events, err)
			}
		})
	}
}

func TestCodexIgnoresStderr(t *testing.T) {
	decoder := &codexDecoder{secret: testCodexKey}
	for _, line := range [][]byte{[]byte("authentication diagnostic " + testCodexKey), {0xff}, []byte(`{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`)} {
		events, err := decoder.Decode(line, runtimes.Stderr, 1)
		if len(events) != 0 || err != nil {
			t.Fatalf("stderr was not ignored: %#v, %v", events, err)
		}
	}
	if decoder.Finish() == nil {
		t.Fatal("stderr protocol-looking text completed the turn")
	}
}

func TestCodexRedactsCredential(t *testing.T) {
	decoder := &codexDecoder{secret: testCodexKey}
	line := `{"type":"item.completed","item":{"type":"agent_message","text":"before ` + testCodexKey + ` after"},"metadata":{"` + testCodexKey + `":"embedded ` + testCodexKey + ` value","nested":[{"diagnostic":"` + testCodexKey + `"},["` + testCodexKey + `"]]}}`
	events, err := decoder.Decode([]byte(line), runtimes.Stdout, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("decode = %#v, %v", events, err)
	}
	if events[0].Data["content"] != "before [REDACTED] after" {
		t.Fatalf("normalized message leaked credential: %#v", events[0].Data)
	}
	encoded, err := json.Marshal(events[0])
	if err != nil || strings.Contains(string(encoded), testCodexKey) {
		t.Fatalf("event contains credential: %v", err)
	}
	metadata := events[0].Raw["metadata"].(map[string]any)
	if metadata["[REDACTED]"] != "embedded [REDACTED] value" {
		t.Fatalf("raw keys and values were not redacted: %#v", metadata)
	}
	failureLine := `{"type":"turn.failed","error":{"message":"credential ` + testCodexKey + ` rejected","nested":["` + testCodexKey + `"]}}`
	_, err = decoder.Decode([]byte(failureLine), runtimes.Stdout, 2)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Message != "Codex backend reported turn.failed" {
		t.Fatalf("failure = %v", err)
	}
	encoded, err = json.Marshal(failure.Raw)
	if err != nil || failure.Raw == nil || strings.Contains(string(encoded), testCodexKey) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("backend failure was not redacted: %v", err)
	}
}
