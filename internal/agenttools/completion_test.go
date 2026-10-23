package agenttools

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/completion"
)

func TestCompletionToolCorrectableAndScoped(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	proposals, err := agentproposals.NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := completion.SealContract(completion.Policy{Mode: "direct_completion", Requirements: []completion.Requirement{{Key: "tests", Description: "Show tests"}}}, completion.CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := completion.NewSpool(dir, contract)
	if err != nil {
		t.Fatal(err)
	}
	server := New(proposals)
	AddCompletion(server, spool)
	s, c := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, raw := range []string{
		`{"result":"Done","evidence":[]}`,
		`{"result":"Done","result":"other","evidence":[]}`,
		`{"result":"Done","evidence":[{"key":"tests","detail":"Passed"}],"task_id":"forged"}`,
		`{"result":"Done","evidence":[{"key":"tests","detail":"Passed"}],"policy":{"mode":"direct_completion"}}`,
	} {
		out, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "submit_task_result", Arguments: json.RawMessage(raw)})
		if err == nil && !out.IsError {
			t.Fatal("bad submission", raw)
		}
	}
	for range 2 {
		out, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "submit_task_result", Arguments: map[string]any{"result": "Done", "evidence": []map[string]any{{"key": "tests", "detail": "Passed"}}}})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
	}
	if report, err := spool.Read(); err != nil || report.Result != "Done" {
		t.Fatal(report, err)
	}
}
