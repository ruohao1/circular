package agenttools

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/agentproposals"
)

func TestMCPToolsProduceReviewableDraftsAndValidateArguments(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := agentproposals.NewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	s, c := mcp.NewInMemoryTransports()
	server, err := New(spool).Connect(t.Context(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatal("missing MCP tools", err)
	}
	models, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_models", Arguments: map[string]any{}})
	if err != nil || models.IsError {
		t.Fatal("models tool failed", err, models)
	}
	var catalog modelResult
	encoded, _ := json.Marshal(models.StructuredContent)
	if err := json.Unmarshal(encoded, &catalog); err != nil || catalog.DefaultModel != "gpt-6-astra" || len(catalog.Models) == 0 {
		t.Fatal("missing model defaults", err)
	}
	args := map[string]any{"name": "Product engineer", "purpose": "Build console features", "instructions": "Read the repo conventions. Add focused tests."}
	var first string
	for range 2 {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "propose_agent", Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("proposal tool failed", err, result)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		var got proposalResult
		if err := json.Unmarshal(encoded, &got); err != nil || got.Status != "pending_review" || got.Proposal.Model != "gpt-6-astra" || got.Proposal.ReasoningEffort != "low" {
			t.Fatal("invalid review result", err, string(encoded))
		}
		if first != "" && first != got.Proposal.ID {
			t.Fatal("retry duplicated suggestion")
		}
		first = got.Proposal.ID
	}
	args["name"], args["model"], args["reasoning_effort"] = "Execution engineer", "gpt-5.6-terra", "high"
	args["model_reason"] = "Use Terra with high reasoning for changes spanning worker ownership and recovery."
	var selectedID string
	for range 2 {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "propose_agent", Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("selected model rejected", err, result)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		var got proposalResult
		if err := json.Unmarshal(encoded, &got); err != nil || got.Proposal.Model != "gpt-5.6-terra" || got.Proposal.ReasoningEffort != "high" || got.Proposal.ModelReason != "Use Terra with high reasoning for changes spanning worker ownership and recovery." {
			t.Fatal("orchestrator choices changed", err, string(encoded))
		}
		if selectedID != "" && selectedID != got.Proposal.ID {
			t.Fatal("rephrasing a reason duplicated the proposal")
		}
		selectedID = got.Proposal.ID
		args["model_reason"] = "Rephrased explanation for the same choice."
	}
	for _, bad := range []map[string]any{
		{"name": "Missing fields"},
		{"name": "Invalid variant", "purpose": "Test", "instructions": "Test", "model": "gpt-5.6-luna", "reasoning_effort": "ultra"},
		{"name": "Scope injection", "purpose": "Test", "instructions": "Test", "project_id": "another-project"},
		{"name": "Oversized reason", "purpose": "Test", "instructions": "Test", "model_reason": strings.Repeat("x", 1001)},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "propose_agent", Arguments: bad})
		if err == nil && !result.IsError {
			t.Fatal("invalid tool input accepted", bad)
		}
	}
	var output bytes.Buffer
	if err := spool.Publish(&output); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte("\n")) != 2 || !bytes.Contains(output.Bytes(), []byte(`"type":"circular.agent.proposed"`)) {
		t.Fatal("MCP retry created duplicate durable drafts", output.String())
	}
}
