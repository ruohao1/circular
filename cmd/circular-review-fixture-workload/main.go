// circular-review-fixture-workload is a deterministic browser-test workload.
// It is built only by the disposable e2e harness, never a production image.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ruohao1/circular/internal/prreviews"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "review fixture failed:", err)
		os.Exit(1)
	}
}
func run() error {
	var input struct {
		Purpose string `json:"purpose"`
		Prompt  string `json:"prompt"`
		Model   string `json:"model"`
		Effort  string `json:"reasoning_effort"`
		Digest  string `json:"review_context_sha256"`
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16<<20)).Decode(&input); err != nil {
		return err
	}

	if input.Purpose != "pr_review" {
		if input.Model == "" || input.Prompt == "" {
			return fmt.Errorf("missing coding input")
		}
		event := map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "id": "fixture-start", "text": "Fixture workload started."}}
		_ = json.NewEncoder(os.Stdout).Encode(event)
		if strings.Contains(input.Prompt, "hold-for-stop") {
			time.Sleep(90 * time.Second)
		} else {
			time.Sleep(4 * time.Second)
		}
		proof, _ := json.Marshal(map[string]string{"model": input.Model, "reasoning_effort": input.Effort, "prompt": input.Prompt})
		if e := os.WriteFile("/workspace/input-proof.json", proof, 0644); e != nil {
			return e
		}
		if e := os.WriteFile("/workspace/src/validate.ts", []byte("export function valid(value: number) {\n  return value > 0;\n}\n"), 0644); e != nil {
			return e
		}
		event["item"] = map[string]any{"type": "agent_message", "id": "fixture-result", "text": "Fixture completed using " + input.Model + " / " + input.Effort + ". Captured approved instructions in input-proof.json."}
		_ = json.NewEncoder(os.Stdout).Encode(event)
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":10}}`)
		return nil
	}
	context, err := prreviews.ReadContext("/review-context")
	if err != nil {
		return err
	}
	digest, err := prreviews.Fingerprint(context)
	if err != nil || input.Purpose != "pr_review" || input.Digest != digest {
		return fmt.Errorf("invalid fixture context")
	}
	source, err := os.ReadFile("/workspace/src/validate.ts")
	if err != nil {
		return err
	}
	if string(source) != "export function valid(value: number) {\n  return value > 0;\n}\n" {
		return fmt.Errorf("unexpected fixture source")
	}
	report := prreviews.Report{Summary: "The changed validation rejects zero, contrary to the task.", Coverage: "complete", Findings: []prreviews.Finding{{Severity: "high", Title: "Zero is rejected", Path: "src/validate.ts", Side: "head", StartLine: 2, EndLine: 2, Evidence: "The changed line returns value > 0.", Consequence: "Valid zero inputs are rejected.", SuggestedFix: "Accept value >= 0 and add a regression test."}}, Checks: []prreviews.Check{{Method: "Read the captured diff and source.", Outcome: "passed", Evidence: "The changed condition excludes zero."}}, Limitations: []string{}}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("/tmp", "review-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	spool, err := prreviews.NewSpool(directory, context)
	if err != nil {
		return err
	}
	if _, err := spool.Submit(raw); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":10}}`)
	return spool.Publish(os.Stdout)
}
