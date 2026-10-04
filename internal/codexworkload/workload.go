// Package codexworkload runs Codex exec and manages Circular's dedicated login.
package codexworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ruohao1/circular/internal/agentproposals"
	"github.com/ruohao1/circular/internal/codexauth"
	"github.com/ruohao1/circular/internal/codexconfig"
	"github.com/ruohao1/circular/internal/prreviews"
)

const (
	maxInput      = 16 * 1024 * 1024
	executable    = "/usr/local/bin/codex"
	childPath     = "/usr/local/bin:/usr/bin:/bin"
	authDirectory = "/codex-auth"
)

type request struct {
	prompt           string
	model            string
	effort           string
	apiKey           string
	authMode         string
	purpose          string
	contextSHA256    string
	reviewContext    *prreviews.Context
	contextDirectory string
}

// Run receives a versioned request on stdin. Subscription output is redacted
// before forwarding and its stderr is discarded; API-key output is forwarded
// for the worker's decoder. Neither credentials nor prompts are command arguments.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	return run(ctx, stdin, stdout, stderr, executable)
}

// The executable and login-directory seams are package-private; production
// callers cannot select a command or use a host user's Codex home.
func run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, program string) int {
	return runAt(ctx, stdin, stdout, stderr, program, authDirectory)
}

func runAt(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, program, authDir string) int {
	return runAtContext(ctx, stdin, stdout, stderr, program, authDir, "/review-context")
}

// Only fixture tests can choose a context directory. Production always uses the
// trusted runtime's fixed /review-context mount.
func runAtContext(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, program, authDir, contextDirectory string) int {
	if ctx.Err() != nil {
		return 1
	}
	input, err := io.ReadAll(io.LimitReader(stdin, maxInput+1))
	if err != nil || len(input) > maxInput {
		return diagnostic(stderr, "invalid Codex workload request", 2)
	}
	value, err := parse(input)
	if err != nil {
		return diagnostic(stderr, "invalid Codex workload request", 2)
	}
	if ctx.Err() != nil {
		return 1
	}
	if value.purpose == "pr_review" {
		review, err := prreviews.ReadContext(contextDirectory)
		digest, digestErr := prreviews.Fingerprint(review)
		if err != nil || digestErr != nil || digest != value.contextSHA256 {
			return diagnostic(stderr, "PR review input could not be verified", 2)
		}
		value.reviewContext = &review
		value.contextDirectory = contextDirectory
	}
	if value.authMode == "api_key" {
		return executeRun(ctx, value, stdout, stderr, program, "")
	}
	code := 1
	err = codexauth.WithLock(ctx, authDir, func() error {
		if err := codexauth.ValidateChatGPT(authDir); err != nil {
			return err
		}
		code = executeRun(ctx, value, stdout, stderr, program, authDir)
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			publicAuthError(stdout)
		}
		return 1
	}
	return code
}

func privateEnvironment(authDir string) (environment []string, shellEnvironment string, cleanup func(), err error) {
	home, err := os.MkdirTemp("/tmp", "circular-codex-home-")
	if err != nil {
		return nil, "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(home) }
	temporary, codexHome := filepath.Join(home, "tmp"), authDir
	if err := os.Mkdir(temporary, 0700); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
		if err := os.Mkdir(codexHome, 0700); err != nil {
			cleanup()
			return nil, "", nil, err
		}
	}
	environment = []string{"PATH=" + childPath, "HOME=" + home, "CODEX_HOME=" + codexHome, "TMPDIR=" + temporary}
	// Child shells receive the writable private directories but neither the
	// persistent credential location nor any authentication environment value.
	shellEnvironment = "shell_environment_policy.set={PATH=" + strconv.Quote(childPath) +
		",HOME=" + strconv.Quote(home) + ",TMPDIR=" + strconv.Quote(temporary) + "}"
	return environment, shellEnvironment, cleanup, nil
}

