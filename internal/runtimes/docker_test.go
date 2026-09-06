package runtimes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestDockerCLIHelper(t *testing.T) { testsupport.DockerCLIHelper() }

// Inject only the external Docker process, never the runtime's internal modules.
func simulatedDocker(t *testing.T, options map[string]any, configure ...func(*runtimes.DockerConfig)) (*runtimes.Docker, runtimes.Spec, string) {
	t.Helper()
	dir := t.TempDir()
	testsupport.DockerSimulator(t, dir, options)
	dockerConfig := runtimes.DockerConfig{
		WorktreeRoot: filepath.Join(dir, "worktrees"), DockerExecutable: filepath.Join(dir, "fake-docker"),
		AllowedEnvironmentNames: []string{"TASK_SCOPE_TOKEN"}, OperationTimeout: 2 * time.Second,
		StopTimeout: 100 * time.Millisecond,
	}
	for _, change := range configure {
		change(&dockerConfig)
	}
	d, err := runtimes.NewDocker(dockerConfig)
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimes.Spec{
		RunID: runID, Image: "circular-fake-agent-workload:test", CPULimit: 1.5, MemoryLimitMB: 384,
		Worktree: filepath.Join(dir, "worktrees", runID.String()), Stdin: []byte("{\"protocol_version\":1}\n"),
	}
	return d, spec, filepath.Join(dir, "fake-docker-state")
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func waitMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !exists(path) {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("simulator did not reach %s", filepath.Base(path))
		}
	}
}

func dockerCalls(t *testing.T, state string) [][]string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(state, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		var call struct{ Argv []string }
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		result = append(result, call.Argv)
	}
	return result
}

func operationCount(t *testing.T, state, operation string) int {
	t.Helper()
	count := 0
	for _, call := range dockerCalls(t, state) {
		if call[0] == operation {
			count++
		}
	}
	return count
}

func TestPolicyMismatchIsRemovedWithoutEverStarting(t *testing.T) {
	for _, mismatch := range []string{"mount_type", "mount_source", "mount_destination", "mount_rw", "network", "read_only", "cap_drop", "security", "cpu", "memory", "user", "workdir", "restart", "managed_label", "extra_circular_label"} {
		t.Run(mismatch, func(t *testing.T) {
			d, spec, state := simulatedDocker(t, map[string]any{"policy_mismatch": mismatch})
			if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrStart) {
				t.Fatalf("unsafe policy accepted: %v", err)
			}
			if exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "start-invoked")) {
				t.Fatal("unsafe allocation remained or ran")
			}
		})
	}
}

func TestStopContainsRunAfterAttachmentOrObservationFailure(t *testing.T) {
	for _, failedInspection := range []bool{false, true} {
		t.Run(fmt.Sprint(failedInspection), func(t *testing.T) {
			d, spec, state := simulatedDocker(t, map[string]any{"attachment_fails": true, "inspect_fails_after_ready": failedInspection})
			handle, err := d.Start(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Wait(t.Context(), handle); err == nil {
				t.Fatal("lost attachment was reported as success")
			}
			if err := d.Stop(t.Context(), handle); err != nil {
				t.Fatal(err)
			}
			exit, err := os.ReadFile(filepath.Join(state, "exit-code"))
			if err != nil || string(exit) != "137" {
				t.Fatal("running container survived lost observation")
			}
			if err := d.Discard(t.Context(), handle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationFinishesOwnedStartupAndCleanup(t *testing.T) {
	for _, stage := range []string{"start", "stop", "discard"} {
		t.Run(stage, func(t *testing.T) {
			opts := map[string]any{"waits_for_stop": true, "create_delay": 0.1, "stop_delay": 0.1, "remove_delay": 0.1}
			d, spec, state := simulatedDocker(t, opts)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			if stage == "start" {
				go func() { _, err := d.Start(ctx, spec); done <- err }()
				waitMarker(t, filepath.Join(state, "create-started"))
			} else {
				handle, err := d.Start(t.Context(), spec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := d.Discard(context.Background(), handle); err != nil {
						t.Error(err)
					}
				})
				if stage == "stop" {
					go func() { done <- d.Stop(ctx, handle) }()
					waitMarker(t, filepath.Join(state, "stop-started"))
				} else {
					go func() { done <- d.Discard(ctx, handle) }()
					waitMarker(t, filepath.Join(state, "rm-started"))
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			if stage != "stop" && exists(filepath.Join(state, "created")) {
				t.Fatal("cancelled cleanup left an owned container")
			}
			if stage == "stop" {
				exit, err := os.ReadFile(filepath.Join(state, "exit-code"))
				if err != nil || string(exit) != "137" {
					t.Fatal("cancelled Stop returned before termination")
				}
			}
		})
	}
}

func TestCancelledCreateNeverDeletesSameNameReplacement(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"create_delay": 0.1, "replace_name_on_create": true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := d.Start(ctx, spec); done <- err }()
	waitMarker(t, filepath.Join(state, "create-started"))
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !errors.Is(err, runtimes.ErrNameConflict) {
		t.Fatalf("ownership conflict lost: %v", err)
	}
	if !exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "rm-started")) {
		t.Fatal("foreign replacement was touched")
	}
}

func TestCancelledCreateRetainsCleanupFailureAndCancellation(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"create_delay": 0.1, "remove_fails": true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := d.Start(ctx, spec); done <- err }()
	waitMarker(t, filepath.Join(state, "create-started"))
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !errors.Is(err, runtimes.ErrStart) || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("cleanup failure or cancellation lost: %v", err)
	}
	if !exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "start-invoked")) {
		t.Fatal("failed cleanup did not retain the allocation safely")
	}
}

