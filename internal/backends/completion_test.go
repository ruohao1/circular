package backends

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/fakeworkload"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestCompletionFakeExplicitReport(t *testing.T) {
	contract, _ := completion.SealContract(completion.Policy{Mode: "direct_completion", Requirements: []completion.Requirement{{Key: "tests", Description: "Show tests"}}}, completion.CheckConfig{})
	for _, configured := range []bool{false, true} {
		config := json.RawMessage(`{}`)
		if configured {
			config = json.RawMessage(`{"completion_report":{"result":"Fixture verified","evidence":[{"key":"tests","detail":"Fixture check passed"}]}}`)
		}
		invocation, err := (Fake{}).Prepare(Input{Config: config, TaskTitle: "Fixture", CompletionContract: &contract})
		if err != nil {
			t.Fatal(err)
		}
		var out, diagnostic bytes.Buffer
		if code := fakeworkload.Run(t.Context(), bytes.NewReader(invocation.Stdin), &out, &diagnostic, false); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		reports := 0
		for i, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			events, err := invocation.Decoder.Decode([]byte(line), runtimes.Stdout, i+1)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == "task.completion.reported" {
					reports++
				}
			}
		}
		if (reports == 1) != configured {
			t.Fatal("fixture report must be explicit", reports, configured)
		}
	}
}

func TestCompletionTrustedInputAndOutput(t *testing.T) {
	contract, err := completion.SealContract(completion.Policy{Mode: "direct_completion", Requirements: []completion.Requirement{{Key: "tests", Description: "Show tests"}}}, completion.CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Config: json.RawMessage(`{}`), CompletionContract: &contract}
	invocation, err := (Codex{}).Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(invocation.Stdin), "submit_task_result") || !strings.Contains(string(invocation.Stdin), contract.Fingerprint) {
		t.Fatal("missing frozen reporting contract")
	}
	events, err := invocation.Decoder.Decode([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"Done"}}`), runtimes.Stdout, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "task.completion.reported" {
			t.Fatal("plain text manufactured report")
		}
	}
	record := []byte(`{"type":"circular.task.completion.reported","report":{"result":"Done","evidence":[{"key":"tests","detail":"Passed"}]}}`)
	events, err = invocation.Decoder.Decode(record, runtimes.Stdout, 2)
	if err != nil || len(events) != 1 || events[0].Type != "task.completion.reported" {
		t.Fatal(events, err)
	}
	if replay, err := invocation.Decoder.Decode(record, runtimes.Stdout, 3); err != nil || len(replay) != 0 {
		t.Fatal("duplicate event", replay, err)
	}
	changed := strings.Replace(string(record), `"Done"`, `"Different"`, 1)
	if _, err = invocation.Decoder.Decode([]byte(changed), runtimes.Stdout, 4); err == nil {
		t.Fatal("conflicting report")
	}
	for _, unconfigured := range []Input{{Config: json.RawMessage(`{}`)}, {Config: json.RawMessage(`{}`), Kind: runstate.PRReview, ReviewContextSHA256: strings.Repeat("a", 64)}} {
		call, err := (Codex{}).Prepare(unconfigured)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = call.Decoder.Decode(record, runtimes.Stdout, 1); err == nil {
			t.Fatal("unconfigured workload reported completion")
		}
	}
	input.Kind = runstate.PRReview
	input.ReviewContextSHA256 = strings.Repeat("a", 64)
	if _, err = (Codex{}).Prepare(input); err == nil {
		t.Fatal("review workload received coding tools")
	}
}
