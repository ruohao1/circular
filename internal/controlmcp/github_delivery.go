package controlmcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type githubDeliverySettingsInput struct {
	ProjectID string `json:"project_id" jsonschema:"Circular Project UUID"`
}

func (c *client) addGitHubDeliveryReadTools(server *mcp.Server) {
	add(server, "get_github_publishing", "Read whether this project automatically opens draft pull requests for future successful runs, including permission guidance.", true, false, true,
		func(ctx context.Context, input githubDeliverySettingsInput) (object, error) {
			return c.record(ctx, http.MethodGet, "/projects/"+input.ProjectID+"/integrations/github/run-delivery", "settings", nil)
		})
	add(server, "get_run_pull_request", "Read the run's GitHub draft pull request delivery status and link. pending or retrying is not complete. Inspect the same run after publishing; never start a new coding run to retry delivery.", true, false, true,
		func(ctx context.Context, input runInput) (object, error) {
			return c.record(ctx, http.MethodGet, "/runs/"+input.RunID+"/github-delivery", "delivery", nil)
		})
}

func (c *client) addGitHubDeliveryMutations(server *mcp.Server) {
	add(server, "set_github_publishing", "Enable or disable automatic draft pull requests for future successful runs in this project. Changes external publishing behavior; enable only when the user requested automatic publishing. Existing runs are not backfilled and pull requests are never merged automatically.", false, false, true,
		func(ctx context.Context, input struct {
			ProjectID string `json:"project_id" jsonschema:"Circular Project UUID"`
			Enabled   bool   `json:"enabled" jsonschema:"Whether future successful runs automatically open draft pull requests"`
		}) (object, error) {
			return c.record(ctx, http.MethodPost, "/projects/"+input.ProjectID+"/integrations/github/run-delivery", "settings", object{"enabled": input.Enabled})
		})
	add(server, "publish_run_pull_request", "Publish a successful run's captured changes to a dedicated GitHub branch and open a draft pull request. Use only when the user requested publication. Circular handles delivery; credentials are never passed to a coding agent. This queues background work and returns its status. Retry the same run ID; it reuses the original branch and PR. Does not modify the default branch, merge, or start another agent run.", false, false, true,
		func(ctx context.Context, input runInput) (object, error) {
			return c.record(ctx, http.MethodPost, "/runs/"+input.RunID+"/github-delivery", "delivery", object{})
		})
}
