package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ProcessExecutor is the temporary Python execution bridge. It is trusted
// worker infrastructure, never a command supplied by a Task or Agent.
// Like the existing Git adapter, this worker currently targets Unix hosts.
type ProcessExecutor struct {
	Command     []string
	GracePeriod time.Duration
	Stdout      io.Writer
	Stderr      io.Writer
}

func (p ProcessExecutor) Execute(ctx context.Context, claim Claim, workerID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(p.Command) == 0 || p.GracePeriod <= 0 {
		return fmt.Errorf("executor command and positive shutdown grace period are required")
	}
	args := append([]string(nil), p.Command[1:]...)
	args = append(args, "--run-id="+claim.RunID.String(), "--worker-id="+workerID)
	if claim.Recovery {
		args = append(args, "--recovery")
	}
	cmd := exec.Command(p.Command[0], args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = p.Stdout, p.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	// Also bound pipe draining if an unexpected descendant retains a pipe.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Signal the owned process group so an invocation through a launcher
		// cannot swallow shutdown. The Python supervisor shields final cleanup.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(p.GracePeriod)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}
