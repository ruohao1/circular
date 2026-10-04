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

func TestFakePrepare(t *testing.T) {
	id := uuid.New()
	invocation, err := (Fake{DelayMS: 15}).Prepare(Input{
		RunID: id, TaskTitle: "Fix task", TaskDescription: "Description", Instructions: "Keep the API",
		Config: json.RawMessage(`{"delay_ms":8,"failure":"after_first_event"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(invocation.Command, []string{"--write-output"}) || invocation.NetworkEnabled || invocation.TemporaryStorageMB != 0 {
		t.Fatalf("unexpected runtime policy: %+v", invocation)
	}
	if len(invocation.Stdin) == 0 || invocation.Stdin[len(invocation.Stdin)-1] != '\n' {
		t.Fatal("workload request must end with a newline")
	}
	var request map[string]any
	if err := json.Unmarshal(invocation.Stdin, &request); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"protocol_version": float64(1),
		"run":              map[string]any{"id": id.String(), "task_title": "Fix task", "task_description": "Description", "instructions": "Keep the API"},
		"behavior":         map[string]any{"delay_ms": float64(8), "failure": "after_first_event"},
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("request = %#v, want %#v", request, want)
	}
	if invocation.Decoder == nil || invocation.Decoder.Finish() != nil {
		t.Fatal("fake decoder must allow completion without a terminal protocol record")
	}
}

func TestFakeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, config, message string
		delay                 float64
	}{
		{name: "defaults", config: `{}`, delay: 23},
		{name: "null configuration", config: `null`, delay: 23},
		{name: "unknown fields preserved as ignored", config: `{"future_option":true}`, delay: 23},
		{name: "zero", config: `{"delay_ms":0}`, delay: 0},
		{name: "negative zero", config: `{"delay_ms":-0}`, delay: 0},
		{name: "upper bound", config: `{"delay_ms":10000}`, delay: 10000},
		{name: "negative", config: `{"delay_ms":-1}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "too large", config: `{"delay_ms":10001}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "overflow", config: `{"delay_ms":9999999999999999999999999999}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "fraction", config: `{"delay_ms":1.0}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "exponent", config: `{"delay_ms":1e1}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "string", config: `{"delay_ms":"1"}`, message: "fake delay_ms must be an integer from 0 through 10000"},
		{name: "failure mode", config: `{"failure":"invalid"}`, message: "unsupported fake failure mode"},
		{name: "failure type", config: `{"failure":1}`, message: "unsupported fake failure mode"},
		{name: "invalid JSON", config: `{`, message: "invalid fake backend configuration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation, err := (Fake{DelayMS: 23}).Prepare(Input{RunID: uuid.New(), Config: json.RawMessage(test.config)})
			if test.message != "" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Message != test.message || failure.Raw != nil {
					t.Fatalf("error = %v, want %q with no raw output", err, test.message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Behavior struct {
					Delay   float64 `json:"delay_ms"`
					Failure string  `json:"failure"`
				} `json:"behavior"`
			}
			if err := json.Unmarshal(invocation.Stdin, &request); err != nil {
				t.Fatal(err)
			}
			if request.Behavior.Delay != test.delay || request.Behavior.Failure != "none" {
				t.Fatalf("behavior = %+v", request.Behavior)
			}
		})
	}
}

func TestFakeDecodeEvents(t *testing.T) {
	id := uuid.New()
	for _, test := range []struct {
		kind, data string
		want       map[string]any
	}{
		{"agent.message.delta", `{"delta":"hello"}`, map[string]any{"delta": "hello"}},
		{"agent.message.completed", `{"content":"hello"}`, map[string]any{"content": "hello"}},
		{"usage.updated", `{"input_tokens":999999999999999999999999,"output_tokens":-0}`, map[string]any{"input_tokens": json.Number("999999999999999999999999"), "output_tokens": json.Number("-0")}},
	} {
		t.Run(test.kind, func(t *testing.T) {
			decoder := &fakeDecoder{runID: id}
			line := fakeEvent(id, test.kind, test.data)
			events, err := decoder.Decode([]byte(line), runtimes.Stdout, 4)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("events = %#v", events)
			}
			event := events[0]
			if event.Type != test.kind || event.Source != "fake-container-workload" || !reflect.DeepEqual(event.Data, test.want) {
				t.Fatalf("event = %#v", event)
			}
			if event.Raw["run_id"] != id.String() || event.Raw["protocol_version"] != json.Number("1") || event.Raw["source"] != event.Source || event.Raw["type"] != event.Type || !reflect.DeepEqual(event.Raw["data"], test.want) {
				t.Fatalf("raw output was not preserved: %#v", event.Raw)
			}
		})
	}
}

