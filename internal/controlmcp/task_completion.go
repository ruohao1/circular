package controlmcp

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/url"
	"strconv"
)

type completionInput struct {
	TaskID string `json:"task_id" jsonschema:"Circular Task UUID"`
}

func (c *client) addCompletionReadTools(server *mcp.Server) {
	add(server, "get_task_completion", "Inspect the frozen policy, accepted report, current PR, independent review, checks, observation age and completion blockers. Does not contact GitHub.", true, false, true, func(ctx context.Context, in completionInput) (object, error) {
		return c.record(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/completion", "completion", nil)
	})
	add(server, "prepare_task_pr_review", "Verify the current Task completion PR and selected independent reviewer. Save input_fingerprint for launch. Starts no model work.", true, false, true, func(ctx context.Context, in struct {
		TaskID     string `json:"task_id"`
		ReviewerID string `json:"reviewer_id,omitempty"`
	}) (object, error) {
		q := url.Values{}
		if in.ReviewerID != "" {
			q.Set("reviewer_id", in.ReviewerID)
		}
		return c.record(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/pr-reviews/prepare?"+q.Encode(), "preparation", nil)
	})
	add(server, "list_task_pr_reviews", "List independent review attempts for a Task, including external coding work. Continue with next_cursor.", true, false, true, func(ctx context.Context, in struct {
		TaskID string `json:"task_id"`
		Limit  int    `json:"limit,omitempty"`
		Cursor string `json:"cursor,omitempty"`
	}) (object, error) {
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 100 || len(in.Cursor) > 1024 {
			return nil, errors.New("limit must be 1–100 and cursor at most 1024 bytes")
		}
		q := url.Values{"limit": {strconv.Itoa(in.Limit)}, "cursor": {in.Cursor}}
		return c.record(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/pr-reviews?"+q.Encode(), "page", nil)
	})
}
func (c *client) addCompletionMutations(server *mcp.Server) {
	add(server, "refresh_task_completion", "Refresh authoritative GitHub evidence for the current completion attempt. Never changes the Task stage or merges its PR.", false, false, true, func(ctx context.Context, in completionInput) (object, error) {
		return c.record(ctx, http.MethodPost, "/tasks/"+in.TaskID+"/completion/refresh", "completion", object{})
	})
	add(server, "bind_task_pull_request", "Bind a PR number in this Task's Repository to its current released completion session. Requires current Task revision; clears earlier PR readiness. Does not accept URLs, hashes or agent claims as verification.", false, false, true, func(ctx context.Context, in struct {
		TaskID           string `json:"task_id"`
		SessionID        string `json:"session_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		Number           int    `json:"number"`
	}) (object, error) {
		if in.Number < 1 || int64(in.Number) > 2147483647 || in.ExpectedRevision < 1 || in.ExpectedRevision > 9007199254740991 {
			return nil, errors.New("positive PR number and current Task revision are required")
		}
		return c.record(ctx, http.MethodPut, "/tasks/"+in.TaskID+"/pull-request", "completion", object{"session_id": in.SessionID, "expected_revision": in.ExpectedRevision, "number": in.Number})
	})
	add(server, "launch_task_pr_review", "Start independent model review of this Task's current PR. Call prepare_task_pr_review first. Reuse the same request key and parameters on retry; mode=again requires previous_review_id. Review feedback may be published through the existing provider pipeline.", false, false, true, func(ctx context.Context, in struct {
		TaskID string `json:"task_id"`
		reviewLaunchOptions
	}) (object, error) {
		body, err := reviewLaunchBody(in.reviewLaunchOptions)
		if err != nil {
			return nil, err
		}
		return c.record(ctx, http.MethodPost, "/tasks/"+in.TaskID+"/pr-reviews", "review", body)
	})
}