func executeRun(ctx context.Context, value request, stdout, stderr io.Writer, program, authDir string) int {
	environment, shellEnvironment, cleanup, err := privateEnvironment(authDir)
	if err != nil {
		return diagnostic(stderr, "cannot prepare Codex workload", 1)
	}
	defer cleanup()
	proposalDir, err := os.MkdirTemp("/tmp", "circular-agent-proposals-")
	if err != nil {
		return diagnostic(stderr, "cannot prepare Circular agent tools", 1)
	}
	defer os.RemoveAll(proposalDir)
	var publish func(io.Writer) error
	var reviewSpool *prreviews.Spool
	mcpConfig := `mcp_servers.circular={command="/circular-codex-workload",args=["mcp",` + strconv.Quote(proposalDir) + `],required=true,enabled_tools=["propose_agent","list_models"]}`
	if value.purpose == "pr_review" {
		if value.reviewContext == nil {
			return diagnostic(stderr, "PR review input could not be verified", 2)
		}
		// The host worktree's .git pointer is deliberately inaccessible. Use the
		// self-contained, read-only metadata in this Run's trusted context, both
		// for Codex itself and for its explicitly scoped shell environment.
		for _, setting := range [][2]string{
			{"GIT_DIR", filepath.Join(value.contextDirectory, "git")},
			{"GIT_WORK_TREE", "/workspace"},
			{"GIT_OPTIONAL_LOCKS", "0"},
			{"GIT_NO_REPLACE_OBJECTS", "1"},
		} {
			environment = append(environment, setting[0]+"="+setting[1])
			shellEnvironment = strings.TrimSuffix(shellEnvironment, "}") + "," + setting[0] + "=" + strconv.Quote(setting[1]) + "}"
		}
		spool, err := prreviews.NewSpool(proposalDir, *value.reviewContext)
		if err != nil {
			return diagnostic(stderr, "cannot prepare Circular review tools", 1)
		}
		publish = spool.Publish
		reviewSpool = spool
		mcpConfig = `mcp_servers.circular={command="/circular-codex-workload",args=["mcp-review",` + strconv.Quote(proposalDir) + `,` + strconv.Quote(value.contextDirectory) + `],required=true,enabled_tools=["submit_pr_review"]}`
	} else {
		proposals, err := agentproposals.NewSpool(proposalDir)
		if err != nil {
			return diagnostic(stderr, "cannot prepare Circular agent tools", 1)
		}
		publish = proposals.Publish
	}

	method := "chatgpt"
	if value.authMode == "api_key" {
		method = "api"
		environment = append(environment, "CODEX_API_KEY="+value.apiKey)
	}
	args := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"--ignore-user-config", "--ignore-rules", "--sandbox", "danger-full-access",
		"-c", `approval_policy="never"`, "-c", `shell_environment_policy.inherit="none"`,
		"-c", shellEnvironment, "-c", `cli_auth_credentials_store="file"`,
		"-c", "forced_login_method=" + strconv.Quote(method), "--color", "never",
		"-c", mcpConfig,
	}
	if value.model != "" {
		args = append(args, "--model", value.model)
	}
	if value.effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(value.effort))
	}
	args = append(args, "-")
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var redactor *subscriptionOutput
	if authDir != "" {
		secrets, err := codexauth.Secrets(authDir)
		if err != nil {
			publicAuthError(stdout)
			return 1
		}
		redactor = &subscriptionOutput{writer: stdout, authDir: authDir, secrets: secrets, cancel: cancel, context: child}
		stdout, stderr = redactor, io.Discard
	}
	var forwarder *prreviews.ReportForwarder
	var reviewStream *reviewOutput
	if reviewSpool != nil {
		reviewStream = &reviewOutput{writer: stdout}
		forwarder, err = prreviews.StartReportForwarder(proposalDir, reviewSpool, recordWriter{reviewStream})
		if err != nil {
			return diagnostic(stderr, "cannot prepare Circular review output", 1)
		}
		defer forwarder.Finish()
		stdout = reviewStream
	}
	code := executeCommand(child, program, args, environment, strings.NewReader(value.prompt), stdout, stderr)
	if forwarder != nil {
		if err := forwarder.Finish(); err != nil {
			return diagnostic(stderr, "cannot publish Circular review output", 1)
		}
		if err := reviewStream.finish(); err != nil {
			return diagnostic(stderr, "incomplete Codex output", 1)
		}
	}
	if redactor != nil {
		if err := redactor.finish(); err != nil {
			return 1
		}
	}
	if child.Err() == nil && forwarder == nil {
		if err := publish(stdout); err != nil {
			return diagnostic(stderr, "cannot publish Circular agent output", 1)
		}
		if redactor != nil {
			if err := redactor.finish(); err != nil {
				return 1
			}
		}
	}
	return code
}

// Authenticate manages only the dedicated persisted ChatGPT login. Login is
// device authorization; status and logout never select API-key authentication.
func Authenticate(ctx context.Context, action string, stdin io.Reader, stdout, stderr io.Writer) int {
	return authenticate(ctx, action, stdin, stdout, stderr, executable, authDirectory)
}

