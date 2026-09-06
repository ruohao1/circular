package git_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/runtimes"
)

func TestRealContainerProducesRetainedOutputThroughGoResourceModules(t *testing.T) {
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables real container resource integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	const image = "circular-go-resource-integration:test"
	build := exec.CommandContext(ctx, "docker", "build", "-f", filepath.Join(root, "infra/fake-agent-workload.Dockerfile"), "-t", image, root)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build test workload: %v %s", err, output)
	}
	base := t.TempDir()
	owner := git.FileOwner{UID: os.Getuid(), GID: os.Getgid()}
	if os.Geteuid() == 0 {
		owner = git.FileOwner{UID: 65532, GID: 65532}
	}
	if owner.UID == 0 || owner.GID == 0 {
		t.Skip("test requires a non-root workload identity")
	}
	local := localGit(t, base, func(c *git.Config) { c.Owner = &owner })
	repository, err := local.Checkout(ctx, uuid.New(), sourceRepository(t, base))
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(ctx, uuid.New(), repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	docker, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: filepath.Join(base, "worktrees"), ContainerUser: fmt.Sprintf("%d:%d", owner.UID, owner.GID), StopTimeout: time.Second, OperationTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := docker.Release(cleanup, w.RunID, ""); err != nil {
			t.Errorf("release only this test's container: %v", err)
		}
	})
	input, err := json.Marshal(map[string]any{
		"protocol_version": 1,
		"run":              map[string]any{"id": w.RunID.String(), "task_title": "Exercise Go resource modules", "task_description": "Retain isolated output", "instructions": "Write deterministic output"},
		"behavior":         map[string]any{"delay_ms": 0, "failure": "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := docker.Start(ctx, runtimes.Spec{RunID: w.RunID, Image: image, Worktree: w.Path, Command: []string{"--write-output"}, Stdin: input, CPULimit: 1, MemoryLimitMB: 256})
	if err != nil {
		t.Fatal(err)
	}
	result, err := docker.Wait(ctx, handle)
	if err != nil || result.Reason != runtimes.Exited || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("isolated execution failed: %+v %v", result, err)
	}
	output, err := docker.Output(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	for chunk, err := range output {
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Stream == runtimes.Stdout {
			stdout.Write(chunk.Data)
		}
	}
	if !bytes.Contains(stdout.Bytes(), []byte(w.RunID.String())) {
		t.Fatal("workload output lost Run correlation")
	}
	if err := docker.Discard(ctx, handle); err != nil {
		t.Fatal(err)
	}
	diff, err := local.Capture(ctx, w.Path)
	if err != nil || diff.ChangedFiles != 1 || diff.ContainsBinary || !bytes.Contains(diff.Content, []byte("circular-result-"+w.RunID.String()+".txt")) {
		t.Fatalf("Go capture lost container output: %+v %v", diff, err)
	}
	store, err := artifacts.NewLocalStore(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Write(ctx, w.RunID, "git-diff.patch", diff.Content)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Release(ctx, w, git.ReleaseOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Read(ctx, w.RunID, stored.URI)
	if err != nil || !bytes.Equal(retained, diff.Content) || !absent(w.Path) {
		t.Fatal("retained output did not survive exact worktree release")
	}
	gitCommand(t, repository, "rev-parse", w.Branch)
}
