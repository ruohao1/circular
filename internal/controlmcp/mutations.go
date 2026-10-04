package controlmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/codexconfig"
)

func textField(name, value string, max int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s must contain 1–%d characters of valid text", name, max)
	}
	return nil
}

type projectCreate struct {
	Name        string `json:"name" jsonschema:"Project name, maximum 200 characters"`
	Description string `json:"description,omitempty"`
}

type repositoryCreate struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	CloneURL      string `json:"clone_url" jsonschema:"Git clone URL or a local repository path accessible to Circular's worker. Never include credentials"`
	DefaultBranch string `json:"default_branch,omitempty" jsonschema:"Default branch, defaults to main"`
}

type agentCreate struct {
	ProjectID       string `json:"project_id"`
	Name            string `json:"name"`
	Instructions    string `json:"instructions" jsonschema:"Reusable instructions for the role, maximum 20000 characters"`
	Backend         string `json:"backend,omitempty" jsonschema:"codex (default) or fake for simulated execution"`
	Model           string `json:"model,omitempty" jsonschema:"Codex model ID from list_models; omitted choices use Astra"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"An effort supported by the chosen model; omitted choices use that model's default"`
}

type agentModel struct {
	AgentID         string `json:"agent_id"`
	Model           string `json:"model" jsonschema:"Codex model ID from list_models"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type taskCreate struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id,omitempty" jsonschema:"Repository from the same project; required for executing coding work"`
	Title        string `json:"title" jsonschema:"Task objective, maximum 500 characters"`
	Description  string `json:"description,omitempty" jsonschema:"Detailed requirements and acceptance criteria"`
}

type discoveryCreate struct {
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
}

type runCreate struct {
	TaskID     string `json:"task_id"`
	AgentID    string `json:"agent_id" jsonschema:"An enabled Agent from the Task's project"`
	RequestKey string `json:"request_key" jsonschema:"Unique key for this launch intent, 1–200 characters, such as task-id:first-pass. Reuse unchanged on retries/reconnects. A new key deliberately creates another attempt"`
}

type proposalCreate struct {
	RunID string `json:"run_id"`
	agentproposals.Input
}

type proposalAccept struct {
	RunID      string `json:"run_id"`
	ProposalID string `json:"proposal_id"`
	agentproposals.Input
}

type proposalDismiss struct {
	RunID      string `json:"run_id"`
	ProposalID string `json:"proposal_id"`
}

