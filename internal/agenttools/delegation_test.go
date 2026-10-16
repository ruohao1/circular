package agenttools

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/delegations"
)

func TestDelegationToolPendingHandoff(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	proposals, err := agentproposals.NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	delegationDir := t.TempDir()
	_ = os.Chmod(delegationDir, 0700)
	spool, err := delegations.NewSpool(delegationDir)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	server := New(proposals)
	AddDelegation(server, spool, []delegations.Target{{AgentID: id, Name: "Reviewer"}})
	s, c := mcp.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	args := map[string]any{"agent_id": id.String(), "request_key": "review", "objective": map[string]any{"title": "Review", "description": "Find mistakes."}}
	for range 2 {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "delegate_task", Arguments: args})
		if err != nil || result.IsError {
			raw, _ := json.Marshal(result)
			t.Fatal(err, string(raw))
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if got["status"] != "pending_handoff" || got["child_run_id"] != nil {
			t.Fatal("tool claimed admission", string(raw))
		}
	}
	args["parent_run_id"] = uuid.NewString()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "delegate_task", Arguments: args})
	if err == nil && !result.IsError {
		t.Fatal("forged parent accepted")
	}
	delete(args, "parent_run_id")
	args["agent_id"] = uuid.NewString()
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "delegate_task", Arguments: args})
	if err == nil && !result.IsError {
		t.Fatal("unlisted target accepted")
	}
}
