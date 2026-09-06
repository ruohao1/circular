package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/worker"
)

// Use the test executable as a real child process, without a shell or a Python
// dependency. The marker prevents this helper doing work in the parent suite.
func TestExecutorHelperProcess(t *testing.T) {
	marker := -1
	for i, arg := range os.Args {
		if arg == "circular-executor-helper" {
			marker = i
			break
		}
	}
	if marker < 0 {
		return
	}
	switch os.Args[marker+1] {
	case "arguments":
		if err := json.NewEncoder(os.Stdout).Encode(os.Args[marker+2:]); err != nil {
			os.Exit(2)
		}
	case "failure":
		os.Exit(7)
	case "graceful":
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		fmt.Println("ready")
		<-stop
		fmt.Println("stopped")
	case "unresponsive":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ready")
		<-time.After(time.Hour)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func process(t *testing.T, behavior string) worker.ProcessExecutor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return worker.ProcessExecutor{
		Command: []string{executable, "-test.run=^TestExecutorHelperProcess$", "--",
			"circular-executor-helper", behavior},
		GracePeriod: 100 * time.Millisecond,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	}
}

func TestProcessPassesClaimIdentityLiterally(t *testing.T) {
	p := process(t, "arguments")
	original := append([]string(nil), p.Command...)
	var output bytes.Buffer
	p.Stdout = &output
	id := uuid.New()
	owner := "--owner with spaces; $(not-a-command)"
	if err := p.Execute(t.Context(), worker.Claim{RunID: id, Recovery: true}, owner); err != nil {
		t.Fatal(err)
	}
	var arguments []string
	if err := json.Unmarshal(output.Bytes(), &arguments); err != nil {
		t.Fatal(err)
	}
	want := []string{"--run-id=" + id.String(), "--worker-id=" + owner, "--recovery"}
	if !reflect.DeepEqual(arguments, want) || !reflect.DeepEqual(p.Command, original) {
		t.Fatalf("claim arguments were altered: %q", arguments)
	}
}

func TestProcessSurfacesNonzeroExit(t *testing.T) {
	err := process(t, "failure").Execute(t.Context(), worker.Claim{RunID: uuid.New()}, "owner")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("unexpected child result: %v", err)
	}
}

func TestCancelledProcessDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := worker.ProcessExecutor{Command: []string{"/nonexistent/circular-executor"}, GracePeriod: time.Second}
	if err := p.Execute(ctx, worker.Claim{}, "owner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled invocation tried to start: %v", err)
	}
}

type readyOutput struct {
	buffer bytes.Buffer
	ready  chan struct{}
	once   sync.Once
}

func (w *readyOutput) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if strings.Contains(w.String(), "ready\n") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *readyOutput) String() string { return w.buffer.String() }

func TestProcessShutdownIsGracefulOrBounded(t *testing.T) {
	for _, behavior := range []string{"graceful", "unresponsive"} {
		t.Run(behavior, func(t *testing.T) {
			p := process(t, behavior)
			if behavior == "graceful" {
				// Race-instrumented child executables pause briefly on exit.
				p.GracePeriod = 2 * time.Second
			}
			output := &readyOutput{ready: make(chan struct{})}
			p.Stdout = output
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- p.Execute(ctx, worker.Claim{RunID: uuid.New()}, "owner") }()
			select {
			case <-output.ready:
			case err := <-done:
				t.Fatalf("child exited before ready: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("child did not start")
			}
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("unexpected shutdown result: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("executor exceeded its shutdown bound")
			}
			if behavior == "graceful" && !strings.Contains(output.String(), "stopped\n") {
				t.Fatal("SIGTERM did not reach the child before it was killed")
			}
			if behavior == "unresponsive" && time.Since(start) < p.GracePeriod {
				t.Fatal("child was killed without the configured cleanup grace period")
			}
		})
	}
}
