package postgres_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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
	"github.com/ruohao1/circular/internal/worker"
)

type supervisedAttempt struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func launchSupervisor(t *testing.T, f supervisorFixture, claim worker.Claim, owner string) *supervisedAttempt {
	t.Helper()
	supervisor, err := execution.NewSupervisor(f.pool, owner, f.config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	attempt := &supervisedAttempt{cancel: cancel, done: make(chan struct{})}
	go func() { attempt.err = supervisor.Execute(ctx, claim, owner); close(attempt.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-attempt.done:
		case <-time.After(20 * time.Second):
			t.Error("supervisor did not finish owned cleanup")
		}
	})
	return attempt
}

func (a *supervisedAttempt) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-a.done:
		return a.err
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not settle promptly")
		return nil
	}
}

type supervisorFixture struct {
	pool   *pgxpool.Pool
	id     uuid.UUID
	base   string
	config execution.Config
}

func newSupervisorFixture(t *testing.T, options map[string]any) supervisorFixture {
	t.Helper()
	pool := database(t)
	id := seed(t, pool, 1)[0]
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "init", "--initial-branch=main")
	fixtureGit(t, source, "config", "user.name", "Circular Tests")
	fixtureGit(t, source, "config", "user.email", "circular@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "add", ".")
	fixtureGit(t, source, "commit", "--message=initial")
	repository := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repositories (id,project_id,name,clone_url,default_branch,external_refs)
		SELECT $1,tasks.project_id,'supervisor fixture',$3,'main','{}' FROM tasks JOIN runs ON runs.task_id=tasks.id WHERE runs.id=$2`, repository, id, source); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE tasks SET repository_id=$2 WHERE id=(SELECT task_id FROM runs WHERE id=$1)`, id, repository); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "docker")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if options == nil {
		options = map[string]any{"fake_workload": true}
	}
	dockerExecutable, _ := testsupport.DockerSimulator(t, dir, options)
	root := filepath.Join(base, "worktrees")
	return supervisorFixture{pool: pool, id: id, base: base, config: execution.Config{
		Git:          git.Config{RepositoryCacheRoot: filepath.Join(base, "repositories"), WorktreeRoot: root},
		Docker:       runtimes.DockerConfig{WorktreeRoot: root, DockerExecutable: dockerExecutable, OperationTimeout: 2 * time.Second, StopTimeout: 100 * time.Millisecond},
		ArtifactRoot: filepath.Join(base, "artifacts"), Image: "circular-fake-agent-workload:test", CPULimit: 1, MemoryLimitMB: 256, PollInterval: 25 * time.Millisecond,
	}}
}

func TestSupervisorCompletesAnIsolatedRunAndRetainsReplayAfterCleanup(t *testing.T) {
	f := newSupervisorFixture(t, nil)
	claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	var executor worker.Executor = supervisor
	if err := executor.Execute(t.Context(), *claim, "supervisor-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); !os.IsNotExist(err) {
		t.Fatal("completed Run retained its live worktree")
	}
	if _, err := os.Stat(filepath.Join(f.base, "docker", "fake-docker-state", "created")); !os.IsNotExist(err) {
		t.Fatal("completed Run retained its container")
	}
	s := testsupport.Observe(t, f.pool, f.id)
	if s.Run.Status != "succeeded" || s.Run.Error != nil || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" || s.Workspace.ContainerID == nil {
		t.Fatalf("success: %+v", s)
	}
	s.AssertTypes(t, "workspace.provisioning", "workspace.provisioning", "workspace.ready", "run.started", "agent.message.delta", "agent.message.delta", "agent.message.completed", "usage.updated", "artifact.created", "git.diff.updated", "run.completed", "artifact.created", "workspace.released")
	testsupport.AssertJSON(t, s.Events[6].Data, map[string]any{"content": "Fake container workload completed: Execute fixture"})
	testsupport.AssertJSON(t, s.Events[7].Data, map[string]any{"input_tokens": 2, "output_tokens": 6})
	for _, event := range s.Events[4:8] {
		if event.Raw["run_id"] != f.id.String() {
			t.Fatal(event)
		}
		testsupport.AssertJSON(t, event.Raw["data"], event.Data)
	}
	content, err := artifacts.NewLocalStore(f.config.ArtifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, a := range s.Artifacts {
		kinds[a.Kind] = true
		data, err := content.Read(t.Context(), f.id, a.URI)
		if err != nil {
			t.Fatal(err)
		}
		if a.Kind == "diff" {
			if !bytes.Contains(data, []byte("+Fake container workload completed: Execute fixture")) {
				t.Fatal("missing final diff")
			}
		} else {
			r := tar.NewReader(bytes.NewReader(data))
			found := false
			for {
				h, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Name == ".git" {
					t.Fatal("archive contains Git metadata")
				}
				if h.Name == "circular-result-"+f.id.String()+".txt" {
					found = true
				}
			}
			if !found {
				t.Fatal("archive lost output")
			}
		}
	}
	if len(s.Artifacts) != 2 || !kinds["diff"] || !kinds["workspace"] {
		t.Fatal(s.Artifacts)
	}
}

