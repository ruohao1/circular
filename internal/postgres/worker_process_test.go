package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/testsupport"
)

type workerProcess struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func realWorkerFixture(t *testing.T) (supervisorFixture, string) {
	t.Helper()
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables real worker process tests")
	}
	f := newSupervisorFixture(t, nil)
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	binary := filepath.Join(t.TempDir(), "circular-worker-go")
	build := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-race", "-o", binary, "./cmd/circular-worker-go")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v %s", err, output)
	}
	f.config.Image = "circular-native-worker:test"
	build = exec.CommandContext(t.Context(), "docker", "build", "-f", "infra/fake-agent-workload.Dockerfile", "-t", f.config.Image, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v %s", err, output)
	}
	f.config.Docker.DockerExecutable = "docker"
	return f, binary
}

func startWorker(t *testing.T, f supervisorFixture, binary, owner string) *workerProcess {
	t.Helper()
	dsn, err := url.Parse(postgres.DatabaseURL(os.Getenv("TEST_DATABASE_URL")))
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", f.pool.Config().ConnConfig.RuntimeParams["search_path"])
	dsn.RawQuery = query.Encode()
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "CIRCULAR_") {
			env[key] = value
		}
	}
	for key, value := range map[string]string{
		"DATABASE_URL": dsn.String(), "CIRCULAR_WORKER_ID": owner,
		"CIRCULAR_REPOSITORY_CACHE_ROOT": f.config.Git.RepositoryCacheRoot, "CIRCULAR_WORKTREE_ROOT": f.config.Git.WorktreeRoot,
		"CIRCULAR_DOCKER_WORKTREE_ROOT": f.config.Git.WorktreeRoot, "CIRCULAR_ARTIFACT_ROOT": f.config.ArtifactRoot,
		"CIRCULAR_RUNNER_IMAGE": f.config.Image, "CIRCULAR_POLL_INTERVAL_SECONDS": "0.025", "GORACE": "atexit_sleep_ms=0",
	} {
		env[key] = value
	}
	command := exec.Command(binary)
	command.Dir = f.base // no developer .env may affect a fixture
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	log, err := os.Create(filepath.Join(t.TempDir(), "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = log
	command.Stderr = log
	p := &workerProcess{command: command, done: make(chan struct{})}
	if err := command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	go func() { p.err = command.Wait(); _ = log.Close(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(90 * time.Second):
			_ = command.Process.Kill()
			<-p.done
			t.Error("test-owned worker did not shut down")
		}
	})
	return p
}

func (p *workerProcess) stop(t *testing.T, signal syscall.Signal) {
	t.Helper()
	if err := p.command.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
		if signal == syscall.SIGTERM && p.err != nil {
			t.Fatalf("graceful worker exit: %v", p.err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("worker stop timed out")
	}
}

func waitSnapshot(t *testing.T, f supervisorFixture, id uuid.UUID, ready func(testsupport.Snapshot) bool) testsupport.Snapshot {
	t.Helper()
	deadline := time.Now().Add(35 * time.Second)
	for {
		snapshot := testsupport.Observe(t, f.pool, id)
		if ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not settle: %+v", snapshot.Run)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assertReleased(t *testing.T, f supervisorFixture, id uuid.UUID) testsupport.Snapshot {
	t.Helper()
	s := waitSnapshot(t, f, id, func(s testsupport.Snapshot) bool {
		return s.Run.WorkerID == nil && s.Workspace != nil && s.Workspace.Status == "released"
	})
	s.AssertReplay(t)
	if len(s.Artifacts) != 2 || s.Count("run.started") != 1 {
		t.Fatal(s)
	}
	if _, err := os.Lstat(filepath.Join(f.config.Git.WorktreeRoot, id.String())); !os.IsNotExist(err) {
		t.Fatal("live worktree remained")
	}
	output, err := exec.CommandContext(t.Context(), "docker", "ps", "-aq", "--filter", "label=io.circular.run_id="+id.String()).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("container leaked: %v %s", err, output)
	}
	handler, err := httpapi.New(f.pool, httpapi.Config{ArtifactRoot: f.config.ArtifactRoot})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Artifacts {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/runs/"+id.String()+"/artifacts/"+a.ID.String()+"/content", nil))
		if response.Code != 200 {
			t.Fatalf("retained content inaccessible: %d", response.Code)
		}
	}
	return s
}

func compensateContainer(t *testing.T, f supervisorFixture, id uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		docker, err := runtimes.NewDocker(f.config.Docker)
		if err == nil {
			err = docker.Release(ctx, id, "")
		}
		if err != nil {
			t.Errorf("test-owned container compensation: %v", err)
		}
	})
}