func TestRetainedCreateBlocksRetryUntilReconciled(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"create_delay": 0.6, "create_hangs_after_creation": true, "reconciliation_unavailable": true, "remove_delay": 0.05}, func(c *runtimes.DockerConfig) { c.OperationTimeout = 150 * time.Millisecond })
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrOperation) {
		t.Fatalf("ambiguous create not retained: %v", err)
	}
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrOperation) {
		t.Fatalf("unresolved retry accepted: %v", err)
	}
	if operationCount(t, state, "create") != 1 {
		t.Fatal("second allocation attempted before reconciliation")
	}
	if err := os.WriteFile(filepath.Join(state, "reconciliation-available"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := d.Start(ctx, spec); done <- err }()
	waitMarker(t, filepath.Join(state, "rm-started"))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry returned %v", err)
	}
	if exists(filepath.Join(state, "created")) || operationCount(t, state, "create") != 1 {
		t.Fatal("cancelled reconciliation leaked or allocated new work")
	}
}

func TestBlockedStdinHasABoundedFailureAndCompensation(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"start_delay": 5}, func(c *runtimes.DockerConfig) { c.OperationTimeout = 200 * time.Millisecond })
	spec.Stdin = bytes.Repeat([]byte("x"), 4*1024*1024)
	start := time.Now()
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrStart) {
		t.Fatalf("blocked input was accepted: %v", err)
	}
	if time.Since(start) > 3*time.Second || exists(filepath.Join(state, "created")) {
		t.Fatal("blocked input escaped its time or cleanup bound")
	}
}

func TestDiscardSettlesObserversEvenWhenStopAndKillFail(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"waits_for_stop": true, "stop_fails": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, err := d.Output(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan error, 1)
	go func() {
		for _, err := range output {
			if err != nil {
				drained <- err
				return
			}
		}
		drained <- nil
	}()
	if err := d.Discard(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(state, "created")) || operationCount(t, state, "stop") != 1 || operationCount(t, state, "kill") != 1 {
		t.Fatal("failed graceful shutdown did not fall back to exact-ID removal")
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Discard returned while its attachment and output observer were still running")
	}
}

