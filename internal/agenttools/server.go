// Package agenttools exposes Circular's reviewable agent proposals through MCP.
package agenttools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/codexconfig"
)

type proposalResult struct {
	Proposal agentproposals.Draft `json:"proposal"`
	Status   string               `json:"status"`
	Message  string               `json:"message"`
}

type modelResult struct {
	DefaultModel string              `json:"default_model"`
	Models       []codexconfig.Model `json:"models"`
}

func New(spool *agentproposals.Spool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "circular", Version: "0.1.0"}, nil)
	no := false
	mcp.AddTool(server, &mcp.Tool{Name: "propose_agent", Title: "Propose an agent", Description: "Propose a reusable Codex agent for this Run's project. First call list_models, then choose model and reasoning_effort for this role and explain both in model_reason. Respect user-specified choices. Supply its name, purpose, and complete instructions. Astra is the fallback when no model is selected. Circular prefills your recommendation for user review and editing. This does not create an Agent or start a Run. Do not claim it was created.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}},
		func(ctx context.Context, request *mcp.CallToolRequest, input agentproposals.Input) (*mcp.CallToolResult, proposalResult, error) {
			draft, err := spool.Propose(input)
			if err != nil {
				return nil, proposalResult{}, err
			}
			return nil, proposalResult{Proposal: draft, Status: "pending_review", Message: "Recommendation saved for the user to review after this Run. No Agent has been created."}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "list_models", Title: "List agent models", Description: "List Circular's supported models and reasoning efforts before choosing settings for each proposed role. Select an appropriate effort based on the role's complexity, uncertainty, and expected work; do not automatically choose the highest effort. Default model and effort are fallbacks, not requirements for every role. Account access can differ; this catalog does not provide prices or benchmarks.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}},
		func(ctx context.Context, request *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, modelResult, error) {
			return nil, modelResult{DefaultModel: codexconfig.DefaultModel, Models: codexconfig.Models()}, nil
		})
	return server
}

func Run(ctx context.Context, directory string) error {
	spool, err := agentproposals.NewSpool(directory)
	if err != nil {
		return err
	}
	return New(spool).Run(ctx, &mcp.StdioTransport{})
}
