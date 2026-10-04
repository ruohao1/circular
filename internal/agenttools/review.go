package agenttools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/prreviews"
)

func NewReview(spool *prreviews.Spool) *mcp.Server {
	return newReview(spool, nil)
}
func newReview(spool *prreviews.Spool, forward func(context.Context, string) error) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "circular-pr-review", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "submit_pr_review", Description: "Save this run's structured PR assessment. An identical replay is safe; a different second submission is rejected. This does not publish feedback or launch work."}, func(ctx context.Context, request *mcp.CallToolRequest, report prreviews.Report) (*mcp.CallToolResult, map[string]string, error) {
		// Preserve raw arguments so duplicate and unknown fields cannot disappear in
		// typed decoding before the report validator sees them.
		checksum, err := spool.Submit(request.Params.Arguments)
		if err != nil {
			return nil, nil, err
		}
		if forward != nil {
			if err := forward(ctx, checksum); err != nil {
				return nil, nil, err
			}
		}
		return nil, map[string]string{"status": "submitted", "sha256": checksum}, nil
	})
	return server
}
func RunReview(ctx context.Context, directory, contextDirectory string) error {
	value, err := prreviews.ReadContext(contextDirectory)
	if err != nil {
		return err
	}
	spool, err := prreviews.NewSpool(directory, value)
	if err != nil {
		return err
	}
	return newReview(spool, func(ctx context.Context, checksum string) error {
		return prreviews.ForwardReport(ctx, directory, checksum)
	}).Run(ctx, &mcp.StdioTransport{})
}
