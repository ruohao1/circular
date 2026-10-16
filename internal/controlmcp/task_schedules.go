package controlmcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskTimelineInput struct {
	taskPlanningListInput
	From    string `json:"from"`
	To      string `json:"to"`
	Section string `json:"section,omitempty"`
}

func (c *client) addTaskTimelineTool(server *mcp.Server) {
	add(server, "list_task_timeline", "Read scheduled Tasks overlapping an explicit inclusive from/to calendar window (YYYY-MM-DD, at most 366 days), plus Unscheduled Tasks missing either date. Default 20 per section, maximum 100. Continue a section with section and next_cursor, preserving all filters and dates. A changed board requires restarting both sections. Dates do not execute work.", true, false, true, func(ctx context.Context, in taskTimelineInput) (object, error) {
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 100 {
			return nil, errors.New("limit must be between 1 and 100")
		}
		q := url.Values{"from": {in.From}, "to": {in.To}, "limit": {strconv.Itoa(in.Limit)}}
		for name, value := range map[string]string{"q": in.Query, "status": in.Status, "priority": in.Priority, "repository_id": in.RepositoryID, "preferred_agent_id": in.PreferredAgentID, "section": in.Section, "cursor": in.Cursor} {
			if value != "" {
				q.Set(name, value)
			}
		}
		var result object
		err := c.json(ctx, http.MethodGet, "/projects/"+in.ProjectID+"/task-timeline?"+q.Encode(), nil, &result)
		return result, err
	})
}
