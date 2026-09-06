package fakeworkload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/fakeworkload"
)

const id = "00000000-0000-4000-8000-000000000170"

func input() map[string]any {
	return map[string]any{"protocol_version": 1, "run": map[string]any{"id": id, "task_title": "Add health endpoint", "task_description": "Return a stable response.", "instructions": "Work carefully."}, "behavior": map[string]any{"delay_ms": 0, "failure": "none"}}
}
func execute(t *testing.T, document any, write bool) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return rawExecute(t, string(raw), write)
}
func rawExecute(t *testing.T, raw string, write bool) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	code := fakeworkload.Run(t.Context(), strings.NewReader(raw), &out, &diagnostic, write)
	return code, out.String(), diagnostic.String()
}

const first = `{"data":{"delta":"Fake container workload completed: "},"protocol_version":1,"run_id":"` + id + `","source":"fake-container-workload","type":"agent.message.delta"}` + "\n"
const complete = first + `{"data":{"delta":"Add health endpoint"},"protocol_version":1,"run_id":"` + id + `","source":"fake-container-workload","type":"agent.message.delta"}` + "\n" + `{"data":{"content":"Fake container workload completed: Add health endpoint"},"protocol_version":1,"run_id":"` + id + `","source":"fake-container-workload","type":"agent.message.completed"}` + "\n" + `{"data":{"input_tokens":9,"output_tokens":7},"protocol_version":1,"run_id":"` + id + `","source":"fake-container-workload","type":"usage.updated"}` + "\n"

func TestWorkloadPreservesVersionOneBytesAndBoundedDelay(t *testing.T) {
	code, out, err := execute(t, input(), false)
	if code != 0 || out != complete || err != "" {
		t.Fatalf("%d %s %s", code, out, err)
	}
	doc := input()
	doc["behavior"].(map[string]any)["delay_ms"] = 15
	start := time.Now()
	code, out, err = execute(t, doc, false)
	if code != 0 || out != complete || err != "" || time.Since(start) < 50*time.Millisecond {
		t.Fatal("delayed stream changed")
	}
	doc = input()
	doc["run"].(map[string]any)["task_title"] = "Café 🧪"
	code, out, err = execute(t, doc, false)
	if code != 0 || err != "" || !strings.Contains(out, `Caf\u00e9 \ud83e\uddea`) {
		t.Fatal(code, out, err)
	}
}

func TestFailureModesRetainTheExactPartialStream(t *testing.T) {
	for _, mode := range []string{"before_events", "after_first_event"} {
		t.Run(mode, func(t *testing.T) {
			doc := input()
			doc["behavior"].(map[string]any)["failure"] = mode
			code, out, diagnostic := execute(t, doc, false)
			message := "injected failure before emitting events"
			expected := ""
			if mode == "after_first_event" {
				expected = first
				message = "injected failure after first event"
			}
			stderr := `{"error":{"code":"injected_failure","message":"` + message + `"},"protocol_version":1,"run_id":"` + id + `"}` + "\n"
			if code != 20 || out != expected || diagnostic != stderr {
				t.Fatal(code, out, diagnostic)
			}
		})
	}
}