func TestFakeRejectsInvalidRecords(t *testing.T) {
	id := uuid.New()
	valid := fakeEvent(id, "agent.message.delta", `{"delta":"hello"}`)
	for _, test := range []struct {
		name, line, reason string
		hasRaw             bool
	}{
		{"invalid UTF-8", string([]byte{0xff}), "fake backend emitted an invalid UTF-8 JSON line", false},
		{"malformed", `{`, "fake backend emitted an invalid UTF-8 JSON line", false},
		{"trailing JSON", valid + `{}`, "fake backend emitted an invalid UTF-8 JSON line", false},
		{"array", `[]`, "fake backend event must be a JSON object", false},
		{"duplicate field", strings.Replace(valid, `"delta":"hello"`, `"delta":"hello","delta":"world"`, 1), "fake backend event contains a duplicate JSON field", false},
		{"nonfinite number", strings.Replace(valid, `"delta":"hello"`, `"delta":NaN`, 1), "fake backend line is not valid JSON", false},
		{"overflowing float", strings.Replace(valid, `"delta":"hello"`, `"delta":1e9999`, 1), "fake backend line is not valid JSON", false},
		{"unpaired surrogate", fakeEvent(id, "agent.message.delta", `{"delta":"\ud800"}`), "fake backend event contains text that is not valid Unicode", false},
		{"foreign run", fakeEvent(uuid.New(), "agent.message.delta", `{"delta":"hello"}`), "fake backend event does not match the executing run", true},
		{"unsupported version", strings.Replace(valid, `"protocol_version":1`, `"protocol_version":1.0`, 1), "fake backend event uses an unsupported protocol version", true},
		{"extra field", strings.Replace(valid, `"protocol_version":1`, `"protocol_version":1,"extra":true`, 1), "fake backend event fields do not match protocol version 1", true},
		{"wrong source", strings.Replace(valid, "fake-container-workload", "other", 1), "fake backend event has an unsupported source", true},
		{"wrong type", fakeEvent(id, "other", `{}`), "fake backend event has an unsupported type", true},
		{"array data", fakeEvent(id, "agent.message.delta", `[]`), "fake backend event data must be an object", true},
		{"delta data", fakeEvent(id, "agent.message.delta", `{"delta":2}`), "fake backend message delta has invalid data", true},
		{"completed data", fakeEvent(id, "agent.message.completed", `{"content":"text","extra":true}`), "fake backend completed message has invalid data", true},
		{"fractional tokens", fakeEvent(id, "usage.updated", `{"input_tokens":1.5,"output_tokens":1}`), "fake backend usage update has invalid data", true},
		{"negative tokens", fakeEvent(id, "usage.updated", `{"input_tokens":1,"output_tokens":-1}`), "fake backend usage update has invalid data", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := &fakeDecoder{runID: id}
			events, err := decoder.Decode([]byte(test.line), runtimes.Stdout, 7)
			var failure *Failure
			if len(events) != 0 || !errors.As(err, &failure) {
				t.Fatalf("events = %#v, error = %v", events, err)
			}
			if failure.Message != test.reason+" at stdout line 7" || (failure.Raw != nil) != test.hasRaw {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestFakeBackendErrors(t *testing.T) {
	id := uuid.New()
	for _, test := range []struct {
		name, line, message string
	}{
		{"invalid input", `{"protocol_version":1,"error":{"code":"invalid_input","message":"details"}}`, "fake backend reported invalid_input"},
		{"injected failure", `{"protocol_version":1,"run_id":"` + id.String() + `","error":{"code":"injected_failure","message":"details"}}`, "fake backend reported injected_failure"},
		{"foreign error", `{"protocol_version":1,"run_id":"` + uuid.NewString() + `","error":{"code":"injected_failure","message":"details"}}`, "fake backend event does not match the executing run at stderr line 2"},
		{"unexpected run", `{"protocol_version":1,"run_id":"` + id.String() + `","error":{"code":"invalid_input","message":"details"}}`, "fake backend invalid-input error fields do not match protocol version 1 at stderr line 2"},
		{"missing run", `{"protocol_version":1,"error":{"code":"injected_failure","message":"details"}}`, "fake backend injected-failure fields do not match protocol version 1 at stderr line 2"},
		{"unknown code", `{"protocol_version":1,"error":{"code":"other","message":"details"}}`, "fake backend error has unsupported data at stderr line 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := &fakeDecoder{runID: id}
			events, err := decoder.Decode([]byte(test.line), runtimes.Stderr, 2)
			var failure *Failure
			if len(events) != 0 || !errors.As(err, &failure) || failure.Message != test.message {
				t.Fatalf("events = %#v, error = %v", events, err)
			}
			if failure.Raw == nil || failure.Raw["protocol_version"] != json.Number("1") || failure.Raw["error"].(map[string]any)["message"] != "details" {
				t.Fatalf("backend error output was lost: %#v", failure.Raw)
			}
		})
	}
}

func fakeEvent(id uuid.UUID, kind, data string) string {
	return `{"protocol_version":1,"run_id":"` + id.String() + `","source":"fake-container-workload","type":"` + kind + `","data":` + data + `}`
}