func TestRealWorkerPersistsSuccessAndFailureThroughHTTP(t *testing.T) {
	for _, mode := range []string{"none", "before_events", "after_first_event"} {
		t.Run(mode, func(t *testing.T) {
			f, binary := realWorkerFixture(t)
			compensateContainer(t, f, f.id)
			if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config=$2::json WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id, `{"failure":"`+mode+`","delay_ms":25}`); err != nil {
				t.Fatal(err)
			}
			p := startWorker(t, f, binary, "real-worker")
			s := assertReleased(t, f, f.id)
			p.stop(t, syscall.SIGTERM)
			if mode == "none" {
				if s.Run.Status != "succeeded" || s.Run.Error != nil || s.Count("run.completed") != 1 || s.Count("run.failed") != 0 {
					t.Fatal(s)
				}
			} else {
				if s.Run.Status != "failed" || s.Run.Error == nil || *s.Run.Error != "fake backend reported injected_failure" || s.Count("run.failed") != 1 || s.Count("run.completed") != 0 {
					t.Fatal(s)
				}
			}
		})
	}
}

func TestRealWorkerSignalsRetainOutputAndCrashRecoveryNeverReexecutes(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			f, binary := realWorkerFixture(t)
			compensateContainer(t, f, f.id)
			if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config='{"delay_ms":10000}' WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id); err != nil {
				t.Fatal(err)
			}
			p := startWorker(t, f, binary, "original-worker")
			live := waitSnapshot(t, f, f.id, func(s testsupport.Snapshot) bool { return s.Run.Status == "running" })
			p.stop(t, signal)
			if signal == syscall.SIGKILL {
				if live.Workspace == nil || live.Workspace.ContainerID == nil {
					t.Fatal("missing recovery identity")
				}
				if _, err := exec.CommandContext(t.Context(), "docker", "inspect", *live.Workspace.ContainerID).CombinedOutput(); err != nil {
					t.Fatal("crash fixture lost live container")
				}
				expire(t, f.pool, f.id)
				replacement := startWorker(t, f, binary, "replacement-worker")
				s := assertReleased(t, f, f.id)
				replacement.stop(t, syscall.SIGTERM)
				if s.Run.Error == nil || *s.Run.Error != "worker lease expired" {
					t.Fatal(s.Run)
				}
				var attempts int
				if err := f.pool.QueryRow(t.Context(), "SELECT recovery_attempts FROM runs WHERE id=$1", f.id).Scan(&attempts); err != nil || attempts != 1 {
					t.Fatal("recovery was not bounded", err)
				}
			}
			s := assertReleased(t, f, f.id)
			if s.Run.Status != "failed" || s.Count("run.failed") != 1 || s.Count("run.completed") != 0 {
				t.Fatal(s)
			}
		})
	}
}

func TestRealWorkersKeepCancellationAndContainerAuthorityRunScoped(t *testing.T) {
	f, binary := realWorkerFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config='{"delay_ms":3000}' WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id); err != nil {
		t.Fatal(err)
	}
	second := uuid.New()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) SELECT $2,task_id,agent_id,backend,'queued',2,'{}' FROM runs WHERE id=$1`, f.id, second); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{f.id, second} {
		compensateContainer(t, f, id)
	}
	one, two := startWorker(t, f, binary, "worker-one"), startWorker(t, f, binary, "worker-two")
	lives := map[uuid.UUID]testsupport.Snapshot{}
	for _, id := range []uuid.UUID{f.id, second} {
		live := waitSnapshot(t, f, id, func(s testsupport.Snapshot) bool { return s.Run.Status == "running" })
		lives[id] = live
		if live.Workspace == nil || live.Workspace.ContainerID == nil {
			t.Fatal("missing identity")
		}
		output, err := exec.CommandContext(t.Context(), "docker", "exec", *live.Workspace.ContainerID, "/circular-fake-workload", "--probe-isolation").CombinedOutput()
		if err != nil {
			t.Fatalf("isolation probe: %v %s", err, output)
		}
		var facts struct {
			UID      int  `json:"uid"`
			Database bool `json:"database_url"`
			Socket   bool `json:"docker_socket"`
			SSH      bool `json:"ssh_directory"`
		}
		if err := json.Unmarshal(output, &facts); err != nil {
			t.Fatal(err)
		}
		if facts.UID == 0 || facts.Database || facts.Socket || facts.SSH {
			t.Fatalf("control-plane authority leaked: %+v", facts)
		}
	}
	if *lives[f.id].Workspace.ContainerID == *lives[second].Workspace.ContainerID {
		t.Fatal("workers shared a container")
	}
	testsupport.Cancel(t, f.pool, f.id)
	testsupport.Cancel(t, f.pool, f.id)
	cancelled := assertReleased(t, f, f.id)
	if cancelled.Run.Status != "cancelled" || cancelled.Count("run.cancelled") != 1 || cancelled.Count("run.failed") != 0 || cancelled.Count("run.completed") != 0 {
		t.Fatal(cancelled)
	}
	if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, second.String())); err != nil {
		t.Fatal("cancellation removed another Run's worktree")
	}
	s := assertReleased(t, f, second)
	if s.Run.Status != "succeeded" {
		t.Fatal(s.Run)
	}
	one.stop(t, syscall.SIGTERM)
	two.stop(t, syscall.SIGTERM)
	handler, err := httpapi.New(f.pool, httpapi.Config{ArtifactRoot: f.config.ArtifactRoot})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/runs/"+second.String()+"/artifacts/"+cancelled.Artifacts[0].ID.String()+"/content", nil))
	if response.Code != 404 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("cross-Run artifact exposed: %d %s", response.Code, data)
	}
}