func TestIncompleteOutputCannotReportSuccessfulCompletion(t *testing.T) {
	d, spec, _ := simulatedDocker(t, map[string]any{"output_pipe_linger": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	if result, err := d.Wait(t.Context(), handle); !errors.Is(err, runtimes.ErrStart) {
		t.Fatalf("incomplete Docker output was treated as success: %+v %v", result, err)
	}
}

func TestConcurrentCallsShareCreationStopAndDiscard(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"waits_for_stop": true, "create_delay": 0.05, "stop_delay": 0.05, "remove_delay": 0.05})
	type startResult struct {
		handle runtimes.Handle
		err    error
	}
	started := make(chan startResult, 32)
	for range cap(started) {
		go func() { handle, err := d.Start(t.Context(), spec); started <- startResult{handle, err} }()
	}
	var handle runtimes.Handle
	for range cap(started) {
		got := <-started
		if got.err != nil || (handle != (runtimes.Handle{}) && got.handle != handle) {
			t.Fatalf("same Run was not deduplicated: %+v", got)
		}
		handle = got.handle
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	for _, operation := range []func(context.Context, runtimes.Handle) error{d.Stop, d.Discard} {
		done := make(chan error, 64)
		for range cap(done) {
			go func() {
				for range 50 {
					if err := operation(t.Context(), handle); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
		}
		for range cap(done) {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, operation := range []string{"create", "start", "stop", "rm"} {
		if operationCount(t, state, operation) != 1 {
			t.Fatalf("concurrent %s was not idempotent", operation)
		}
	}
}

func TestCancelledObserversDoNotCancelSharedExecution(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"waits_for_stop": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.Wait(ctx, handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait observer ignored cancellation: %v", err)
	}
	output, err := d.Output(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range output {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("output observer ignored cancellation: %v", err)
		}
	}
	for _, err := range output {
		if !errors.Is(err, runtimes.ErrOutputConsumed) {
			t.Fatalf("output iterator could be reused: %v", err)
		}
	}
	if exists(filepath.Join(state, "exit-code")) || operationCount(t, state, "stop") != 0 {
		t.Fatal("cancelling an observer stopped shared execution")
	}
	if err := d.Stop(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if result, err := d.Wait(t.Context(), handle); err != nil || result.Reason != runtimes.Stopped || result.ExitCode != nil {
		t.Fatalf("cancellation poisoned shared completion: %+v %v", result, err)
	}
}

func TestUnknownOrForgedHandlesCannotControlResources(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"waits_for_stop": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	for _, forged := range []runtimes.Handle{{}, {ID: handle.ID, ResourceID: strings.Repeat("b", 64)}, {ID: "foreign", ResourceID: handle.ResourceID}} {
		if _, err := d.Wait(t.Context(), forged); !errors.Is(err, runtimes.ErrUnknownHandle) {
			t.Fatalf("forged wait accepted: %v", err)
		}
		if _, err := d.Output(t.Context(), forged); !errors.Is(err, runtimes.ErrUnknownHandle) {
			t.Fatalf("forged output accepted: %v", err)
		}
		if err := d.Stop(t.Context(), forged); !errors.Is(err, runtimes.ErrUnknownHandle) {
			t.Fatalf("forged stop accepted: %v", err)
		}
		if err := d.Discard(t.Context(), forged); !errors.Is(err, runtimes.ErrUnknownHandle) {
			t.Fatalf("forged discard accepted: %v", err)
		}
	}
	if operationCount(t, state, "stop") != 0 || operationCount(t, state, "rm") != 0 {
		t.Fatal("unowned handles touched resources")
	}
}

func TestEnvironmentAndLaunchSnapshotsDoNotLeakOrAlias(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "GITHUB_TOKEN", "DOCKER_HOST", "SSH_AUTH_SOCK", "CIRCULAR_SECRET", "PATH"} {
		t.Setenv(name, "ambient-must-not-leak")
	}
	d, spec, state := simulatedDocker(t, map[string]any{"policy_mismatch": "extra_label"})
	spec.Environment = map[string]string{"TASK_SCOPE_TOKEN": "scoped-secret"}
	spec.Command = []string{"echo", "safe"}
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	for _, change := range []func(*runtimes.Spec){
		func(s *runtimes.Spec) { s.Environment["TASK_SCOPE_TOKEN"] = "changed" },
		func(s *runtimes.Spec) { s.Command[1] = "changed" },
		func(s *runtimes.Spec) { s.Stdin[0] = 'x' },
	} {
		changed := spec
		changed.Environment = map[string]string{"TASK_SCOPE_TOKEN": "scoped-secret"}
		changed.Command = append([]string{}, spec.Command...)
		changed.Stdin = bytes.Clone(spec.Stdin)
		change(&changed)
		if _, err := d.Start(t.Context(), changed); !errors.Is(err, runtimes.ErrNameConflict) {
			t.Fatalf("changed launch was treated as idempotent: %v", err)
		}
	}
	// Changing the original caller's storage after Start must not alter the
	// adapter's identity snapshot either.
	spec.Environment["TASK_SCOPE_TOKEN"], spec.Command[1], spec.Stdin[0] = "changed", "changed", 'x'
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrNameConflict) {
		t.Fatalf("runtime retained mutable caller storage: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(state, "create-environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.Unmarshal(content, &environment); err != nil {
		t.Fatal(err)
	}
	if environment["TASK_SCOPE_TOKEN"] != "scoped-secret" || environment["PATH"] != "/bin:/usr/bin" || strings.Contains(string(content), "ambient-must-not-leak") {
		t.Fatal("Docker CLI environment was not explicitly scoped")
	}
	for _, call := range dockerCalls(t, state) {
		if strings.Contains(strings.Join(call, " "), "scoped-secret") {
			t.Fatal("environment value appeared in process arguments")
		}
	}
}

func TestEmptyInputRepresentationsHaveTheSameLaunchIdentity(t *testing.T) {
	d, spec, _ := simulatedDocker(t, nil)
	spec.Stdin = nil
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	spec.Stdin = []byte{}
	if again, err := d.Start(t.Context(), spec); err != nil || again != handle {
		t.Fatalf("equivalent empty input changed identity: %+v %v", again, err)
	}
}

func TestTimeoutWithoutCommitCanBeRetriedAfterConfirmedAbsence(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"create_delay": 0.6, "create_hangs_without_commit_once": true}, func(c *runtimes.DockerConfig) { c.OperationTimeout = 150 * time.Millisecond })
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrOperation) {
		t.Fatalf("initial creation did not time out: %v", err)
	}
	if exists(filepath.Join(state, "created")) || operationCount(t, state, "rm") != 0 {
		t.Fatal("absent allocation was treated as owned")
	}
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("confirmed absence did not clear the unresolved creation: %v", err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	if operationCount(t, state, "create") != 2 {
		t.Fatal("retry did not create exactly one new allocation")
	}
}

func TestAmbiguousCreateRemovesOwnedNonceBeforeCheckingForeignName(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"create_delay": 0.6, "create_hangs_after_creation": true, "owned_nonce_with_foreign_name": true}, func(c *runtimes.DockerConfig) { c.OperationTimeout = 150 * time.Millisecond })
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrOperation) {
		t.Fatalf("creation did not time out: %v", err)
	}
	if !exists(filepath.Join(state, "owned-removed")) || !exists(filepath.Join(state, "created")) {
		t.Fatal("reconciliation failed to separate the owned ID from the foreign name")
	}
	for _, call := range dockerCalls(t, state) {
		if call[0] == "rm" && call[len(call)-1] != strings.Repeat("a", 64) {
			t.Fatal("cleanup targeted a name or foreign ID")
		}
	}
}

func TestFailedDiscardIsTypedRetainedAndRetryable(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"remove_fails": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := d.Discard(t.Context(), handle); !errors.Is(err, runtimes.ErrDiscard) {
			t.Fatalf("removal failure was not retained: %v", err)
		}
		if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrDiscard) {
			t.Fatalf("failed discard allowed execution reuse: %v", err)
		}
	}
	if operationCount(t, state, "create") != 1 || operationCount(t, state, "rm") != 2 || !exists(filepath.Join(state, "created")) {
		t.Fatal("removal retries lost track of the owned allocation")
	}
}