func authenticate(ctx context.Context, action string, stdin io.Reader, stdout, stderr io.Writer, program, authDir string) int {
	var args []string
	switch action {
	case "login":
		args = []string{"login", "--device-auth"}
	case "status":
		args = []string{"login", "status"}
	case "logout":
		args = []string{"logout"}
	default:
		return diagnostic(stderr, "unsupported Codex authentication command", 2)
	}
	code := 1
	err := codexauth.WithLock(ctx, authDir, func() error {
		if action == "status" {
			if err := codexauth.ValidateChatGPT(authDir); err != nil {
				return err
			}
		}
		environment, _, cleanup, err := privateEnvironment(authDir)
		if err != nil {
			return err
		}
		defer cleanup()
		args = append(args, "-c", `cli_auth_credentials_store="file"`, "-c", `forced_login_method="chatgpt"`)
		code = executeCommand(ctx, program, args, environment, stdin, stdout, stderr)
		if code == 0 && action == "login" {
			return codexauth.ValidateChatGPT(authDir)
		}
		return nil
	})
	if err != nil {
		return diagnostic(stderr, "Codex ChatGPT login is unavailable; run the Circular Codex login command", 1)
	}
	return code
}

func executeCommand(ctx context.Context, program string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer) int {
	command := exec.CommandContext(ctx, program, args...)
	command.Env = environment
	command.Stdin = stdin
	command.Stdout, command.Stderr = stdout, stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killGroup(command) }
	command.WaitDelay = time.Second
	if command.Start() != nil {
		return diagnostic(stderr, "cannot start Codex workload", 1)
	}
	defer killGroup(command)
	err := command.Wait()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	return diagnostic(stderr, "Codex workload execution failed", 1)
}

func publicError(stdout io.Writer, message string) {
	_ = json.NewEncoder(stdout).Encode(map[string]any{"type": "error", "code": "circular_codex_output_invalid", "message": message})
}

func publicAuthError(stdout io.Writer) {
	_ = json.NewEncoder(stdout).Encode(map[string]any{"type": "error", "code": "circular_chatgpt_auth_required", "message": "Codex ChatGPT login is unavailable; run the Circular Codex login command"})
}

func killGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func diagnostic(stderr io.Writer, message string, code int) int {
	_, _ = io.WriteString(stderr, message+"\n")
	return code
}

func parse(input []byte) (request, error) {
	invalid := errors.New("invalid request")
	if !utf8.Valid(input) {
		return request{}, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return request{}, invalid
	}
	fields := make(map[string]json.RawMessage, 4)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return request{}, invalid
		}
		if name != "protocol_version" && name != "prompt" && name != "model" && name != "reasoning_effort" && name != "api_key" && name != "auth_mode" && name != "purpose" && name != "review_context_sha256" {
			return request{}, invalid
		}
		if _, duplicate := fields[name]; duplicate {
			return request{}, invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return request{}, invalid
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return request{}, invalid
	}
	if decoder.Decode(new(any)) != io.EOF || string(bytes.TrimSpace(fields["protocol_version"])) != "1" {
		return request{}, invalid
	}
	var value request
	for _, field := range []struct {
		name   string
		target *string
	}{{"prompt", &value.prompt}, {"api_key", &value.apiKey}, {"model", &value.model}, {"reasoning_effort", &value.effort}, {"auth_mode", &value.authMode}, {"purpose", &value.purpose}, {"review_context_sha256", &value.contextSHA256}} {
		raw, found := fields[field.name]
		if !found && field.name != "prompt" {
			continue
		}
		var text *string
		if json.Unmarshal(raw, &text) != nil || text == nil {
			return request{}, invalid
		}
		*field.target = *text
	}
	if _, explicit := fields["auth_mode"]; !explicit {
		value.authMode = "chatgpt"
		// Preserve version 1 API-key-only callers without allowing explicit
		// ChatGPT mode to fall back to a key.
		if _, supplied := fields["api_key"]; supplied {
			value.authMode = "api_key"
		}
	}
	if value.authMode != "chatgpt" && value.authMode != "api_key" ||
		value.authMode == "chatgpt" && value.apiKey != "" ||
		value.authMode == "api_key" && strings.TrimSpace(value.apiKey) == "" ||
		strings.TrimSpace(value.prompt) == "" || strings.ContainsRune(value.apiKey, 0) {
		return request{}, invalid
	}
	if _, explicit := fields["purpose"]; !explicit {
		value.purpose = "coding"
	}
	if value.purpose != "coding" && value.purpose != "pr_review" || value.purpose == "pr_review" && !prreviews.DigestPattern.MatchString(value.contextSHA256) || value.purpose == "coding" && value.contextSHA256 != "" {
		return request{}, invalid
	}
	settings, err := codexconfig.Resolve(value.model, value.effort)
	if err != nil {
		return request{}, invalid
	}
	value.model, value.effort = settings.Model, settings.ReasoningEffort
	return value, nil
}
