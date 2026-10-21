package postgres_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/execution"
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

func TestDelegationFullWorkerLifecycle(t *testing.T) {
	// Build once; each outcome owns a separate database schema and worktree root.
	first, binary := realWorkerFixture(t)
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := first
			if outcome != "succeeded" {
				f = newSupervisorFixture(t, nil)
				f.config.Image = first.config.Image
				f.config.Docker.DockerExecutable = "docker"
			}
			var parentAgent, project uuid.UUID
			if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&parentAgent, &project); err != nil {
				t.Fatal(err)
			}
			target := uuid.New()
			childConfig := `{"delay_ms":350}`
			if outcome == "failed" {
				childConfig = `{"delay_ms":350,"failure":"after_first_event"}`
			}
			if outcome == "cancelled" {
				childConfig = `{"delay_ms":1000}`
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Inspector','fake','Inspect the checkpoint',$3,true)`, target, project, childConfig); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config=$2 WHERE id=$1`, parentAgent, testsupport.DelegationConfig(t, target)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO github_run_delivery_settings(project_id,enabled) VALUES($1,true)`, project); err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = postgres.SetDelegationTargets(t.Context(), tx, parentAgent, []uuid.UUID{target}); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			process := startWorker(t, f, binary, "delegation-acceptance")
			parked := waitSnapshot(t, f, f.id, func(s testsupport.Snapshot) bool {
				return s.Run.Status == "waiting_for_input" || s.Run.Status == "succeeded" || s.Run.Status == "failed"
			})
			if parked.Run.Status != "waiting_for_input" || parked.Count("run.completed") != 0 {
				t.Fatal("parent did not hand off", parked.Run, parked.Events)
			}
			var deliveries int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM github_run_deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
				t.Fatal("premature publication", deliveries, err)
			}
			var child uuid.UUID
			if err = f.pool.QueryRow(t.Context(), `SELECT child_run_id FROM delegations WHERE parent_run_id=$1`, f.id).Scan(&child); err != nil {
				t.Fatal(err)
			}
			waitSnapshot(t, f, child, func(s testsupport.Snapshot) bool { return s.Run.Status == "running" })
			// Admission froze local source access; enable publication eligibility without provider credentials.
			if _, err := f.pool.Exec(t.Context(), `UPDATE repositories SET external_refs='{"github":{"repository_id":"123","installation_id":"456"}}' WHERE project_id=$1`, project); err != nil {
				t.Fatal(err)
			}
			if outcome == "cancelled" {
				testsupport.Cancel(t, f.pool, child)
			}
			parent := waitSnapshot(t, f, f.id, func(s testsupport.Snapshot) bool {
				return s.Run.WorkerID == nil && s.Workspace != nil && s.Workspace.Status == "released"
			})
			process.stop(t, syscall.SIGTERM)
			childSnapshot := testsupport.Observe(t, f.pool, child)
			if parent.Run.Status != "succeeded" || parent.Count("run.completed") != 1 || parent.Count("run.started") != 1 || parent.Count("run.continued") != 1 || childSnapshot.Run.Status != outcome {
				t.Fatal(parent.Run, childSnapshot.Run, parent.Events)
			}
			var runs, tasks, invocations, attempt, unreleased int
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM tasks),(SELECT count(*) FROM run_invocations),(SELECT attempt FROM runs WHERE id=$1),(SELECT count(*) FROM workspaces WHERE status!='released')`, f.id).Scan(&runs, &tasks, &invocations, &attempt, &unreleased); err != nil {
				t.Fatal(err)
			}
			if runs != 2 || tasks != 2 || invocations != 3 || attempt != 1 || unreleased != 0 {
				t.Fatal(runs, tasks, invocations, attempt, unreleased)
			}
			var distinctWorkspaces, parentDeliveries, childDeliveries int
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(DISTINCT worktree_path) FROM workspaces),(SELECT count(*) FROM github_run_deliveries WHERE run_id=$1),(SELECT count(*) FROM github_run_deliveries WHERE run_id=$2)`, f.id, child).Scan(&distinctWorkspaces, &parentDeliveries, &childDeliveries); err != nil || distinctWorkspaces != 2 || parentDeliveries != 1 || childDeliveries != 0 {
				t.Fatal("workspace or publication identity", distinctWorkspaces, parentDeliveries, childDeliveries, err)
			}
			var overlap bool
			if err = f.pool.QueryRow(t.Context(), `SELECT (SELECT position FROM events WHERE run_id=$1 AND type='run.suspended') > (SELECT position FROM events WHERE run_id=$2 AND type='run.started') OR (SELECT position FROM events WHERE run_id=$2 AND type='workspace.released') > (SELECT position FROM events WHERE run_id=$1 AND type='run.continued')`, f.id, child).Scan(&overlap); err != nil || overlap {
				t.Fatal("invocations overlap", overlap, err)
			}
			store, err := artifacts.NewLocalStore(f.config.ArtifactRoot)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []uuid.UUID{f.id, child} {
				snapshot := testsupport.Observe(t, f.pool, id)
				if snapshot.Run.WorkerID != nil || snapshot.Workspace.Status != "released" {
					t.Fatal("claim or workspace leaked", snapshot.Run)
				}
				if _, err = os.Stat(filepath.Join(f.config.Git.WorktreeRoot, id.String())); !os.IsNotExist(err) {
					t.Fatal("worktree leaked", err)
				}
				output, err := exec.CommandContext(t.Context(), "docker", "ps", "-aq", "--filter", "label=io.circular.run_id="+id.String()).CombinedOutput()
				if err != nil || strings.TrimSpace(string(output)) != "" {
					t.Fatal("container leaked", err, string(output))
				}
				for _, a := range snapshot.Artifacts {
					var metadata struct {
						SHA256    string `json:"sha256"`
						SizeBytes int64  `json:"size_bytes"`
					}
					raw, _ := json.Marshal(a.Metadata)
					if err = json.Unmarshal(raw, &metadata); err != nil {
						t.Fatal(err)
					}
					file, err := store.OpenVerified(t.Context(), id, artifacts.Content{URI: a.URI, SHA256: metadata.SHA256, SizeBytes: metadata.SizeBytes})
					if err != nil {
						t.Fatal(err)
					}
					file.Close()
					if a.Kind == "workspace" {
						raw, err := store.Read(t.Context(), id, a.URI)
						if err != nil {
							t.Fatal(err)
						}
						archive := tar.NewReader(bytes.NewReader(raw))
						files := map[string]bool{}
						for {
							header, err := archive.Next()
							if err == io.EOF {
								break
							}
							if err != nil {
								t.Fatal(err)
							}
							files[header.Name] = true
						}
						if !files["circular-result-"+f.id.String()+".txt"] || (id == child) != files["circular-result-"+child.String()+".txt"] {
							t.Fatal("checkpoint or workspace isolation lost", id, files)
						}
					}
				}
			}
			childDiff, err := store.Read(t.Context(), child, "artifact://"+child.String()+"/git-diff.patch")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(childDiff), "circular-result-"+f.id.String()) || !strings.Contains(string(childDiff), "circular-result-"+child.String()) {
				t.Fatal("child diff not relative to parent checkpoint", string(childDiff))
			}
			parentDiff, err := store.Read(t.Context(), f.id, "artifact://"+f.id.String()+"/git-diff.patch")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(parentDiff), "circular-result-"+f.id.String()) || !strings.Contains(string(parentDiff), "Child outcome: "+outcome) || !strings.Contains(string(parentDiff), "Patch not applied") || strings.Contains(string(parentDiff), "diff --git a/circular-result-"+child.String()) {
				t.Fatal("parent lost progress or auto-applied child", string(parentDiff))
			}
			var result delegations.Result
			if err = f.pool.QueryRow(t.Context(), `SELECT result FROM delegations WHERE parent_run_id=$1 AND status='completed'`, f.id).Scan(&result); err != nil || string(result.Status) != childSnapshot.Run.Status || result.Patch == nil {
				t.Fatal(result, err)
			}
			var usage struct{ InputTokens, OutputTokens int }
			for _, event := range parent.Events {
				if event.Type == "usage.updated" {
					var sample struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					}
					raw, _ := json.Marshal(event.Data)
					if err = json.Unmarshal(raw, &sample); err != nil {
						t.Fatal(err)
					}
					usage.InputTokens += sample.InputTokens
					usage.OutputTokens += sample.OutputTokens
				}
			}
			handler, err := httpapi.New(f.pool, httpapi.Config{ArtifactRoot: f.config.ArtifactRoot})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/runs/"+f.id.String()+"/execution", nil))
			var executionRead struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				}
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &executionRead) != nil || executionRead.Usage.InputTokens != usage.InputTokens || executionRead.Usage.OutputTokens != usage.OutputTokens {
				t.Fatal("usage projection", response.Body.String(), usage)
			}
		})
	}
}

