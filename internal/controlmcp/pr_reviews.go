package controlmcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/prreviews"
)

type reviewInput struct {
	ReviewID string `json:"review_id" jsonschema:"Circular PR review UUID"`
}
type reviewPrepareInput struct {
	RunID      string `json:"run_id" jsonschema:"Source coding run UUID with a delivered PR"`
	ReviewerID string `json:"reviewer_id,omitempty" jsonschema:"Enabled PR reviewer UUID, defaults to project selection"`
}
type reviewLaunchInput struct {
	RunID                    string `json:"run_id" jsonschema:"Source coding run UUID"`
	ReviewerID               string `json:"reviewer_id,omitempty"`
	RequestKey               string `json:"request_key" jsonschema:"Stable nonzero UUID; reuse for all retries of this launch"`
	ExpectedInputFingerprint string `json:"expected_input_fingerprint" jsonschema:"Fingerprint from prepare_pr_review"`
	Mode                     string `json:"mode" jsonschema:"normal reuses matching work; again explicitly starts a new attempt"`
	PreviousReviewID         string `json:"previous_review_id,omitempty" jsonschema:"Required only for again"`
}

func (c *client) addPRReviewReadTools(server *mcp.Server) {
	add(server, "get_pr_review_settings", "Read the selected reviewer and default-off automatic review setting. Does not start model work.", true, false, true, func(ctx context.Context, in githubDeliverySettingsInput) (object, error) {
		return c.record(ctx, http.MethodGet, "/projects/"+in.ProjectID+"/integrations/github/pr-reviews", "settings", nil)
	})
	add(server, "prepare_pr_review", "Read the verified PR commits and selected reviewer's model/effort. Save its input_fingerprint for launch. Does not start model work.", true, false, true, func(ctx context.Context, in reviewPrepareInput) (object, error) {
		query := url.Values{}
		if in.ReviewerID != "" {
			query.Set("reviewer_id", in.ReviewerID)
		}
		return c.record(ctx, http.MethodGet, "/runs/"+in.RunID+"/pr-reviews/prepare?"+query.Encode(), "preparation", nil)
	})
	add(server, "list_pr_reviews", "List this source coding run's PR review attempts. Continue using next_cursor; starts no work.", true, false, true, func(ctx context.Context, in struct {
		RunID  string `json:"run_id"`
		Limit  int    `json:"limit,omitempty"`
		Cursor string `json:"cursor,omitempty"`
	}) (object, error) {
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 100 || len(in.Cursor) > 1024 {
			return nil, errors.New("limit must be 1–100 and cursor at most 1024 bytes")
		}
		query := url.Values{"limit": {strconv.Itoa(in.Limit)}, "cursor": {in.Cursor}}
		return c.record(ctx, http.MethodGet, "/runs/"+in.RunID+"/pr-reviews?"+query.Encode(), "page", nil)
	})
	add(server, "get_pr_review", "Read the structured report, exact commits, execution, assessment, freshness and independent publication states.", true, false, true, func(ctx context.Context, in reviewInput) (object, error) {
		return c.record(ctx, http.MethodGet, "/pr-reviews/"+in.ReviewID, "review", nil)
	})
	add(server, "refresh_pr_review", "Check current PR identity through the shared one-minute throttle. Reads GitHub only; never starts a model or publishes feedback.", true, false, true, func(ctx context.Context, in reviewInput) (object, error) {
		return c.record(ctx, http.MethodPost, "/pr-reviews/"+in.ReviewID+"/refresh", "review", object{})
	})
}
func (c *client) addPRReviewMutations(server *mcp.Server) {
	add(server, "set_pr_review_settings", "Choose the reviewer and enable or disable automatic reviews of future delivered PRs. Default off; no backfill. Enabling authorizes additional model work and GitHub feedback. Disabling cancels pending automatic launches and queued automatic reviews.", false, false, true, func(ctx context.Context, in struct {
		ProjectID  string `json:"project_id"`
		Automatic  bool   `json:"automatic"`
		ReviewerID string `json:"reviewer_id,omitempty"`
	}) (object, error) {
		body := object{"automatic": in.Automatic}
		if in.ReviewerID != "" {
			body["reviewer_id"] = in.ReviewerID
		}
		return c.record(ctx, http.MethodPost, "/projects/"+in.ProjectID+"/integrations/github/pr-reviews", "settings", body)
	})
	add(server, "launch_pr_review", "Start additional model work to review a delivered PR and publish eligible feedback. Call prepare_pr_review first. Reuse the same request_key and parameters for retries. For an explicit new attempt use a new UUID key, mode=again and previous_review_id. The reviewer must differ from the source coder.", false, false, true, func(ctx context.Context, in reviewLaunchInput) (object, error) {
		key, err := uuid.Parse(in.RequestKey)
		if err != nil {
			return nil, errors.New("request_key must be a nonzero UUID")
		}
		var previous uuid.UUID
		if in.PreviousReviewID != "" {
			previous, _ = uuid.Parse(in.PreviousReviewID)
		}
		value := prreviews.LaunchRequest{RequestKey: key, ExpectedInputFingerprint: in.ExpectedInputFingerprint, Mode: in.Mode, PreviousReviewID: previous}
		if err = value.Validate(); err != nil {
			return nil, err
		}
		body := object{"request_key": in.RequestKey, "expected_input_fingerprint": in.ExpectedInputFingerprint, "mode": in.Mode}
		if in.ReviewerID != "" {
			body["reviewer_id"] = in.ReviewerID
		}
		if in.PreviousReviewID != "" {
			body["previous_review_id"] = in.PreviousReviewID
		}
		return c.record(ctx, http.MethodPost, "/runs/"+in.RunID+"/pr-reviews", "review", body)
	})
	add(server, "retry_pr_review_publication", "Retry or reconcile GitHub feedback for this same review. Does not start a model run. An uncertain write is checked for an existing receipt and never blindly posted again.", false, false, true, func(ctx context.Context, in reviewInput) (object, error) {
		return c.record(ctx, http.MethodPost, "/pr-reviews/"+in.ReviewID+"/publication/retry", "review", object{})
	})
}
