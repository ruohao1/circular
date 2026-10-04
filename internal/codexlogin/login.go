// Package codexlogin runs explicit Codex authentication commands in an owned
// container. It never reads credentials or starts the worker control plane.
package codexlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	ownershipLabel = "io.circular.codex-auth"
	systemPath     = "/bin:/usr/bin"
	loginLimit     = 15 * time.Minute
	operationLimit = 15 * time.Second
	cleanupLimit   = 10 * time.Second
	outputLimit    = 64 * 1024
)

var (
	containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	userID      = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)
	imageName   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[0-9a-f]{64})?$`)
)

// Config contains trusted host configuration. CredentialRoot is the exact
// Docker-daemon-visible directory prepared by execution.PrepareCodexAuth.
type Config struct {
	DockerExecutable string
	Image            string
	CredentialRoot   string
	ContainerUser    string
}

// Run forwards only the attached authentication command's output, including
// its device login URL/code. Docker control-operation failures are sanitized.
func Run(ctx context.Context, config Config, action string, stdout, stderr io.Writer) int {
	if action != "login" && action != "status" && action != "logout" {
		return fail(stderr, "usage: circular-codex-auth login|status|logout", 2)
	}
	if !validConfig(config) {
		return fail(stderr, "invalid Codex authentication configuration", 2)
	}
	program, err := dockerExecutable(config.DockerExecutable)
	if err != nil {
		return fail(stderr, "Docker CLI is unavailable for Codex authentication", 1)
	}
	ctx, cancel := context.WithTimeout(ctx, loginLimit)
	defer cancel()
	if ctx.Err() != nil {
		return fail(stderr, "Codex authentication cancelled or timed out", 1)
	}
	id := uuid.NewString()
	name := "circular-codex-auth-" + id
	network := "none"
	if action == "login" {
		network = "bridge"
	}
	arguments := []string{
		"create", "--name", name, "--label", ownershipLabel + "=" + id,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", config.ContainerUser, "--restart", "no", "--network", network,
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=128m,mode=1777",
		"--mount", "type=bind,source=" + config.CredentialRoot + ",target=/codex-auth",
		"--entrypoint", "/circular-codex-workload", config.Image, action,
	}
	code, output, err := control(ctx, program, arguments...)
	created := strings.TrimSpace(output)
	if err != nil || code != 0 || !containerID.MatchString(created) {
		// The daemon may have committed allocation even if the create response
		// was interrupted. Only the exact random name with our label is eligible.
		if cleanup(context.WithoutCancel(ctx), program, name, id, "") != nil {
			return fail(stderr, "Codex authentication could not start; container cleanup could not be confirmed", 1)
		}
		return fail(stderr, "Codex authentication container could not start", 1)
	}
	code, err = attached(ctx, program, created, stdout, stderr)
	cleaned := cleanup(context.WithoutCancel(ctx), program, name, id, created)
	if cleaned != nil {
		return fail(stderr, "Codex authentication container cleanup could not be confirmed", 1)
	}
	if ctx.Err() != nil {
		return fail(stderr, "Codex authentication cancelled or timed out", 1)
	}
	if err != nil {
		return fail(stderr, "Codex authentication command failed", 1)
	}
	return code
}

func validConfig(config Config) bool {
	root := config.CredentialRoot
	if !imageName.MatchString(config.Image) || !userID.MatchString(config.ContainerUser) || !filepath.IsAbs(root) || root == "/" || filepath.Clean(root) != root || !utf8.ValidString(root) || strings.ContainsRune(root, ',') {
		return false
	}
	for _, r := range root {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func dockerExecutable(configured string) (string, error) {
	if configured == "" {
		configured = "docker"
	}
	candidates := []string{configured}
	if strings.ContainsRune(configured, filepath.Separator) {
		if !filepath.IsAbs(configured) {
			return "", errors.New("Docker CLI unavailable")
		}
	} else {
		candidates = nil
		for _, directory := range filepath.SplitList(systemPath) {
			candidates = append(candidates, filepath.Join(directory, configured))
		}
	}
	for _, path := range candidates {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return resolved, nil
		}
	}
	return "", errors.New("Docker CLI unavailable")
}

func command(ctx context.Context, program string, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, arguments...)
	cmd.Env = []string{"PATH=" + systemPath}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error { return killGroup(cmd) }
	return cmd
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func wait(cmd *exec.Cmd) (int, error) {
	if err := cmd.Start(); err != nil {
		return 0, errors.New("Docker command could not start")
	}
	defer killGroup(cmd)
	err := cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return 0, errors.New("Docker command did not complete")
	}
	return cmd.ProcessState.ExitCode(), nil
}

func attached(ctx context.Context, program, id string, stdout, stderr io.Writer) (int, error) {
	cmd := command(ctx, program, "start", "--attach", id)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return wait(cmd)
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > outputLimit-b.Len() {
		return 0, errors.New("Docker control response is too large")
	}
	return b.Buffer.Write(data)
}

func control(ctx context.Context, program string, arguments ...string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationLimit)
	defer cancel()
	var output limitedBuffer
	cmd := command(ctx, program, arguments...)
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	code, err := wait(cmd)
	if ctx.Err() != nil {
		return 0, "", errors.New("Docker operation cancelled")
	}
	return code, output.String(), err
}

func cleanup(ctx context.Context, program, name, label, expectedID string) error {
	ctx, cancel := context.WithTimeout(ctx, cleanupLimit)
	defer cancel()
	code, output, err := control(ctx, program, "inspect", "--type", "container", "--format", "{{json .}}", name)
	if err != nil || code != 0 {
		// A failed create or externally removed container may have no allocation.
		// Distinguish confirmed absence from an unavailable Docker daemon.
		code, output, err = control(ctx, program, "container", "ls", "--all", "--no-trunc", "--quiet", "--filter", "label="+ownershipLabel+"="+label, "--filter", "name=^/"+name+"$")
		if err == nil && code == 0 && strings.TrimSpace(output) == "" {
			return nil
		}
		return errors.New("cannot confirm container ownership")
	}
	var allocation struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal([]byte(output), &allocation) != nil || !containerID.MatchString(allocation.ID) || allocation.Name != "/"+name || allocation.Config.Labels[ownershipLabel] != label || expectedID != "" && allocation.ID != expectedID {
		return errors.New("container ownership does not match")
	}
	// Removal uses the inspected immutable ID, never the replaceable name.
	code, _, err = control(ctx, program, "rm", "--force", allocation.ID)
	if err != nil || code != 0 {
		return errors.New("owned container could not be removed")
	}
	return nil
}

func fail(stderr io.Writer, message string, code int) int {
	_, _ = io.WriteString(stderr, message+"\n")
	return code
}
