package execution

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestCompletionOutputRetainedBeforeFailure(t *testing.T) {
	contract, err := completion.SealContract(completion.Policy{Mode: "direct_completion"}, completion.CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := (backends.Codex{}).Prepare(backends.Input{Config: json.RawMessage(`{}`), CompletionContract: &contract})
	if err != nil {
		t.Fatal(err)
	}
	output := protocolTestOutput(runtimes.Output{Stream: runtimes.Stdout, Data: []byte(`{"type":"circular.task.completion.reported","report":{"result":"Done","evidence":[]}}` + "\n" + `{"type":"turn.failed","error":{"message":"later process failure"}}` + "\n")})
	reports := 0
	err, drained := ingestOutput(uuid.New(), preparedBackend{name: "codex", invocation: invocation}, output, func(event backends.Event) error {
		if event.Type == "task.completion.reported" {
			reports++
		}
		return nil
	})
	if err == nil || !drained || reports != 1 {
		t.Fatal("report altered failed process outcome", reports, drained, err)
	}
}