func TestStartDoesNotReturnAContainerThatIsOnlyCreated(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"start_delay": 0.15, "waits_for_stop": true})
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	if !exists(filepath.Join(state, "start-attached")) {
		t.Fatal("Start returned before Docker reported the container running")
	}
	if err := d.Stop(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if result, err := d.Wait(t.Context(), handle); err != nil || result.Reason != runtimes.Stopped {
		t.Fatalf("immediate Stop raced readiness: %+v %v", result, err)
	}
}

func TestFailedCreateReconcilesOwnedResourcesBeforeReturning(t *testing.T) {
	for _, test := range []struct {
		name    string
		options map[string]any
		failure error
	}{
		{"invalid-identity", map[string]any{"invalid_create_id": true}, runtimes.ErrStart},
		{"nonzero-after-commit", map[string]any{"create_returns_failure": true}, runtimes.ErrStart},
		{"timeout-after-commit", map[string]any{"create_delay": 0.6, "create_hangs_after_creation": true}, runtimes.ErrOperation},
		{"late-daemon-commit", map[string]any{"create_delay": 0.6, "create_late_commit_delay": 0.25}, runtimes.ErrOperation},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, spec, state := simulatedDocker(t, test.options, func(config *runtimes.DockerConfig) { config.OperationTimeout = 150 * time.Millisecond })
			_, err := d.Start(t.Context(), spec)
			if !errors.Is(err, test.failure) {
				t.Fatalf("unexpected creation error: %v", err)
			}
			if !exists(filepath.Join(state, "rm-started")) || exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "start-invoked")) {
				t.Fatal("a failed creation left an owned allocation or started execution")
			}
		})
	}
}

