package codexworkload

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/agenttools"
)

type inspection struct {
	Args        []string          `json:"args"`
	Environment []string          `json:"environment"`
	Prompt      string            `json:"prompt"`
	Directories map[string]uint32 `json:"directories"`
	Files       []string          `json:"files"`
}

// The current Go test binary acts as an external CLI without introducing a
// configurable production command or passing test controls in the environment.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "review-git-container" {
		program, err := os.Executable()
		if err != nil {
			os.Exit(91)
		}
		os.Exit(runAtContext(context.Background(), os.Stdin, os.Stdout, os.Stderr, program, "", "/review-context"))
	}
	if len(os.Args) == 4 && os.Args[1] == "mcp-review" {
		if err := agenttools.RunReview(context.Background(), os.Args[2], os.Args[3]); err != nil {
			os.Exit(91)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		os.Exit(helper())
	}
	if len(os.Args) > 1 && (os.Args[1] == "login" || os.Args[1] == "logout") {
		os.Exit(authHelper())
	}
	os.Exit(m.Run())
}

func helper() int {
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 90
	}
	switch string(prompt) {
	case "review-git-probe":
		return reviewGitProbe()
	case "review-report", "review-fail", "review-missing", "review-wait":
		return reviewHelper(string(prompt))
	case "propose-agent":
		for _, argument := range os.Args {
			match := regexp.MustCompile(`args=\["mcp","([^"]+)"\]`).FindStringSubmatch(argument)
			if len(match) != 2 {
				continue
			}
			spool, err := agentproposals.NewSpool(match[1])
			if err != nil {
				return 95
			}
			_, err = spool.Propose(agentproposals.Input{Name: "Engineer", Purpose: "Test", Instructions: "Never reveal synthetic-access-token", Model: "gpt-5.6-terra", ReasoningEffort: "high", ModelReason: "Keep synthetic-access-token private when explaining the model choice"})
			if err != nil {
				return 96
			}
			fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`)
			return 0
		}
		return 97
	case "refresh-login":
		return refreshHelper(string(prompt))
	case "fail":
		fmt.Fprintln(os.Stdout, `{"type":"turn.failed"}`)
		fmt.Fprintln(os.Stderr, "fixture failure")
		return 23
	case "wait":
		program, err := os.Executable()
		if err != nil {
			return 91
		}
		child := exec.Command(program, "child")
		if child.Start() != nil {
			return 92
		}
		fmt.Fprintf(os.Stdout, "%d\n", child.Process.Pid)
		_ = child.Wait()
		return 0
	default:
		value := inspection{Args: os.Args[1:], Environment: os.Environ(), Prompt: string(prompt), Directories: map[string]uint32{}}
		for _, variable := range []string{"HOME", "CODEX_HOME", "TMPDIR"} {
			info, err := os.Stat(os.Getenv(variable))
			if err != nil {
				return 93
			}
			value.Directories[variable] = uint32(info.Mode().Perm())
		}
		_ = filepath.WalkDir(os.Getenv("HOME"), func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				value.Files = append(value.Files, path)
			}
			return err
		})
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			return 94
		}
		fmt.Fprintln(os.Stderr, "fixture diagnostic")
		return 0
	}
}

func encoded(t *testing.T, prompt, model string) []byte {
	t.Helper()
	value, err := json.Marshal(map[string]any{"protocol_version": 1, "prompt": prompt, "model": model, "api_key": "fixture-api-key"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func program(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIReceivesPromptOnStdinAndOnlyPrivateEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "must-not-inherit")
	t.Setenv("OPENAI_API_KEY", "must-not-inherit")
	t.Setenv("CODEX_API_KEY", "must-not-inherit")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PATH", "must-not-inherit")
	prompt := "Implement a test.\nKeep literal $(text), `text`, and café."
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), bytes.NewReader(encoded(t, prompt, "gpt-fixture-codex")), &stdout, &stderr, program(t))
	if code != 0 || stderr.String() != "fixture diagnostic\n" {
		t.Fatalf("exit %d, diagnostic %q", code, stderr.String())
	}
	var got inspection
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var shellEnvironment, mcpConfig string
	for i, argument := range got.Args {
		if strings.HasPrefix(argument, "mcp_servers.circular=") {
			mcpConfig = argument
		}
		if strings.HasPrefix(argument, "shell_environment_policy.set=") {
			if shellEnvironment != "" || i == 0 || got.Args[i-1] != "-c" {
				t.Fatal("shell environment must be one explicit config override")
			}
			shellEnvironment = argument
		}
	}
	if !regexp.MustCompile(`^mcp_servers\.circular=\{command="/circular-codex-workload",args=\["mcp","/tmp/circular-agent-proposals-[^"/]+"\],required=true,enabled_tools=\["propose_agent","list_models"\]\}$`).MatchString(mcpConfig) {
		t.Fatal("MCP server is not bound to the trusted command and private spool", mcpConfig)
	}
	wantArgs := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "danger-full-access", "-c", `approval_policy="never"`, "-c", `shell_environment_policy.inherit="none"`,
		"-c", shellEnvironment,
		"-c", `cli_auth_credentials_store="file"`, "-c", `forced_login_method="api"`,
		"--color", "never", "-c", mcpConfig, "--model", "gpt-fixture-codex", "-",
	}
	if !reflect.DeepEqual(got.Args, wantArgs) || got.Prompt != prompt {
		t.Fatalf("unexpected CLI request: %+v", got)
	}
	environment := map[string]string{}
	for _, entry := range got.Environment {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if len(environment) != 5 || environment["PATH"] != childPath || environment["CODEX_API_KEY"] != "fixture-api-key" {
		t.Fatalf("unexpected child environment: %#v", environment)
	}
	home := environment["HOME"]
	if !strings.HasPrefix(home, "/tmp/circular-codex-home-") || environment["CODEX_HOME"] != filepath.Join(home, ".codex") || environment["TMPDIR"] != filepath.Join(home, "tmp") {
		t.Fatalf("unexpected private directories: %#v", environment)
	}
	// Parse the generated TOML subset into values rather than accepting an
	// opaque string that could omit the private directories or expose the key.
	table, found := strings.CutPrefix(shellEnvironment, "shell_environment_policy.set={")
	if !found || !strings.HasSuffix(table, "}") {
		t.Fatalf("shell environment is not a TOML inline table: %q", shellEnvironment)
	}
	shellValues := map[string]string{}
	for _, assignment := range strings.Split(strings.TrimSuffix(table, "}"), ",") {
		name, quoted, found := strings.Cut(assignment, "=")
		value, err := strconv.Unquote(quoted)
		if !found || err != nil || shellValues[name] != "" {
			t.Fatalf("invalid shell environment assignment: %q", assignment)
		}
		shellValues[name] = value
	}
	if !reflect.DeepEqual(shellValues, map[string]string{"PATH": childPath, "HOME": home, "TMPDIR": environment["TMPDIR"]}) {
		t.Fatalf("shell environment must contain only private directories and PATH: %#v", shellValues)
	}
	if !reflect.DeepEqual(got.Directories, map[string]uint32{"HOME": 0700, "CODEX_HOME": 0700, "TMPDIR": 0700}) || len(got.Files) != 0 {
		t.Fatalf("private directories contain unexpected state: %+v", got)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("private home survived completion: %v", err)
	}
}

func TestRunPublishesMCPProposalsAfterCLIThroughSubscriptionRedaction(t *testing.T) {
	dir := privateAuthDirectory(t)
	saveLogin(t, dir)
	var stdout, stderr bytes.Buffer
	if code := runAt(t.Context(), bytes.NewReader(subscriptionInput(t, "propose-agent")), &stdout, &stderr, program(t), dir); code != 0 {
		t.Fatal("wrapper failed", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "synthetic-access-token") {
		t.Fatal("MCP proposal bypassed redaction")
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"type":"turn.completed"`) {
		t.Fatal("CLI output lost", stdout.String())
	}
	var published struct {
		Type     string               `json:"type"`
		Proposal agentproposals.Draft `json:"proposal"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &published); err != nil || published.Type != "circular.agent.proposed" || published.Proposal.Instructions != "Never reveal [REDACTED]" || published.Proposal.Model != "gpt-5.6-terra" || published.Proposal.ReasoningEffort != "high" || published.Proposal.ModelReason != "Keep [REDACTED] private when explaining the model choice" {
		t.Fatal("missing redacted draft", err, published)
	}
}

func TestOptionalModelAndNonzeroExit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := `{"protocol_version":1,"prompt":"inspect","api_key":"fixture-api-key"}`
	if code := run(t.Context(), strings.NewReader(input), &stdout, &stderr, program(t)); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var got inspection
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args[len(got.Args)-5:], []string{"--model", "gpt-6-astra", "-c", `model_reasoning_effort="low"`, "-"}) {
		t.Fatal("unset model must pin Astra with low reasoning", got.Args)
	}
	stdout.Reset()
	stderr.Reset()
	code := run(t.Context(), bytes.NewReader(encoded(t, "fail", "")), &stdout, &stderr, program(t))
	if code != 23 || stdout.String() != "{\"type\":\"turn.failed\"}\n" || stderr.String() != "fixture failure\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestSelectedReasoningEffortReachesCLI(t *testing.T) {
	input := `{"protocol_version":1,"prompt":"inspect","api_key":"fixture-api-key","model":"gpt-6-astra","reasoning_effort":"ultra"}`
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), strings.NewReader(input), &stdout, &stderr, program(t)); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var got inspection
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args[len(got.Args)-5:], []string{"--model", "gpt-6-astra", "-c", `model_reasoning_effort="ultra"`, "-"}) {
		t.Fatal("selected variant did not reach CLI", got.Args)
	}
}

