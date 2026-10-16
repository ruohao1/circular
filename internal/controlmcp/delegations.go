package controlmcp

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
)

type delegationAgentInput struct {
	AgentID string `json:"agent_id"`
}

func (c *client) addDelegationRead(server *mcp.Server) {
	add(server, "get_delegation_targets", "Read the owner's allowed specialist Agents for this Agent. An empty set disables delegation.", true, false, true, func(ctx context.Context, in delegationAgentInput) (object, error) {
		var out object
		err := c.json(ctx, http.MethodGet, "/agents/"+in.AgentID+"/delegation-targets", nil, &out)
		return out, err
	})
}

func (c *client) addDelegationWrite(server *mcp.Server) {
	add(server, "set_delegation_targets", "Replace this Agent's allowed specialist Agents. Targets must be enabled and in the same Project. [] disables future admissions; cancel the parent Run to stop admitted work.", false, false, true, func(ctx context.Context, in struct {
		delegationAgentInput
		TargetIDs []string `json:"target_ids"`
	}) (object, error) {
		var out object
		err := c.json(ctx, http.MethodPut, "/agents/"+in.AgentID+"/delegation-targets", object{"target_ids": in.TargetIDs}, &out)
		return out, err
	})
}