func realWorkerFixture(t *testing.T) (supervisorFixture, string) {
	t.Helper()
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables real worker process tests")
	}
	f := newSupervisorFixture(t, nil)
	binary := nativeWorkerBinary(t)
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	f.config.Image = "circular-native-worker:test"
	build := exec.CommandContext(t.Context(), "docker", "build", "-f", "infra/fake-agent-workload.Dockerfile", "-t", f.config.Image, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v %s", err, output)
	}
	f.config.Docker.DockerExecutable = "docker"
	return f, binary
}

func nativeWorkerBinary(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	binary := filepath.Join(t.TempDir(), "circular-worker-go")
	build := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-race", "-o", binary, "./cmd/circular-worker-go")
	build.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v %s", err, output)
	}
	return binary
}

func TestDelegationRecoveryProcessClaimBoundaries(t *testing.T) {
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables worker process crash fixtures")
	}
	binaryPath := nativeWorkerBinary(t)
	for _, stage := range []string{"before-commit", "after-commit"} {
		t.Run(stage, func(t *testing.T) {
			f, child := parkedDelegation(t)
			if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status='failed' WHERE id=$1`, child); err != nil {
				t.Fatal(err)
			}
			key := int64(binary.BigEndian.Uint32(f.id[:4]) & 0x7fffffff)
			lock, err := f.pool.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			defer lock.Exec(context.WithoutCancel(t.Context()), `SELECT pg_advisory_unlock($1)`, key)
			condition := "OLD.invocation=1 AND NEW.invocation=2"
			if stage == "after-commit" {
				condition = "OLD.invocation=2 AND NEW.invocation=2 AND NEW.worker_id='crashing-worker'"
			}
			// Block a real worker at a durable boundary without a production test hook.
			fault := fmt.Sprintf(`CREATE FUNCTION block_continuation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$;
		CREATE TRIGGER block_continuation BEFORE UPDATE ON runs FOR EACH ROW WHEN (NEW.id='%s' AND %s) EXECUTE FUNCTION block_continuation()`, key, f.id, condition)
			if _, err = f.pool.Exec(t.Context(), fault); err != nil {
				t.Fatal(err)
			}
			process := startWorker(t, f.supervisorFixture, binaryPath, "crashing-worker")
			deadline := time.Now().Add(15 * time.Second)
			for {
				var waiting bool
				if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted)`, key).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not reach claim boundary")
				}
				time.Sleep(10 * time.Millisecond)
			}
			process.stop(t, syscall.SIGKILL)
			if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, key); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(t.Context(), `DROP TRIGGER block_continuation ON runs`); err != nil {
				t.Fatal(err)
			}
			var invocation int
			var owner *string
			if err = f.pool.QueryRow(t.Context(), `SELECT invocation,worker_id FROM runs WHERE id=$1`, f.id).Scan(&invocation, &owner); err != nil {
				t.Fatal(err)
			}
			q := postgres.NewQueue(f.pool)
			if stage == "before-commit" {
				if invocation != 1 || owner != nil {
					t.Fatal("uncommitted claim survived", invocation, owner)
				}
				if err = cancelDelegationRun(t, f, f.id); err != nil {
					t.Fatal(err)
				}
			} else {
				if invocation != 2 || owner == nil {
					t.Fatal("committed claim lost", invocation, owner)
				}
				expire(t, f.pool, f.id)
			}
			claim := acquire(t, q, "process-recovery")
			if claim == nil || !claim.Recovery || claim.Continuation {
				t.Fatal("crashed invocation replayed", claim)
			}
			s, err := execution.NewSupervisor(f.pool, "process-recovery", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Execute(t.Context(), *claim, "process-recovery"); err != nil {
				t.Fatal(err)
			}
			got := testsupport.Observe(t, f.pool, f.id)
			if got.Run.WorkerID != nil || got.Workspace.Status != "released" || got.Count("run.continued") != 0 {
				t.Fatal(got.Run, got.Workspace)
			}
		})
	}
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
	secondTask := uuid.New()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) SELECT $2,t.project_id,t.repository_id,'Independent work',t.description,'open','{}','{"mode":"direct_completion","requirements":[]}' FROM tasks t JOIN runs r ON r.task_id=t.id WHERE r.id=$1`, f.id, secondTask); err != nil {
		t.Fatal(err)
	}
	var agent uuid.UUID
	if err = tx.QueryRow(t.Context(), `SELECT agent_id FROM runs WHERE id=$1`, f.id).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	launched, err := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: secondTask, AgentID: agent})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := launched.RunID
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
