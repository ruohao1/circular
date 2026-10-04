package codexworkload

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/prreviews"
)

func reviewHelper(prompt string) int {
	for _, arg := range os.Args {
		match := regexp.MustCompile(`args=\["mcp-review","([^"]+)","([^"]+)"\]`).FindStringSubmatch(arg)
		if len(match) != 3 {
			continue
		}
		if !strings.Contains(arg, `enabled_tools=["submit_pr_review"]`) || strings.Contains(arg, "propose_agent") || strings.Contains(arg, "list_models") {
			return 92
		}
		if prompt != "review-missing" {
			program, err := os.Executable()
			if err != nil {
				return 93
			}
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture-codex", Version: "1"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: exec.Command(program, "mcp-review", match[1], match[2])}, nil)
			if err != nil {
				return 94
			}
			defer client.Close()
			result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "submit_pr_review", Arguments: json.RawMessage(`{"summary":"Do not reveal synthetic-access-token","coverage":"complete","findings":[],"checks":[],"limitations":[]}`)})
			if err != nil || result.IsError {
				return 95
			}
		}
		if prompt == "review-wait" {
			fmt.Fprintln(os.Stdout, `{"type":"fixture.review.acknowledged"}`)
			for {
				time.Sleep(time.Hour)
			}
		}
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`)
		if prompt == "review-fail" {
			return 23
		}
		return 0
	}
	return 96
}
func reviewContextFixture(t *testing.T) (string, string) {
	t.Helper()
	runID := uuid.New()
	diff := []byte("review diff\n")
	sum := sha256.Sum256(diff)
	snapshot, err := prreviews.SealSnapshot(prreviews.LaunchSnapshot{SourceRunID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), RepositoryID: uuid.New(), Reviewer: prreviews.ReviewerSnapshot{AgentID: uuid.New(), Backend: "codex", BackendConfig: json.RawMessage(`{}`)}, PR: prreviews.PRIdentity{BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	value := prreviews.Context{RunID: runID, ReviewID: uuid.New(), Snapshot: snapshot, MergeBaseSHA: snapshot.PR.BaseSHA, DiffSHA256: hex.EncodeToString(sum[:]), Files: []prreviews.ChangedFile{}, Evidence: []prreviews.Evidence{}, Limitations: []string{}}
	path := filepath.Join(t.TempDir(), runID.String())
	if err := prreviews.WriteContext(path, value, diff); err != nil {
		t.Fatal(err)
	}
	digest, _ := prreviews.Fingerprint(value)
	return path, digest
}
func TestReviewWorkloadPrivateToolContextBindingAndFailedExit(t *testing.T) {
	path, digest := reviewContextFixture(t)
	auth := privateAuthDirectory(t)
	saveLogin(t, auth)
	for _, prompt := range []string{"review-report", "review-fail", "review-missing"} {
		input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": prompt, "purpose": "pr_review", "review_context_sha256": digest, "auth_mode": "chatgpt"})
		var stdout, stderr bytes.Buffer
		code := runAtContext(t.Context(), bytes.NewReader(input), &stdout, &stderr, program(t), auth, path)
		want := 0
		if prompt == "review-fail" {
			want = 23
		}
		if code != want {
			t.Fatalf("%s: code %d, %s", prompt, code, stderr.String())
		}
		reportCount := bytes.Count(stdout.Bytes(), []byte("circular.pr_review.submitted"))
		expected := 1
		if prompt == "review-missing" {
			expected = 0
		}
		if reportCount != expected || strings.Contains(stdout.String(), "synthetic-access-token") {
			t.Fatal("wrong or unredacted output", stdout.String())
		}
	}
	input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": "review-report", "purpose": "pr_review", "review_context_sha256": strings.Repeat("f", 64), "auth_mode": "chatgpt"})
	var out, diagnostics bytes.Buffer
	if code := runAtContext(t.Context(), bytes.NewReader(input), &out, &diagnostics, "/must-not-start", auth, path); code == 0 || out.Len() != 0 {
		t.Fatal("context mismatch executed")
	}
	for _, extra := range []string{`"purpose":"unknown"`, `"purpose":"pr_review"`, `"purpose":"coding","review_context_sha256":"bad"`, `"purpose":"pr_review","review_context_sha256":"` + digest + `","context_path":"/other"`} {
		if _, err := parse([]byte(`{"protocol_version":1,"prompt":"test",` + extra + `}`)); err == nil {
			t.Fatal("invalid purpose accepted", extra)
		}
	}
}

func TestReviewSubmissionReachesWorkerBeforeAcknowledgementAndCancellation(t *testing.T) {
	path, digest := reviewContextFixture(t)
	auth := privateAuthDirectory(t)
	saveLogin(t, auth)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": "review-wait", "purpose": "pr_review", "review_context_sha256": digest, "auth_mode": "chatgpt"})
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan int, 1)
	go func() {
		code := runAtContext(ctx, bytes.NewReader(input), writer, io.Discard, program(t), auth, path)
		writer.Close()
		done <- code
	}()
	reports, acknowledged := 0, false
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "synthetic-access-token") {
			t.Error("report secret escaped redaction")
		}
		if strings.Contains(line, "circular.pr_review.submitted") {
			reports++
		}
		if strings.Contains(line, "fixture.review.acknowledged") {
			acknowledged = true
			if reports != 1 {
				t.Error("acknowledged report is still only in the temporary container spool")
			}
			cancel()
		}
	}
	if code := <-done; code == 0 || !acknowledged || reports != 1 {
		t.Fatalf("cancelled review lost partial report: code=%d ack=%v reports=%d", code, acknowledged, reports)
	}
}
