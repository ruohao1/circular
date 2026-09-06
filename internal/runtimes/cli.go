package runtimes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

var ErrOperation = errors.New("Docker operation failed")

const systemPath = "/bin:/usr/bin"

// cliProcess owns a Unix process group. stdout/stderr copy goroutines finish
// before done closes; termination also bounds descendants retaining pipe ends.
type cliProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	stopOnce sync.Once
}

func dockerExecutable(configured string) (string, error) {
	candidates := []string{configured}
	if strings.ContainsRune(configured, filepath.Separator) {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("%w: Docker CLI path must be absolute", ErrOperation)
		}
	} else {
		candidates = nil
		for _, dir := range filepath.SplitList(systemPath) {
			candidates = append(candidates, filepath.Join(dir, configured))
		}
	}
	for _, candidate := range candidates {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: Docker CLI is unavailable", ErrOperation)
}

func (d *Docker) command(arguments []string, environment map[string]string) (*exec.Cmd, error) {
	executable, err := dockerExecutable(d.config.DockerExecutable)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, arguments...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = []string{"PATH=" + systemPath}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cmd.Env = append(cmd.Env, name+"="+environment[name])
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = time.Second
	return cmd, nil
}

func startCLI(cmd *exec.Cmd) (*cliProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: Docker CLI is unavailable", ErrOperation)
	}
	p := &cliProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		if errors.Is(p.err, exec.ErrWaitDelay) {
			// The CLI exited, but descendants retained its output pipes. The
			// group belongs to this invocation; contain it before publishing
			// completion instead of leaving an orphan behind closed pipes.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		close(p.done)
	}()
	return p, nil
}

func (p *cliProcess) terminate() {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	})
}

func (d *Docker) runCLI(ctx context.Context, arguments []string, environment map[string]string, timeout time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	cmd, err := d.command(arguments, environment)
	if err != nil {
		return 0, "", err
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	p, err := startCLI(cmd)
	if err != nil {
		return 0, "", err
	}
	select {
	case <-ctx.Done():
		p.terminate()
		return 0, "", fmt.Errorf("%w: operation cancelled or exceeded its deadline", ErrOperation)
	case <-p.done:
	}
	var exit *exec.ExitError
	if p.err != nil && !errors.As(p.err, &exit) {
		return 0, "", fmt.Errorf("%w: Docker response could not be read", ErrOperation)
	}
	if !utf8.Valid(output.Bytes()) {
		return 0, "", fmt.Errorf("%w: Docker returned invalid UTF-8", ErrOperation)
	}
	return cmd.ProcessState.ExitCode(), output.String(), nil
}

func (d *Docker) cli(ctx context.Context, arguments ...string) (int, string, error) {
	return d.runCLI(ctx, arguments, nil, d.config.OperationTimeout)
}