func (c *client) addMutations(server *mcp.Server) {
	add(server, "create_project", "Create a Circular project. Includes the default Repository discovery Agent. Does not create a Task or start execution. This is not idempotent; inspect list_projects before retrying an uncertain response.", false, false, false,
		func(ctx context.Context, input projectCreate) (object, error) {
			if err := textField("name", input.Name, 200); err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPost, "/projects", "project", input)
		})
	add(server, "add_repository", "Register a Git repository in a project. For private GitHub repositories use the console's GitHub import so it retains the installation identity. This does not clone or execute anything. Names must be unique within the project.", false, false, false,
		func(ctx context.Context, input repositoryCreate) (object, error) {
			if err := textField("name", input.Name, 200); err != nil {
				return nil, err
			}
			if err := textField("clone_url", input.CloneURL, 4000); err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPost, "/repositories", "repository", input)
		})
	add(server, "create_agent", "Create a reusable enabled Agent with a chosen model and reasoning effort. Call list_models first and respect user preferences. Codex with Astra is the fallback; fake is available for simulation. Does not start a Run. Names must be unique in the project. Inspect list_agents before retrying an uncertain response.", false, false, false,
		func(ctx context.Context, input agentCreate) (object, error) {
			if err := textField("name", input.Name, 200); err != nil {
				return nil, err
			}
			if err := textField("instructions", input.Instructions, 20000); err != nil {
				return nil, err
			}
			if input.Backend == "" {
				input.Backend = "codex"
			}
			body := object{"project_id": input.ProjectID, "name": input.Name, "instructions": input.Instructions, "backend": input.Backend}
			switch input.Backend {
			case "codex":
				settings, err := codexconfig.Resolve(input.Model, input.ReasoningEffort)
				if err != nil {
					return nil, err
				}
				body["backend_config"] = settings
			case "fake":
				if input.Model != "" || input.ReasoningEffort != "" {
					return nil, errors.New("fake Agents do not use a model or reasoning effort")
				}
			default:
				return nil, errors.New("backend must be codex or fake")
			}
			return c.record(ctx, http.MethodPost, "/agents", "agent", body)
		})
	add(server, "update_agent_model", "Change a Codex Agent's model and reasoning effort while preserving its name and instructions. This changes the shared Agent settings used when work is dispatched. Call list_models first; an omitted effort uses the chosen model's default.", false, true, true,
		func(ctx context.Context, input agentModel) (object, error) {
			if input.Model == "" {
				return nil, errors.New("model is required")
			}
			settings, err := codexconfig.Resolve(input.Model, input.ReasoningEffort)
			if err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPatch, "/agents/"+input.AgentID, "agent", object{"backend_config": settings})
		})
	add(server, "create_task", "Prepare a Task with an objective, requirements and optional repository. The repository must belong to the project. Does not start execution; use start_run separately. This is not idempotent; inspect list_tasks before retrying an uncertain response.", false, false, false,
		func(ctx context.Context, input taskCreate) (object, error) {
			if err := textField("title", input.Title, 500); err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPost, "/tasks", "task", input)
		})
	add(server, "prepare_discovery", "Prepare a repository exploration Task using the project's default discovery Agent. Returns its agent_id from the Task's Circular references. Does not start execution; choose settings and use start_run separately. This is not idempotent; inspect list_tasks before retrying an uncertain response.", false, false, false,
		func(ctx context.Context, input discoveryCreate) (object, error) {
			result, err := c.record(ctx, http.MethodPost, "/projects/"+input.ProjectID+"/discovery", "task", object{"repository_id": input.RepositoryID})
			if err != nil {
				return nil, err
			}
			if task, ok := result["task"].(object); ok {
				if refs, ok := task["external_refs"].(object); ok {
					if circular, ok := refs["circular"].(object); ok {
						result["agent_id"] = circular["agent_id"]
					}
				}
			}
			return result, nil
		})
	add(server, "start_run", "Queue a Task for an enabled Agent in the same project. This starts actual execution and can consume the configured model subscription/API quota. Supply a stable request_key for this launch intent; retries with the same Task, Agent and key return the original Run, even after it finishes. A different key starts another attempt. Inspect get_run for progress; this tool does not wait for completion.", false, false, true,
		func(ctx context.Context, input runCreate) (object, error) {
			input.RequestKey = strings.TrimSpace(input.RequestKey)
			if err := textField("request_key", input.RequestKey, 200); err != nil {
				return nil, err
			}
			if err := c.requireKeyedLaunch(ctx); err != nil {
				return nil, err
			}
			result, err := c.record(ctx, http.MethodPost, "/runs", "run", input)
			if err == nil {
				if run, ok := result["run"].(object); ok {
					result["console_url"] = c.webURL + "/runs/" + fmt.Sprint(run["id"])
				}
			}
			return result, err
		})
	add(server, "cancel_run", "Cancel a queued or active Run. Circular's worker stops execution and cleans up its resources. Repeating cancellation of an already-cancelled Run is safe; succeeded/failed Runs return a conflict. Inspect get_run for retained output and cleanup state.", false, true, true,
		func(ctx context.Context, input runInput) (object, error) {
			return c.record(ctx, http.MethodPost, "/runs/"+input.RunID+"/cancel", "run", object{})
		})
	add(server, "propose_agent", "Save an Agent recommendation for review in a Run's project. Include model, reasoning_effort and model_reason after calling list_models. Identical configurations are deduplicated. Does not create an Agent or start execution. Read proposals with get_run.", false, false, true,
		func(ctx context.Context, input proposalCreate) (object, error) {
			normalized, err := agentproposals.Normalize(input.Input)
			if err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPost, "/runs/"+input.RunID+"/agent-proposals", "proposal", normalized)
		})
	add(server, "create_proposed_agent", "Accept a pending Agent proposal from get_run, creating the reusable Agent with the reviewed name, instructions, model and reasoning effort supplied here. This is an explicit creation action, not a preview. Retries return the same created Agent. Does not start a Run.", false, false, true,
		func(ctx context.Context, input proposalAccept) (object, error) {
			normalized, err := agentproposals.Normalize(input.Input)
			if err != nil {
				return nil, err
			}
			return c.record(ctx, http.MethodPost, "/runs/"+input.RunID+"/agent-proposals/"+input.ProposalID+"/create", "agent", normalized)
		})
	add(server, "dismiss_agent_proposal", "Dismiss a pending Agent proposal. Repeated dismissal is safe. A proposal that already created an Agent cannot be dismissed.", false, true, true,
		func(ctx context.Context, input proposalDismiss) (object, error) {
			return c.record(ctx, http.MethodPost, "/runs/"+input.RunID+"/agent-proposals/"+input.ProposalID+"/dismiss", "proposal", object{})
		})
}
