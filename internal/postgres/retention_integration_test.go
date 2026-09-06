package postgres_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/execution"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/testsupport"
)

type retainedFixture struct {
	pool      *pgxpool.Pool
	store     *postgres.Resources
	retention *execution.Retention
	git       *git.Local
	config    git.Config
	worktree  git.Worktree
	content   *artifacts.LocalStore
	base      string
}

func retentionDocker(t *testing.T, f retainedFixture) *runtimes.Docker {
	t.Helper()
	dir := filepath.Join(f.base, "docker")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dockerExecutable, _ := testsupport.DockerSimulator(t, dir, nil)
	docker, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: f.config.WorktreeRoot, DockerExecutable: dockerExecutable, OperationTimeout: 2 * time.Second, StopTimeout: time.Millisecond * 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := docker.Release(ctx, f.worktree.RunID, ""); err != nil {
			t.Errorf("test runtime cleanup: %v", err)
		}
	})
	return docker
}

func TestCleanupRetainsOutputThenReleasesExactResourcesAndIsIdempotent(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	handle, err := docker.Start(t.Context(), runtimes.Spec{RunID: f.worktree.RunID, Image: "circular-fake-agent-workload:test", Worktree: f.worktree.Path, CPULimit: 1, MemoryLimitMB: 256, Stdin: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithRun(t.Context(), f.worktree.RunID, func(r *postgres.RunResources) error { _, err := r.RecordContainer(handle.ResourceID); return err }); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
			t.Fatal(err)
		}
	}
	state, err := f.store.Read(t.Context(), f.worktree.RunID)
	if err != nil || state.Workspace.Status != "released" || *state.Workspace.ContainerID != handle.ResourceID || len(state.Artifacts) != 2 || state.Status != "failed" {
		t.Fatalf("cleanup corrupted durable outcome: %+v %v", state, err)
	}
	if _, err := os.Lstat(f.worktree.Path); !os.IsNotExist(err) {
		t.Fatal("worktree was not released")
	}
	fixtureGit(t, f.worktree.RepositoryPath, "rev-parse", f.worktree.Branch)
	for _, a := range state.Artifacts {
		if _, err := f.content.Read(t.Context(), f.worktree.RunID, a.URI); err != nil {
			t.Fatal("released Workspace lost retained bytes")
		}
	}
	if _, err := docker.Wait(t.Context(), handle); err == nil {
		t.Fatal("released runtime handle remained active")
	}
	s := testsupport.Observe(t, f.pool, f.worktree.RunID)
	s.AssertReplay(t)
	if s.Count("artifact.created") != 2 || s.Count("git.diff.updated") != 1 || s.Count("run.failed") != 1 {
		t.Fatal(s.Types())
	}
	testsupport.AssertJSON(t, s.Types()[len(s.Events)-2:], []string{"workspace.failed", "workspace.released"})
}

func fixtureGit(t *testing.T, path string, args ...string) {
	t.Helper()
	if output, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %v %s", err, output)
	}
}

