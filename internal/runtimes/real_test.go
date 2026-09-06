package runtimes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runtimes"
)

const realImage = "circular-go-runtime:test"
const volumeImage = "circular-go-runtime-volume:test"

var imageBuild sync.Once
var imageBuildError error

func dockerCommand(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func realRuntime(t *testing.T, delay int, image string, worktreeRoots ...string) (*runtimes.Docker, runtimes.Spec) {
	t.Helper()
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables real Docker parity tests")
	}
	imageBuild.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		for _, args := range [][]string{
			{"build", "-f", filepath.Join(root, "infra/fake-agent-workload.Dockerfile"), "-t", realImage, "-t", "circular-fake-agent-workload:runtime-test", root},
			{"build", "-f", filepath.Join(root, "infra/fake-agent-workload-volume.Dockerfile"), "-t", volumeImage, root},
		} {
			if output, err := dockerCommand(ctx, args...); err != nil {
				imageBuildError = fmt.Errorf("build runtime test image: %w\n%s", err, output)
				return
			}
		}
	})
	if imageBuildError != nil {
		t.Fatal(imageBuildError)
	}
	id := uuid.New()
	root := filepath.Join(t.TempDir(), "worktrees")
	if len(worktreeRoots) != 0 {
		root = worktreeRoots[0]
	}
	worktree := filepath.Join(root, id.String())
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worktree, 0777); err != nil {
		t.Fatal(err)
	} // Non-root workload owns only this disposable mount.
	d, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: root, StopTimeout: time.Second, OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"protocol_version": 1,
		"run":              map[string]any{"id": id.String(), "task_title": "Exercise Go Docker runtime", "task_description": "Verify isolation", "instructions": "Emit deterministic events"},
		"behavior":         map[string]any{"delay_ms": delay, "failure": "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimes.Spec{RunID: id, Image: image, Worktree: worktree, Stdin: input, CPULimit: 1, MemoryLimitMB: 256}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Fixture compensation is scoped to the exact random Run and immutable
		// ID. It also removes a deliberately rejected image-volume allocation.
		name := "circular-run-" + strings.ReplaceAll(id.String(), "-", "")
		output, err := dockerCommand(ctx, "container", "inspect", name)
		if err != nil {
			return
		}
		var containers []struct {
			ID     string `json:"Id"`
			Name   string
			Config struct{ Labels map[string]string }
		}
		if err := json.Unmarshal(output, &containers); err != nil || len(containers) != 1 {
			t.Errorf("could not inspect test-owned container: %s", output)
			return
		}
		container := containers[0]
		if container.Name != "/"+name || container.Config.Labels["io.circular.managed"] != "true" || container.Config.Labels["io.circular.run_id"] != id.String() {
			t.Error("test cleanup refused a container with unexpected ownership")
			return
		}
		if output, err := dockerCommand(ctx, "rm", "--force", "--volumes", container.ID); err != nil {
			t.Errorf("remove exact test-owned container: %v %s", err, output)
		}
	})
	return d, spec
}

func TestRealDockerRunsExistingFakeProtocol(t *testing.T) {
	d, spec := realRuntime(t, 0, realImage)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	handle, err := d.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Wait(ctx, handle)
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("workload failed: %+v %v", result, err)
	}
	streams := outputBytes(t, d, handle)
	if streams[runtimes.Stderr] != "" {
		t.Fatalf("unexpected stderr: %s", streams[runtimes.Stderr])
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(streams[runtimes.Stdout]), "\n") {
		var event struct {
			Type  string
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.RunID != spec.RunID.String() {
			t.Fatal("output crossed Run identities")
		}
		types = append(types, event.Type)
	}
	if !reflect.DeepEqual(types, []string{"agent.message.delta", "agent.message.delta", "agent.message.completed", "usage.updated"}) {
		t.Fatalf("protocol changed: %v", types)
	}
	if err := d.Discard(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if output, err := dockerCommand(ctx, "container", "ls", "-aq", "--filter", "label=io.circular.run_id="+spec.RunID.String()); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("Run container leaked: %v %s", err, output)
	}
}