func TestInvalidRequestsNeverEchoSecretValuesOrAllocate(t *testing.T) {
	for _, test := range []struct {
		name, message string
		change        func(map[string]any)
	}{
		{"protocol", "unsupported protocol_version: expected 1", func(v map[string]any) { v["protocol_version"] = 2 }},
		{"credentials", "input contains unsupported fields: database_url, platform_credentials", func(v map[string]any) { v["database_url"] = "do-not-print"; v["platform_credentials"] = "do-not-print" }},
		{"missing_run", "input is missing required fields: run", func(v map[string]any) { delete(v, "run") }},
		{"run_shape", "run must be an object", func(v map[string]any) { v["run"] = "do-not-print" }},
		{"run_fields", "run is missing required fields: instructions", func(v map[string]any) { delete(v["run"].(map[string]any), "instructions") }},
		{"run_credentials", "run contains unsupported fields: platform_credentials", func(v map[string]any) { v["run"].(map[string]any)["platform_credentials"] = "do-not-print" }},
		{"uuid", "run.id must be a canonical UUID", func(v map[string]any) { v["run"].(map[string]any)["id"] = "../../do-not-print" }},
		{"title", "run.task_title must be a non-empty string", func(v map[string]any) { v["run"].(map[string]any)["task_title"] = " " }},
		{"description", "run.task_description must be a string", func(v map[string]any) {
			v["run"].(map[string]any)["task_description"] = map[string]any{"secret": "do-not-print"}
		}},
		{"instructions", "run.instructions must be a string", func(v map[string]any) { v["run"].(map[string]any)["instructions"] = nil }},
		{"behavior_shape", "behavior must be an object", func(v map[string]any) { v["behavior"] = "do-not-print" }},
		{"behavior_fields", "behavior is missing required fields: failure", func(v map[string]any) { delete(v["behavior"].(map[string]any), "failure") }},
		{"behavior_credentials", "behavior contains unsupported fields: database_url", func(v map[string]any) { v["behavior"].(map[string]any)["database_url"] = "do-not-print" }},
		{"failure", "behavior.failure must be one of: none, before_events, after_first_event", func(v map[string]any) { v["behavior"].(map[string]any)["failure"] = "do-not-print" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			doc := input()
			test.change(doc)
			code, out, diagnostic := execute(t, doc, true)
			expected := `{"error":{"code":"invalid_input","message":"` + test.message + `"},"protocol_version":1}` + "\n"
			if code != 2 || out != "" || diagnostic != expected {
				t.Fatal(code, out, diagnostic)
			}
			files, _ := os.ReadDir(".")
			if len(files) != 0 {
				t.Fatal("invalid input allocated output")
			}
		})
	}
	for _, delay := range []any{true, -1, 10001, 1.5} {
		doc := input()
		doc["behavior"].(map[string]any)["delay_ms"] = delay
		code, out, diagnostic := execute(t, doc, false)
		if code != 2 || out != "" || !strings.Contains(diagnostic, "behavior.delay_ms must be an integer from 0 through 10000") {
			t.Fatal(code, out, diagnostic)
		}
	}
	for _, raw := range []string{`{"protocol_version":1,"secret":"do-not-print"`, "{\"run\":\xff}", `[]`, `{} {}`, `{"a":1,"a":2}`} {
		code, out, err := rawExecute(t, raw, false)
		if code != 2 || out != "" || strings.Contains(err, "do-not-print") {
			t.Fatal(code, out, err)
		}
	}
	for _, name := range []string{"task_title", "task_description", "instructions"} {
		doc := input()
		doc["run"].(map[string]any)[name] = "replace-me"
		raw, _ := json.Marshal(doc)
		code, out, err := rawExecute(t, strings.Replace(string(raw), `"replace-me"`, `"\ud800"`, 1), false)
		if code != 2 || out != "" || !strings.Contains(err, "run."+name+" must be valid Unicode text") {
			t.Fatal(code, out, err)
		}
	}
}

func TestOutputIsExclusiveAndCancellationStopsProgress(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, _ := execute(t, input(), true)
	if code != 0 {
		t.Fatal(code)
	}
	content, err := os.ReadFile("circular-result-" + id + ".txt")
	if err != nil || string(content) != "Fake container workload completed: Add health endpoint\n" {
		t.Fatal(string(content), err)
	}
	code, _, _ = execute(t, input(), true)
	if code == 0 {
		t.Fatal("existing output overwritten")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, _ := json.Marshal(input())
	var out, diagnostic bytes.Buffer
	if fakeworkload.Run(ctx, bytes.NewReader(raw), &out, &diagnostic, false) == 0 {
		t.Fatal("cancelled workload completed")
	}
}
