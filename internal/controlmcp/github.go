package controlmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type githubAccountsInput struct {
	ProjectID string `json:"project_id" jsonschema:"Circular Project UUID from list_projects"`
	Page      int    `json:"page,omitempty" jsonschema:"GitHub page number, defaults to 1; use next_page to continue"`
}

type githubCreateInput struct {
	ProjectID      string `json:"project_id" jsonschema:"Circular Project UUID"`
	InstallationID string `json:"installation_id" jsonschema:"GitHub installation ID from list_github_accounts; this is a decimal string, not a Circular UUID"`
	Name           string `json:"name" jsonschema:"New repository name, maximum 100 characters; no owner prefix"`
	Description    string `json:"description,omitempty" jsonschema:"Optional repository description, maximum 350 characters"`
	Visibility     string `json:"visibility,omitempty" jsonschema:"private (default) or public; use public only when the user explicitly chose it"`
	RequestKey     string `json:"request_key" jsonschema:"Choose one nonzero UUID per creation intent and reuse it unchanged with identical parameters after any error or reconnect"`
}

func (c *client) addGitHubReadTools(server *mcp.Server) {
	add(server, "list_github_accounts", "List this project's connected GitHub accounts and repository creation permissions. Choose an installation with can_create=true. If permissions are missing, follow permission_message in the console; reconnect alone does not approve new app permissions.", true, false, true,
		func(ctx context.Context, input githubAccountsInput) (object, error) {
			if input.Page == 0 {
				input.Page = 1
			}
			if input.Page < 1 || input.Page > 10000 {
				return nil, errors.New("page must be between 1 and 10000")
			}
			var result object
			err := c.json(ctx, http.MethodGet, fmt.Sprintf("/projects/%s/integrations/github/installations?page=%d", input.ProjectID, input.Page), nil, &result)
			return result, err
		})
}

func (c *client) addGitHubMutations(server *mcp.Server) {
	add(server, "create_github_repository", "Create a real GitHub repository with an initial README and attach it to the selected Circular project. Use only when the user has requested repository creation. Call list_github_accounts first. Visibility defaults to private. Reuse the same request_key and parameters for every retry. attached means ready; needs_access or uncertain requires following the returned message. Never generate a new key to retry an uncertain outcome. Does not start a coding run.", false, false, true,
		func(ctx context.Context, input githubCreateInput) (object, error) {
			if err := textField("name", input.Name, 100); err != nil {
				return nil, err
			}
			if !utf8.ValidString(input.Description) || utf8.RuneCountInString(input.Description) > 350 {
				return nil, errors.New("description must be valid text of at most 350 characters")
			}
			if input.Visibility == "" {
				input.Visibility = "private"
			}
			if input.Visibility != "private" && input.Visibility != "public" {
				return nil, errors.New("visibility must be private or public")
			}
			key, err := uuid.Parse(input.RequestKey)
			if err != nil || key == uuid.Nil {
				return nil, errors.New("request_key must be a nonzero UUID reused for this creation intent")
			}
			body := object{"installation_id": input.InstallationID, "name": input.Name, "description": input.Description, "visibility": input.Visibility, "request_key": key.String()}
			return c.record(ctx, http.MethodPost, "/projects/"+input.ProjectID+"/integrations/github/repositories", "creation", body)
		})
}
