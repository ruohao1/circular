package agenttools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/prreviews"
)

func AddCompletion(server *mcp.Server, spool *completion.Spool) { addCompletion(server, spool, nil) }
func addCompletion(server *mcp.Server, spool *completion.Spool, forward func(context.Context, string) error) {
	if spool == nil {
		return
	}
	no := false
	mcp.AddTool(server, &mcp.Tool{Name: "submit_task_result", Title: "Submit task result", Description: "Submit this Task's final result and evidence for every frozen requirement. Include a PR number only for this Task's Repository. An identical replay is safe; a different report is rejected. This records evidence; successful execution and safe cleanup are also required. A handoff report cannot finish the parent Task.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}}, func(ctx context.Context, call *mcp.CallToolRequest, report completion.Report) (*mcp.CallToolResult, map[string]string, error) {
		if err := prreviews.DecodeStrict(call.Params.Arguments, &report); err != nil {
			return nil, nil, err
		}
		if err := spool.Submit(report); err != nil {
			return nil, nil, err
		}
		checksum, err := prreviews.Fingerprint(report)
		if err != nil {
			return nil, nil, err
		}
		if forward != nil {
			if err = forward(ctx, checksum); err != nil {
				return nil, nil, err
			}
		}
		return nil, map[string]string{"status": "submitted", "message": "Report recorded; Circular will evaluate the final successful invocation after cleanup."}, nil
	})
}
