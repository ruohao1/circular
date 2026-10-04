package backends

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestReviewInvocationHasOnlyReviewInstructionsAndFrozenChoices(t *testing.T) {
	invocation, err := (Codex{}).Prepare(Input{Kind: runstate.PRReview, ReviewContextSHA256: strings.Repeat("a", 64), Instructions: "Inspect boundaries", Config: json.RawMessage(`{"model":"gpt-5.6-terra","reasoning_effort":"high"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(invocation.Stdin, &req); err != nil {
		t.Fatal(err)
	}
	prompt := req["prompt"].(string)
	if req["purpose"] != "pr_review" || req["model"] != "gpt-5.6-terra" || req["reasoning_effort"] != "high" || !strings.Contains(prompt, "submit_pr_review") || !strings.Contains(prompt, "Inspect boundaries") || strings.Contains(prompt, "propose_agent") {
		t.Fatal("wrong review prompt/config", req)
	}
	for _, config := range []string{`{"purpose":"pr_review"}`, `{"review_context_sha256":"a"}`} {
		if _, err := (Codex{}).Prepare(Input{Config: json.RawMessage(config)}); err == nil {
			t.Fatal("public config selected workload purpose")
		}
	}
	if _, err := (Codex{}).Prepare(Input{Kind: runstate.PRReview, Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("review launched without trusted context digest")
	}
}
func TestReviewDecoderRejectsForgedScopeAndPreservesCandidateAfterCompletion(t *testing.T) {
	v, err := (Codex{AuthMode: "api_key", APIKey: testCodexKey}).Prepare(Input{Kind: runstate.PRReview, ReviewContextSHA256: strings.Repeat("a", 64), Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	completed := []byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`)
	if _, err := v.Decoder.Decode(completed, runtimes.Stdout, 1); err != nil {
		t.Fatal(err)
	}
	report := `{"type":"circular.pr_review.submitted","report":{"summary":"Never reveal ` + testCodexKey + `","coverage":"complete","findings":[],"checks":[],"limitations":[]}}`
	events, err := v.Decoder.Decode([]byte(report), runtimes.Stdout, 2)
	if err != nil || len(events) != 1 || events[0].Type != "pr_review.report.submitted" {
		t.Fatal(events, err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), testCodexKey) {
		t.Fatal("review bypassed redactor")
	}
	malformed := strings.Replace(report, `"coverage":"complete"`, `"coverage":"complete","run_id":"forged"`, 1)
	rejected, err := v.Decoder.Decode([]byte(malformed), runtimes.Stdout, 3)
	if err != nil || len(rejected) != 1 || rejected[0].Type != "pr_review.report.rejected" {
		t.Fatal(rejected, err)
	}
	encoded, _ = json.Marshal(rejected)
	if strings.Contains(string(encoded), "forged") {
		t.Fatal("rejection retained unsafe payload")
	}
	if _, err := v.Decoder.Decode([]byte(`{"type":"circular.agent.proposed","proposal":{}}`), runtimes.Stdout, 4); err == nil {
		t.Fatal("review accepted proposal")
	}
	coding, err := (Codex{}).Prepare(Input{Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coding.Decoder.Decode([]byte(report), runtimes.Stdout, 1); err == nil {
		t.Fatal("coding accepted review")
	}
}
