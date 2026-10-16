package backends

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestDelegationDecoderAfterCompletion(t *testing.T) {
	id := uuid.New()
	input := Input{Config: json.RawMessage(`{}`), DelegationTargets: []delegations.Target{{AgentID: id, Name: "Reviewer"}}}
	invocation, err := (Codex{AuthMode: "api_key", APIKey: "fixture-secret"}).Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(invocation.Stdin), "delegate_task") {
		t.Fatal("missing handoff instructions")
	}
	if _, err := invocation.Decoder.Decode([]byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`), runtimes.Stdout, 1); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "circular.delegation.requested", "request": delegations.Request{AgentID: id, RequestKey: "review", Objective: delegations.DelegationObjective{Title: "Review", Description: "Check fixture-secret"}}})
	events, err := invocation.Decoder.Decode(raw, runtimes.Stdout, 2)
	if err != nil || len(events) != 1 || events[0].Type != "delegation.requested" {
		t.Fatalf("events %v: %v", events, err)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "fixture-secret") {
		t.Fatal("credential persisted")
	}
	if replay, err := invocation.Decoder.Decode(raw, runtimes.Stdout, 3); err != nil || len(replay) != 0 {
		t.Fatalf("replay %v %v", replay, err)
	}
	for _, input := range []Input{{Config: json.RawMessage(`{}`)}, {Kind: runstate.PRReview, Config: json.RawMessage(`{}`), ReviewContextSHA256: strings.Repeat("a", 64)}} {
		call, err := (Codex{}).Prepare(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = call.Decoder.Decode(raw, runtimes.Stdout, 1); err == nil {
			t.Fatal("unconfigured invocation accepted delegation")
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), id.String(), uuid.NewString(), 1),
		strings.Replace(string(raw), `"title":"Review"`, `"title":"Review","parent_run_id":"forged"`, 1),
		strings.Replace(string(raw), `"request_key":"review"`, `"request_key":"second"`, 1),
	} {
		if _, err = invocation.Decoder.Decode([]byte(bad), runtimes.Stdout, 4); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