func TestMalformedRequestsFailBeforeStartingAndNeverEchoInput(t *testing.T) {
	valid := string(encoded(t, "private-prompt", ""))
	for name, input := range map[string]string{
		"unknown":            strings.Replace(valid, "{", `{"private-secret":"do-not-echo",`, 1),
		"duplicate":          strings.Replace(valid, "{", `{"api_key":"do-not-echo",`, 1),
		"version":            strings.Replace(valid, `"protocol_version":1`, `"protocol_version":2`, 1),
		"fractional_version": strings.Replace(valid, `"protocol_version":1`, `"protocol_version":1.0`, 1),
		"prompt_null":        strings.Replace(valid, `"private-prompt"`, `null`, 1),
		"prompt_empty":       strings.Replace(valid, `"private-prompt"`, `"  \n"`, 1),
		"key_empty":          strings.Replace(valid, `"fixture-api-key"`, `""`, 1),
		"key_nul":            strings.Replace(valid, `"fixture-api-key"`, `"private\u0000key"`, 1),
		"model_null":         strings.Replace(valid, `"model":""`, `"model":null`, 1),
		"model_flag":         strings.Replace(valid, `"model":""`, `"model":"--private-secret"`, 1),
		"model_space":        strings.Replace(valid, `"model":""`, `"model":"private secret"`, 1),
		"model_long":         strings.Replace(valid, `"model":""`, `"model":"`+strings.Repeat("x", 201)+`"`, 1),
		"model_control":      strings.Replace(valid, `"model":""`, `"model":"private\nsecret"`, 1),
		"effort_injection":   strings.Replace(valid, "{", `{"reasoning_effort":"high\nprivate-secret",`, 1),
		"effort_null":        strings.Replace(valid, "{", `{"reasoning_effort":null,`, 1),
		"effort_number":      strings.Replace(valid, "{", `{"reasoning_effort":3,`, 1),
		"effort_duplicate":   strings.Replace(valid, "{", `{"reasoning_effort":"low","reasoning_effort":"high",`, 1),
		"effort_unsupported": strings.Replace(valid, `"model":""`, `"model":"gpt-5.6-luna","reasoning_effort":"ultra"`, 1),
		"trailing":           valid + ` {"private-secret":true}`,
		"truncated":          valid[:len(valid)-1],
		"utf8":               "{\"prompt\":\"\xff\"}",
		"missing":            `{}`,
		"array":              `[]`,
		"oversized":          strings.Repeat(" ", maxInput+1),
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), strings.NewReader(input), &stdout, &stderr, "/missing-private-secret")
			if code != 2 || stdout.Len() != 0 || stderr.String() != "invalid Codex workload request\n" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestStartupErrorsNeverEchoPrivateValues(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), bytes.NewReader(encoded(t, "private-prompt", "")), &stdout, &stderr, "/missing-private-secret")
	if code != 1 || stdout.Len() != 0 || stderr.String() != "cannot start Codex workload\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestCancellationStopsCLIAndDescendantsAfterStreamingBegins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var stderr bytes.Buffer
	result := make(chan int, 1)
	input, binary := encoded(t, "wait", ""), program(t)
	go func() { result <- run(ctx, bytes.NewReader(input), writer, &stderr, binary) }()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(reader).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	var childPID int
	select {
	case line := <-ready:
		var err error
		childPID, err = strconv.Atoi(line)
		if err != nil || childPID <= 0 {
			t.Fatal("CLI did not stream its child process identity")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not stream output while running")
	}
	cancel()
	select {
	case code := <-result:
		if code != 128+int(syscall.SIGKILL) || stderr.Len() != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop CLI")
	}
	deadline := time.Now().Add(time.Second)
	for processAlive(childPID) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
			t.Fatal("CLI descendant survived cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return false
	}
	if err == nil {
		_, state, found := strings.Cut(string(data), ") ")
		if found && strings.HasPrefix(state, "Z ") {
			return false
		}
	}
	return syscall.Kill(pid, 0) == nil
}

func TestAlreadyCancelledContextDoesNotStartCLI(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, bytes.NewReader(encoded(t, "private-prompt", "")), &stdout, &stderr, "/missing-private-secret"); code != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}
