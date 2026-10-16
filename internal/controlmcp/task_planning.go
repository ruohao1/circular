package controlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskPlanningListInput struct {
	ProjectID        string `json:"project_id"`
	Query            string `json:"q,omitempty"`
	Status           string `json:"status,omitempty"`
	Priority         string `json:"priority,omitempty"`
	RepositoryID     string `json:"repository_id,omitempty"`
	PreferredAgentID string `json:"preferred_agent_id,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
}
type taskPlanningID struct {
	TaskID string `json:"task_id"`
}

func (c *client) addTaskPlanningTools(server *mcp.Server) {
	add(server, "list_task_planning", "Read a Project's Task groups in saved order, with preferred Agent and separate Run summaries. Default five groups, up to 50 Tasks per group. Continue one group using its status and next_cursor. A changed board requires restarting pagination.", true, false, true, func(ctx context.Context, in taskPlanningListInput) (object, error) {
		q := url.Values{}
		for name, value := range map[string]string{"q": in.Query, "status": in.Status, "priority": in.Priority, "repository_id": in.RepositoryID, "preferred_agent_id": in.PreferredAgentID, "cursor": in.Cursor} {
			if value != "" {
				q.Set(name, value)
			}
		}
		if in.Limit != 0 {
			q.Set("limit", strconv.Itoa(in.Limit))
		}
		var result object
		err := c.json(ctx, http.MethodGet, "/projects/"+in.ProjectID+"/task-planning?"+q.Encode(), nil, &result)
		return result, err
	})
	add(server, "get_task_planning", "Read a Task, editing capabilities and current board revision. Run history remains available through list_runs with task_id.", true, false, true, func(ctx context.Context, in taskPlanningID) (object, error) {
		var result object
		if err := c.json(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/planning", nil, &result); err != nil {
			return nil, err
		}
		result["console_url"] = c.webURL + "/tasks?taskId=" + url.QueryEscape(in.TaskID)
		return result, nil
	})
	if c.readOnly {
		return
	}
	revision := object{"type": "integer", "minimum": 1, "maximum": 9007199254740991}
	id := object{"type": "string", "format": "uuid"}
	nullableID := object{"anyOf": []any{id, object{"type": "null"}}}
	update := object{"task_id": id, "expected_revision": revision, "title": object{"type": "string", "minLength": 1, "maxLength": 500}, "description": object{"type": "string", "maxLength": 20000}, "repository_id": nullableID, "preferred_agent_id": nullableID, "priority": object{"type": "string", "enum": []string{"none", "low", "normal", "high", "urgent"}}}
	nullableDate := object{"anyOf": []any{object{"type": "string", "format": "date", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}, object{"type": "null"}}}
	update["planned_start_date"], update["planned_finish_date"] = nullableDate, nullableDate
	update["expected_board_revision"] = revision
	move := object{"task_id": id, "expected_revision": revision, "expected_board_revision": revision, "status": object{"type": "string", "enum": []string{"open", "ready", "in_progress", "in_review", "completed", "cancelled"}}, "placement": object{"type": "string", "enum": []string{"start", "end", "before", "after", "up", "down"}}, "anchor_task_id": id}
	c.addTaskPlanningMutation(server, "update_task", "Update local Task fields using expected_revision. Planned start/finish are optional YYYY-MM-DD calendar dates, years 0001–9999; finish is inclusive. Supplying either date, including null, also requires expected_board_revision from get_task_planning. Omitted fields stay unchanged; null clears a date, Repository or preferred Agent. Partial schedules are Unscheduled. Source-managed content and active Run instructions are protected. Does not execute work. On conflict reload before a new edit.", http.MethodPatch, "", update, []string{"task_id", "expected_revision"})
	c.addTaskPlanningMutation(server, "move_task", "Move a Task using its current revision and board revision. before/after require anchor_task_id; other placements forbid it. up/down use full saved order in the current stage. Stages are manual: Done/cancelled never starts or cancels Runs. On conflict reload before a new intent.", http.MethodPost, "/move", move, []string{"task_id", "expected_revision", "expected_board_revision", "status", "placement"})
}

func (c *client) addTaskPlanningMutation(server *mcp.Server, name, description, method, suffix string, properties object, required []string) {
	no := false
	schema := object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	if name == "update_task" {
		schema["dependentRequired"] = object{"planned_start_date": []string{"expected_board_revision"}, "planned_finish_date": []string{"expected_board_revision"}}
	}
	mcp.AddTool[map[string]json.RawMessage, object](server, &mcp.Tool{Name: name, Description: description, InputSchema: schema, Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, _ *mcp.CallToolRequest, fields map[string]json.RawMessage) (*mcp.CallToolResult, object, error) {
		_, hasStart := fields["planned_start_date"]
		_, hasFinish := fields["planned_finish_date"]
		if hasStart || hasFinish {
			var board int64
			if json.Unmarshal(fields["expected_board_revision"], &board) != nil || board < 1 || board > 9007199254740991 {
				return nil, nil, errors.New("expected_board_revision must be a positive safe integer when a planned date is supplied")
			}
		}
		var taskID string
		if json.Unmarshal(fields["task_id"], &taskID) != nil {
			return nil, nil, errors.New("task_id must be a Circular UUID")
		}
		id, err := uuid.Parse(taskID)
		if err != nil || id == uuid.Nil {
			return nil, nil, errors.New("task_id must be a nonzero Circular UUID")
		}
		delete(fields, "task_id")
		var result object
		if err = c.json(ctx, method, "/tasks/"+id.String()+suffix, fields, &result); err != nil {
			if (hasStart || hasFinish) && (strings.Contains(err.Error(), "json_invalid") || strings.Contains(err.Error(), "Circular HTTP 405:")) {
				return nil, nil, fmt.Errorf("planned dates may be unsupported; upgrade Circular's API and migrations before editing schedules: %w", err)
			}
			return nil, nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		if len(encoded) > 512<<10 {
			return nil, nil, errors.New("result exceeds 512 KiB; inspect the Task in the console")
		}
		return nil, result, nil
	})
}

// Creation on an older server silently ignores unknown fields. Check support
// before sending new planning options, so a saved preference cannot disappear.
func (c *client) requireTaskPlanning(ctx context.Context) error {
	data, err := c.request(ctx, http.MethodGet, c.origin+"/openapi.json", nil)
	if err != nil {
		return err
	}
	var contract struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(data, &contract) != nil || len(contract.Components.Schemas["TaskCreate"].Properties["priority"]) == 0 || len(contract.Components.Schemas["TaskCreate"].Properties["preferred_agent_id"]) == 0 || len(contract.Paths["/api/v1/tasks/{task_id}/planning"]) == 0 || len(contract.Paths["/api/v1/tasks/{task_id}/move"]) == 0 {
		return fmt.Errorf("upgrade Circular's API and migrations before using task planning options")
	}
	return nil
}