func TestSupervisorHonorsAgentFailureBehaviorAndRetainsRawBackendDiagnostics(t *testing.T) {
	for _, mode := range []string{"before_events", "after_first_event"} {
		t.Run(mode, func(t *testing.T) {
			f := newSupervisorFixture(t, nil)
			if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config=$2::json WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id, `{"delay_ms":25,"failure":"`+mode+`"}`); err != nil {
				t.Fatal(err)
			}
			claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err := supervisor.Execute(t.Context(), *claim, "supervisor-owner"); err == nil || !strings.Contains(err.Error(), "fake backend reported injected_failure") {
				t.Fatalf("backend failure behavior was not honored: %v", err)
			}
			s := testsupport.Observe(t, f.pool, f.id)
			s.AssertReplay(t)
			if s.Run.Status != "failed" || s.Run.Error == nil || *s.Run.Error != "fake backend reported injected_failure" || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" {
				t.Fatalf("failure: %+v", s)
			}
			wantDelta := 0
			if mode == "after_first_event" {
				wantDelta = 1
			}
			if s.Count("run.failed") != 1 || s.Count("run.completed") != 0 || s.Count("agent.message.delta") != wantDelta || len(s.Artifacts) != 2 {
				t.Fatal(s.Types())
			}
			for _, e := range s.Events {
				if e.Type == "run.failed" {
					if e.Raw["run_id"] != f.id.String() || e.Raw["error"].(map[string]any)["code"] != "injected_failure" {
						t.Fatal(e.Raw)
					}
				}
			}
			for _, a := range s.Artifacts {
				if a.Kind == "diff" && a.Metadata["empty"] != (mode == "before_events") {
					t.Fatal(a.Metadata)
				}
			}
		})
	}
}

