package agenttools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/delegations"
)

func AddDelegation(server *mcp.Server, spool *delegations.Spool, targets []delegations.Target) {
	if len(targets) == 0 || delegations.ValidateTargets(targets) != nil {
		return
	}
	catalog, _ := json.Marshal(targets)
	no := false
	type arguments struct {
		AgentID    string                          `json:"agent_id"`
		RequestKey string                          `json:"request_key"`
		Objective  delegations.DelegationObjective `json:"objective"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "delegate_task", Title: "Delegate an objective", Description: "Request one handoff to an allowed specialist: " + string(catalog) + ". Supply agent_id, a stable request_key, and objective title/description. After pending_handoff, summarize your progress and finish this invocation normally. Circular will validate the request after exit. No child exists yet; do not wait or claim it is running.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}}, func(ctx context.Context, call *mcp.CallToolRequest, input arguments) (*mcp.CallToolResult, map[string]string, error) {
		request, err := delegations.Decode(call.Params.Arguments)
		if err != nil {
			return nil, nil, err
		}
		if !delegations.Allows(targets, request.AgentID) {
			return nil, nil, errors.New("agent is not an allowed delegation target")
		}
		if _, err = spool.Request(request); err != nil {
			return nil, nil, err
		}
		return nil, map[string]string{"status": "pending_handoff", "message": "Request saved. Summarize progress and finish normally; no child has been created yet."}, nil
	})
}