func outputBytes(t *testing.T, d *runtimes.Docker, handle runtimes.Handle) map[runtimes.Stream]string {
	t.Helper()
	output, err := d.Output(t.Context(), handle)
	if err != nil {
		t.Fatal(err)
	}
	streams := map[runtimes.Stream]string{}
	for chunk, err := range output {
		if err != nil {
			t.Fatal(err)
		}
		streams[chunk.Stream] += string(chunk.Data)
	}
	return streams
}

func TestRunStreamsOutputAndKeepsOneStableResult(t *testing.T) {
	d, spec, state := simulatedDocker(t, nil)
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Discard(context.Background(), handle); err != nil {
			t.Error(err)
		}
	})
	if handle.ResourceID != strings.Repeat("a", 64) {
		t.Fatal("immutable resource identity was not returned")
	}
	again, err := d.Start(t.Context(), spec)
	if err != nil || again != handle {
		t.Fatalf("same launch did not return the original handle: %v", err)
	}
	result, err := d.Wait(t.Context(), handle)
	if err != nil || result.Reason != runtimes.Exited || result.ExitCode == nil || *result.ExitCode != 23 {
		t.Fatalf("unexpected result: %+v, %v", result, err)
	}
	if got := outputBytes(t, d, handle); !reflect.DeepEqual(got, map[runtimes.Stream]string{
		runtimes.Stdout: "first\nlast\n", runtimes.Stderr: "warning\n",
	}) {
		t.Fatalf("output changed: %q", got)
	}
	if _, err := d.Output(t.Context(), handle); !errors.Is(err, runtimes.ErrOutputConsumed) {
		t.Fatalf("second output consumer was allowed: %v", err)
	}
	stdin, err := os.ReadFile(filepath.Join(state, "stdin.bin"))
	if err != nil || !bytes.Equal(stdin, spec.Stdin) {
		t.Fatal("the one-shot input was not delivered intact")
	}
	if err := d.Stop(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	later, err := d.Wait(t.Context(), handle)
	if err != nil || !reflect.DeepEqual(result, later) {
		t.Fatal("stopping a naturally completed Run replaced its result")
	}
	*result.ExitCode = 99
	last, err := d.Wait(t.Context(), handle)
	if err != nil || *last.ExitCode != 23 {
		t.Fatal("a caller mutated shared completion state")
	}
}

func TestReleaseChecksPersistedOwnershipBeforeRemoving(t *testing.T) {
	for _, test := range []struct {
		name, id    string
		run         uuid.UUID
		replacement bool
		allowed     bool
	}{
		{"by-id", strings.Repeat("a", 64), runID, false, true},
		{"by-name-after-crash", "", runID, false, true},
		{"foreign-run", strings.Repeat("a", 64), uuid.New(), false, false},
		{"wrong-id", strings.Repeat("b", 64), runID, false, false},
		{"invalid-id", "--all", runID, false, false},
		{"same-name-replacement", "", runID, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d, spec, state := simulatedDocker(t, map[string]any{"replace_name_on_create": test.replacement})
			plan, err := d.Resolve(spec)
			if err != nil {
				t.Fatal(err)
			}
			// Model an allocation committed by a previous worker, not a live
			// handle issued by this adapter. The external simulator owns its state.
			create := exec.CommandContext(t.Context(), filepath.Join(filepath.Dir(state), "fake-docker"),
				"create", "--name", plan.ContainerName,
				"--label", "io.circular.managed=true", "--label", "io.circular.run_id="+runID.String(),
				"--mount", "type=bind,src="+spec.Worktree+",dst=/workspace", spec.Image)
			if output, err := create.CombinedOutput(); err != nil {
				t.Fatalf("seed abandoned allocation: %v %s", err, output)
			}
			err = d.Release(t.Context(), test.run, test.id)
			if test.allowed {
				if err != nil || exists(filepath.Join(state, "created")) {
					t.Fatalf("owned persisted allocation was not released: %v", err)
				}
				if err := d.Release(t.Context(), test.run, test.id); err != nil {
					t.Fatalf("repeat release failed: %v", err)
				}
			} else if !errors.Is(err, runtimes.ErrDiscard) || !exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "rm-started")) {
				t.Fatalf("foreign allocation was not protected: %v", err)
			}
		})
	}
}