func TestSupervisorRenewsItsLeaseAndHonorsAPICancellationThroughCleanup(t *testing.T) {
	f := newSupervisorFixture(t, nil)
	if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend_config='{"delay_ms":3000}' WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id); err != nil {
		t.Fatal(err)
	}
	claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET lease_expires_at=$2 WHERE id=$1`, f.id, time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt := launchSupervisor(t, f, *claim, "supervisor-owner")
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		var lease time.Time
		if err := f.pool.QueryRow(t.Context(), "SELECT status,lease_expires_at FROM runs WHERE id=$1", f.id).Scan(&status, &lease); err != nil {
			t.Fatal(err)
		}
		if status == "running" {
			if !lease.After(time.Now().Add(55 * time.Second)) {
				t.Fatal("supervisor did not renew lease")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	testsupport.Cancel(t, f.pool, f.id)
	if err := attempt.wait(t); err != nil {
		t.Fatalf("API cancellation was not settled cleanly: %v", err)
	}
	s := testsupport.Observe(t, f.pool, f.id)
	s.AssertReplay(t)
	if s.Run.Status != "cancelled" || s.Run.Error != nil || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" {
		t.Fatalf("cancellation: %+v", s)
	}
	if s.Count("run.cancelled") != 1 || s.Count("run.failed") != 0 || s.Count("run.completed") != 0 {
		t.Fatal(s.Types())
	}
	cancelled := false
	for _, e := range s.Events {
		if cancelled && e.Source == "fake-container-workload" {
			t.Fatal("backend output after cancellation")
		}
		if e.Type == "run.cancelled" {
			cancelled = true
		}
	}
	if _, err := os.Lstat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); !os.IsNotExist(err) {
		t.Fatal("cancelled Run's worktree remained")
	}
}

func TestSupervisorRetriesTerminalDecisionsAndRecoversPersistentOutagesWithoutReexecution(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "temporary"
		if persistent {
			name = "persistent"
		}
		t.Run(name, func(t *testing.T) {
			f := newSupervisorFixture(t, nil)
			condition := "nextval('terminal_write_attempts') <= 2"
			if persistent {
				condition = "true"
			}
			if _, err := f.pool.Exec(t.Context(), `CREATE SEQUENCE terminal_write_attempts;
CREATE FUNCTION reject_terminal_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF NEW.status IN ('succeeded','failed') AND `+condition+` THEN RAISE EXCEPTION 'simulated terminal-write outage'; END IF;
RETURN NEW; END $$;
CREATE TRIGGER terminal_write_outage BEFORE UPDATE ON runs FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION reject_terminal_write()`); err != nil {
				t.Fatal(err)
			}
			queue := postgres.NewQueue(f.pool)
			claim := acquire(t, queue, "supervisor-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err := supervisor.Execute(t.Context(), *claim, "supervisor-owner"); err == nil {
				t.Fatal("terminal-write outage was reported as success")
			}
			if persistent {
				store, err := postgres.NewResources(f.pool, "supervisor-owner")
				if err != nil {
					t.Fatal(err)
				}
				state, err := store.Read(t.Context(), f.id)
				if err != nil || state.Status != "finalizing" || state.Workspace.Status != "ready" {
					t.Fatalf("outage abandoned an active claim: %+v %v", state, err)
				}
				if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); err != nil {
					t.Fatal("outage discarded recoverable output")
				}
				if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER terminal_write_outage ON runs`); err != nil {
					t.Fatal(err)
				}
				expire(t, f.pool, f.id)
				recovery := acquire(t, queue, "recovery-owner")
				if recovery == nil || !recovery.Recovery {
					t.Fatal("failed terminal decision was not recoverable")
				}
				cleaner, err := execution.NewSupervisor(f.pool, "recovery-owner", f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := cleaner.Execute(t.Context(), *recovery, "recovery-owner"); err != nil {
					t.Fatal(err)
				}
			}
			s := testsupport.Observe(t, f.pool, f.id)
			s.AssertReplay(t)
			if s.Run.Status != "failed" || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" || len(s.Artifacts) != 2 {
				t.Fatalf("recovered outcome: %+v", s)
			}
			var recoveries int
			if err := f.pool.QueryRow(t.Context(), "SELECT recovery_attempts FROM runs WHERE id=$1", f.id).Scan(&recoveries); err != nil {
				t.Fatal(err)
			}
			wantRecoveries := 0
			if persistent {
				wantRecoveries = 1
			}
			if recoveries != wantRecoveries {
				t.Fatal(recoveries)
			}
			for _, kind := range []string{"run.started", "agent.message.completed", "run.failed", "git.diff.updated"} {
				if s.Count(kind) != 1 {
					t.Fatal(s.Types())
				}
			}
			if s.Count("run.completed") != 0 {
				t.Fatal(s.Types())
			}
		})
	}
}

func TestDockerCLIHelper(t *testing.T) { testsupport.DockerCLIHelper() }
