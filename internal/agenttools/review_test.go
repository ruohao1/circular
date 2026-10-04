package agenttools

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/prreviews"
)

func TestReviewToolsExposeOnlyStructuredAssessment(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := prreviews.NewSpool(dir, prreviews.Context{ReviewID: uuid.New(), RunID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	s, c := mcp.NewInMemoryTransports()
	server, err := NewReview(spool).Connect(t.Context(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "review-test", Version: "1"}, nil).Connect(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	list, err := client.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "submit_pr_review" {
		t.Fatal(list, err)
	}
	for _, name := range []string{"propose_agent", "list_models", "create_agent", "create_run"} {
		r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !r.IsError {
			t.Fatal("unexpected capability", name)
		}
	}
	args := map[string]any{"summary": "Read the changes", "coverage": "complete", "findings": []any{}, "checks": []any{}, "limitations": []any{}}
	for range 2 {
		r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "submit_pr_review", Arguments: args})
		if err != nil || r.IsError {
			t.Fatal(r, err)
		}
	}
	args["run_id"] = uuid.NewString()
	r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "submit_pr_review", Arguments: args})
	if err == nil && !r.IsError {
		t.Fatal("forged identity accepted")
	}
}