func TestRealDockerPreservesFastNonzeroExit(t *testing.T) {
	d, spec := realRuntime(t, 0, realImage)
	spec.Stdin = []byte("not-json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	handle, err := d.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Wait(ctx, handle)
	if err != nil || result.ExitCode == nil || *result.ExitCode != 2 {
		t.Fatalf("exit code lost: %+v %v", result, err)
	}
	if !strings.Contains(outputBytes(t, d, handle)[runtimes.Stderr], "invalid_input") {
		t.Fatal("backend error output lost")
	}
	if err := d.Discard(ctx, handle); err != nil {
		t.Fatal(err)
	}
}

func TestRealDockerRejectsImageVolumesBeforeExecution(t *testing.T) {
	d, spec := realRuntime(t, 0, volumeImage)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	before, err := dockerCommand(ctx, "volume", "ls", "-q")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(ctx, spec); !errors.Is(err, runtimes.ErrStart) {
		t.Fatalf("image volume accepted: %v", err)
	}
	if exists(filepath.Join(spec.Worktree, "circular-result-"+spec.RunID.String()+".txt")) {
		t.Fatal("unsafe container executed before inspection")
	}
	after, err := dockerCommand(ctx, "volume", "ls", "-q")
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected image left an anonymous volume")
	}
	if output, err := dockerCommand(ctx, "container", "ls", "-aq", "--filter", "label=io.circular.run_id="+spec.RunID.String()); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("rejected allocation leaked: %v %s", err, output)
	}
}

func TestRealDockerConcurrentRunsHaveIsolatedResources(t *testing.T) {
	first, spec1 := realRuntime(t, 10000, realImage)
	_, spec2 := realRuntime(t, 10000, realImage, filepath.Dir(spec1.Worktree))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type started struct {
		handle runtimes.Handle
		err    error
	}
	one, two := make(chan started, 1), make(chan started, 1)
	go func() { h, err := first.Start(ctx, spec1); one <- started{h, err} }()
	go func() { h, err := first.Start(ctx, spec2); two <- started{h, err} }()
	a, b := <-one, <-two
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent start failed: %v %v", a.err, b.err)
	}
	if a.handle.ResourceID == b.handle.ResourceID {
		t.Fatal("Runs shared a container")
	}
	for _, entry := range []struct {
		spec   runtimes.Spec
		handle runtimes.Handle
	}{{spec1, a.handle}, {spec2, b.handle}} {
		output, err := dockerCommand(ctx, "inspect", entry.handle.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		var containers []struct {
			Mounts []struct {
				Source, Destination, Type string
				RW                        bool
			}
			Config struct {
				User, WorkingDir string
				Env              []string
			}
			HostConfig struct {
				NetworkMode          string
				ReadonlyRootfs       bool
				NanoCpus, Memory     int64
				CapDrop, SecurityOpt []string
			}
		}
		if err := json.Unmarshal(output, &containers); err != nil || len(containers) != 1 {
			t.Fatal("invalid Docker inspection")
		}
		c := containers[0]
		if len(c.Mounts) != 1 || c.Mounts[0].Source != entry.spec.Worktree || c.Mounts[0].Destination != "/workspace" || c.Mounts[0].Type != "bind" || !c.Mounts[0].RW ||
			c.Config.User != "65532:65532" || c.Config.WorkingDir != "/workspace" || c.HostConfig.NetworkMode != "none" || !c.HostConfig.ReadonlyRootfs ||
			c.HostConfig.NanoCpus != 1_000_000_000 || c.HostConfig.Memory != 256*1024*1024 || !reflect.DeepEqual(c.HostConfig.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(c.HostConfig.SecurityOpt, []string{"no-new-privileges"}) {
			t.Fatalf("isolation changed: %+v", c)
		}
		for _, env := range c.Config.Env {
			if strings.HasPrefix(env, "DATABASE_URL=") || strings.HasPrefix(env, "DOCKER_") || strings.HasPrefix(env, "CIRCULAR_") || strings.HasPrefix(env, "SSH_") {
				t.Fatal("control-plane environment reached the Run container")
			}
		}
	}
	if err := first.Stop(ctx, a.handle); err != nil {
		t.Fatal(err)
	}
	if result, err := first.Wait(ctx, a.handle); err != nil || result.Reason != runtimes.Stopped || result.ExitCode != nil {
		t.Fatalf("stop did not settle: %+v %v", result, err)
	}
	if output, err := dockerCommand(ctx, "inspect", "--format", "{{.State.Status}}", b.handle.ResourceID); err != nil || strings.TrimSpace(string(output)) != "running" {
		t.Fatal("cancelling one Run stopped another")
	}
	if err := first.Discard(ctx, a.handle); err != nil {
		t.Fatal(err)
	}
	if err := first.Discard(ctx, b.handle); err != nil {
		t.Fatal(err)
	}
}

func TestRealDockerImmediateStopIsStable(t *testing.T) {
	for attempt := range 3 {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			d, spec := realRuntime(t, 10000, realImage)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			handle, err := d.Start(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := d.Stop(ctx, handle); err != nil {
					t.Fatal(err)
				}
				if result, err := d.Wait(ctx, handle); err != nil || result.Reason != runtimes.Stopped || result.ExitCode != nil {
					t.Fatalf("immediate repeated Stop did not settle: %+v %v", result, err)
				}
			}
			if err := d.Discard(ctx, handle); err != nil {
				t.Fatal(err)
			}
		})
	}
}