func retentionFixture(t *testing.T, options ...func(*git.Config)) retainedFixture {
	t.Helper()
	pool := database(t)
	id := seed(t, pool, 1)[0]
	queue := postgres.NewQueue(pool)
	acquire(t, queue, "retention-owner")
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "init", "--initial-branch=main")
	fixtureGit(t, source, "config", "user.name", "Circular Tests")
	fixtureGit(t, source, "config", "user.email", "circular@example.invalid")
	if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "--message=initial")
	config := git.Config{RepositoryCacheRoot: filepath.Join(base, "cache"), WorktreeRoot: filepath.Join(base, "worktrees")}
	for _, option := range options {
		option(&config)
	}
	local, err := git.NewLocal(config)
	if err != nil {
		t.Fatal(err)
	}
	repositoryID := uuid.New()
	_, err = pool.Exec(t.Context(), `INSERT INTO repositories (id,project_id,name,clone_url,default_branch,external_refs)
		SELECT $1,tasks.project_id,'retention repository',$3,'main','{}' FROM tasks JOIN runs ON runs.task_id=tasks.id WHERE runs.id=$2`, repositoryID, id, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE tasks SET repository_id=$2 WHERE id=(SELECT task_id FROM runs WHERE id=$1)`, id, repositoryID); err != nil {
		t.Fatal(err)
	}
	repository, err := local.Checkout(t.Context(), repositoryID, source)
	if err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), id, repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"output.txt": "visible output\n", "ignored.txt": "retained ignored output"} {
		if err := os.WriteFile(filepath.Join(w.Path, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := postgres.NewResources(pool, "retention-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.CreatePending(w.Path); return err }); err != nil {
		t.Fatal(err)
	}
	if err := queue.ReconcileExit(t.Context(), id, "retention-owner"); err != nil {
		t.Fatal(err)
	}
	content, err := artifacts.NewLocalStore(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	retention, err := execution.NewRetention(store, config, filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	return retainedFixture{pool, store, retention, local, config, w, content, base}
}

func TestCorruptArchiveBytesNeverPermitDestructiveCleanup(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.base, "artifacts", f.worktree.RunID.String(), "worktree.tar")
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteAt([]byte("corrupt"), 0)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal("fixture corruption failed")
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("corrupt retained output was accepted: %v", err)
	}
	if _, err := os.Stat(f.worktree.Path); err != nil {
		t.Fatal("corrupt archive caused loss of recoverable output")
	}
}

func TestRetainedPartialWorktreesCanFinishCleanupWithoutChangingTerminalOutcome(t *testing.T) {
	for _, scenario := range []struct{ status, missing string }{{"succeeded", "directory"}, {"failed", "metadata"}, {"cancelled", "metadata-and-pointer"}} {
		t.Run(scenario.status, func(t *testing.T) {
			f := retentionFixture(t)
			docker := retentionDocker(t, f)
			if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, f.worktree.RunID, scenario.status); err != nil {
				t.Fatal(err)
			}
			if scenario.missing == "directory" {
				if err := os.RemoveAll(f.worktree.Path); err != nil {
					t.Fatal(err)
				}
			} else {
				output, err := exec.CommandContext(t.Context(), "git", "-C", f.worktree.Path, "rev-parse", "--absolute-git-dir").Output()
				if err != nil {
					t.Fatal(err)
				}
				metadata := strings.TrimSpace(string(output))
				if filepath.Dir(metadata) != filepath.Join(f.worktree.RepositoryPath, ".git", "worktrees") {
					t.Fatal("unexpected fixture Git metadata location")
				}
				if err := os.RemoveAll(metadata); err != nil {
					t.Fatal(err)
				}
				if scenario.missing == "metadata-and-pointer" {
					if err := os.Remove(filepath.Join(f.worktree.Path, ".git")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
				t.Fatal(err)
			}
			state, err := f.store.Read(t.Context(), f.worktree.RunID)
			if err != nil || string(state.Status) != scenario.status || state.Workspace.Status != "released" || len(state.Artifacts) != 2 {
				t.Fatal("recovered cleanup changed its terminal decision or lost output")
			}
			if _, err := os.Lstat(f.worktree.Path); !os.IsNotExist(err) {
				t.Fatal("partial output directory remains")
			}
		})
	}
}

func TestRealContainerOutputIsRetainedByGoCleanup(t *testing.T) {
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables real container retention")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	const image = "circular-go-retention:test"
	if output, err := exec.CommandContext(ctx, "docker", "build", "-f", filepath.Join(root, "infra/fake-agent-workload.Dockerfile"), "-t", image, root).CombinedOutput(); err != nil {
		t.Fatalf("build retention workload: %v %s", err, output)
	}
	owner := git.FileOwner{UID: os.Getuid(), GID: os.Getgid()}
	if os.Geteuid() == 0 {
		owner = git.FileOwner{UID: 65532, GID: 65532}
	}
	if owner.UID == 0 || owner.GID == 0 {
		t.Skip("non-root workload identity required")
	}
	f := retentionFixture(t, func(c *git.Config) { c.Owner = &owner })
	docker, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: f.config.WorktreeRoot, ContainerUser: fmt.Sprintf("%d:%d", owner.UID, owner.GID), OperationTimeout: 5 * time.Second, StopTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := docker.Release(cleanup, f.worktree.RunID, ""); err != nil {
			t.Errorf("owned container cleanup: %v", err)
		}
	})
	input := []byte(fmt.Sprintf(`{"protocol_version":1,"run":{"id":%q,"task_title":"Go retention","task_description":"Retain isolated output","instructions":"Write deterministic output"},"behavior":{"delay_ms":0,"failure":"none"}}`, f.worktree.RunID.String()))
	handle, err := docker.Start(ctx, runtimes.Spec{RunID: f.worktree.RunID, Worktree: f.worktree.Path, Image: image, Command: []string{"--write-output"}, Stdin: input, CPULimit: 1, MemoryLimitMB: 256})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.WithRun(ctx, f.worktree.RunID, func(r *postgres.RunResources) error { _, err := r.RecordContainer(handle.ResourceID); return err }); err != nil {
		t.Fatal(err)
	}
	result, err := docker.Wait(ctx, handle)
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("workload failed: %+v %v", result, err)
	}
	if err := f.retention.Cleanup(ctx, f.worktree.RunID, docker); err != nil {
		t.Fatal(err)
	}
	state, err := f.store.Read(ctx, f.worktree.RunID)
	if err != nil || state.Workspace.Status != "released" || len(state.Artifacts) != 2 {
		t.Fatal("real output was not durably retained before release")
	}
	for _, a := range state.Artifacts {
		data, err := f.content.Read(ctx, f.worktree.RunID, a.URI)
		if err != nil || !bytes.Contains(data, []byte("circular-result-"+f.worktree.RunID.String()+".txt")) {
			t.Fatal("retained output lost the real container result")
		}
	}
	if output, err := exec.CommandContext(ctx, "docker", "container", "ls", "-aq", "--filter", "label=io.circular.run_id="+f.worktree.RunID.String()).Output(); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatal("test container remains after cleanup")
	}
}

func TestCleanupCanReconcileAnAbsentWorktreeRootAfterRetention(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.git.Release(t.Context(), f.worktree, git.ReleaseOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(f.config.WorktreeRoot) != f.base {
		t.Fatal("refused unexpected fixture root")
	}
	if err := os.RemoveAll(f.config.WorktreeRoot); err != nil {
		t.Fatal(err)
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
		t.Fatalf("already absent worktree could not be reconciled: %v", err)
	}
	state, err := f.store.Read(t.Context(), f.worktree.RunID)
	if err != nil || state.Workspace.Status != "released" {
		t.Fatal("completed cleanup was not durable")
	}
}

func TestIndependentRetentionInstancesFinishCleanupWithoutDuplicatingOutput(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
		t.Fatal(err)
	}
	retry, err := execution.NewRetention(f.store, f.config, filepath.Join(f.base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.Retain(t.Context(), f.worktree.RunID); err != nil {
		t.Fatal(err)
	}
	if err := retry.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
		t.Fatal(err)
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
		t.Fatal(err)
	}
	s := testsupport.Observe(t, f.pool, f.worktree.RunID)
	s.AssertReplay(t)
	if len(s.Artifacts) != 2 || s.Workspace == nil || s.Workspace.Status != "released" || s.Count("artifact.created") != 2 || s.Count("workspace.released") != 1 {
		t.Fatal(s)
	}
}

func TestGoRetentionPersistsReadableDiffAndIgnoredOutputWithoutReleasingWorktree(t *testing.T) {
	f := retentionFixture(t)
	for range 2 {
		if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
			t.Fatal(err)
		}
	}
	state, err := f.store.Read(t.Context(), f.worktree.RunID)
	if err != nil || len(state.Artifacts) != 2 || state.Workspace.Status != "pending" {
		t.Fatalf("retention altered resource ownership: %+v %v", state, err)
	}
	for _, a := range state.Artifacts {
		data, err := f.content.Read(t.Context(), f.worktree.RunID, a.URI)
		if err != nil {
			t.Fatal(err)
		}
		switch a.Kind {
		case "diff":
			if !bytes.Contains(data, []byte("visible output")) || bytes.Contains(data, []byte("retained ignored output")) {
				t.Fatal("diff content is not the final Git change")
			}
		case "workspace":
			reader := tar.NewReader(bytes.NewReader(data))
			found := false
			for {
				h, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Name == ".git" {
					t.Fatal("Git metadata was archived")
				}
				if h.Name == "ignored.txt" {
					b, err := io.ReadAll(reader)
					found = err == nil && string(b) == "retained ignored output"
				}
			}
			if !found {
				t.Fatal("archive lost ignored output")
			}
		default:
			t.Fatalf("unexpected artifact kind %s", a.Kind)
		}
	}
	if _, err := os.Stat(f.worktree.Path); err != nil {
		t.Fatal("retention released the worktree prematurely")
	}
}

func TestMissingRetainedBytesPreventDestructiveReleaseAndCanBeRepaired(t *testing.T) {
	f := retentionFixture(t)
	docker := retentionDocker(t, f)
	if err := f.retention.Retain(t.Context(), f.worktree.RunID); err != nil {
		t.Fatal(err)
	}
	uri, _ := artifacts.URI(f.worktree.RunID, "worktree.tar")
	original, err := f.content.Read(t.Context(), f.worktree.RunID, uri)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.base, "artifacts", f.worktree.RunID.String(), "worktree.tar")
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("cleanup accepted missing retained output: %v", err)
	}
	state, err := f.store.Read(t.Context(), f.worktree.RunID)
	if err != nil || state.Workspace.Status != "failed" || state.Status != "failed" {
		t.Fatal("retention failure lost the primary outcome or retry ownership")
	}
	if data, err := os.ReadFile(filepath.Join(f.worktree.Path, "ignored.txt")); err != nil || string(data) != "retained ignored output" {
		t.Fatal("unretained output was deleted")
	}
	if _, err := f.content.Write(t.Context(), f.worktree.RunID, "worktree.tar", original); err != nil {
		t.Fatal(err)
	}
	if err := f.retention.Cleanup(t.Context(), f.worktree.RunID, docker); err != nil {
		t.Fatalf("restored bytes could not finish cleanup: %v", err)
	}
}
