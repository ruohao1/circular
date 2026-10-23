package codexworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/completion"
)

func completionHelper(prompt string) int {
	for _, arg := range os.Args {
		match := regexp.MustCompile(`args=\["mcp","([^"]+)"\]`).FindStringSubmatch(arg)
		if len(match) != 2 {
			continue
		}
		if !strings.Contains(arg, `"submit_task_result"`) {
			return 91
		}
		// Exercise the trusted channel while the CLI has a partial JSON line buffered.
		fmt.Fprint(os.Stdout, `{"type":"fixture.`)
		if prompt != "completion-missing" {
			program, err := os.Executable()
			if err != nil {
				return 92
			}
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture-coder", Version: "1"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: exec.Command(program, "mcp", match[1])}, nil)
			if err != nil {
				return 93
			}
			defer client.Close()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "submit_task_result", Arguments: json.RawMessage(`{"result":"Do not reveal synthetic-access-token","evidence":[]}`)})
			if err != nil || result.IsError {
				return 94
			}
		}
		fmt.Fprintln(os.Stdout, `completed"}`)
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`)
		if prompt == "completion-fail" {
			return 23
		}
		return 0
	}
	return 95
}
func TestCompletionWorkloadRetainsReportAcrossProcessFailure(t *testing.T) {
	contract, err := completion.SealContract(completion.Policy{Mode: "direct_completion"}, completion.CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"completion-success", "completion-fail", "completion-missing"} {
		t.Run(prompt, func(t *testing.T) {
			dir := privateAuthDirectory(t)
			saveLogin(t, dir)
			input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": prompt, "completion_contract": contract})
			var out, diagnostic bytes.Buffer
			code := runAt(t.Context(), bytes.NewReader(input), &out, &diagnostic, program(t), dir)
			want := 0
			if prompt == "completion-fail" {
				want = 23
			}
			if code != want {
				t.Fatal(code, diagnostic.String())
			}
			count := strings.Count(out.String(), "circular.task.completion.reported")
			if (count == 1) != (prompt != "completion-missing") || count > 1 {
				t.Fatal("report/outcome conflated", out.String())
			}
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatal("interleaved record", line)
				}
			}
			if strings.Contains(out.String(), "synthetic-access-token") {
				t.Fatal("completion bypassed redaction")
			}
		})
	}
	input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": "review", "purpose": "pr_review", "review_context_sha256": strings.Repeat("a", 64), "completion_contract": contract})
	if _, err := parse(input); err == nil {
		t.Fatal("PR reviewer got coding completion contract")
	}
}
