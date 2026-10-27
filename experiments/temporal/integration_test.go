package temporaleval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
)

type fixture struct {
	a                           Activities
	id                          uuid.UUID
	base, state, options, calls string
	input                       postgres.RunLaunchInput
}

func newFixture(t *testing.T, options map[string]any) fixture {
	t.Helper()
	f := fixture{base: t.TempDir()}
	f.a.Pool = testsupport.Database(t)
	must(t, Init(t.Context(), f.a.Pool))
	source := filepath.Join(f.base, "source")
	must(t, os.Mkdir(source, 0700))
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"config", "user.name", "Circular Tests"}, {"config", "user.email", "circular@example.invalid"}} {
		gitCommand(t, source, args...)
	}
	must(t, os.WriteFile(filepath.Join(source, "README.md"), []byte("initial\n"), 0600))
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "--message=initial")
	project, repository, agent, task := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	sql(t, f.a.Pool, `INSERT INTO projects(id,name) VALUES($1,'Temporal evaluation')`, project)
	sql(t, f.a.Pool, `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Fixture',$3,'main','{}')`, repository, project, source)
	sql(t, f.a.Pool, `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Engineer','fake','','{}',true)`, agent, project)
	sql(t, f.a.Pool, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,'Execute fixture','Prepared requirements','open','{}','{"mode":"direct_completion","requirements":[]}')`, task, project, repository)
	f.input = postgres.RunLaunchInput{TaskID: task, AgentID: agent, RequestKey: "temporal-evaluation"}
	var err error
	f.id, err = Admit(t.Context(), f.a.Pool, f.input)
	must(t, err)
	dir := filepath.Join(f.base, "docker")
	must(t, os.Mkdir(dir, 0700))
	if options == nil {
		options = map[string]any{"fake_workload": true}
	}
	executable, state := testsupport.DockerSimulator(t, dir, options)
	f.state, f.options, f.calls = state, filepath.Join(dir, "docker-options.json"), filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "docker-log")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	must(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> "+quote(f.calls)+"\nexec "+quote(executable)+" \"$@\"\n"), 0700))
	root := filepath.Join(f.base, "worktrees")
	f.a.Config = execution.Config{
		Git:          git.Config{RepositoryCacheRoot: filepath.Join(f.base, "repositories"), WorktreeRoot: root},
		Docker:       runtimes.DockerConfig{WorktreeRoot: root, DockerExecutable: wrapper, OperationTimeout: 2 * time.Second, StopTimeout: 100 * time.Millisecond},
		ArtifactRoot: filepath.Join(f.base, "artifacts"), Image: "circular-fake-agent-workload:test", CPULimit: 1, MemoryLimitMB: 256, PollInterval: 25 * time.Millisecond,
	}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func sql(t *testing.T, p *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	_, err := p.Exec(t.Context(), q, args...)
	must(t, err)
}
func gitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
}
func waitFor(t *testing.T, why string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + why)
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func (f fixture) expire(t *testing.T) {
	t.Helper()
	sql(t, f.a.Pool, `UPDATE runs SET lease_expires_at=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, f.id)
}
func (f fixture) setOptions(t *testing.T, options map[string]any) {
	t.Helper()
	data, err := json.Marshal(options)
	must(t, err)
	must(t, os.WriteFile(f.options, data, 0600))
}
func (f fixture) starts(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return 0
	}
	must(t, err)
	n := 0
	for _, s := range strings.Fields(string(data)) {
		if s == "start" {
			n++
		}
	}
	return n
}
func (f fixture) assertReleased(t *testing.T, status string, output bool) {
	t.Helper()
	s := testsupport.Observe(t, f.a.Pool, f.id)
	s.AssertReplay(t)
	if s.Run.Status != status || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" || len(s.Artifacts) != 2 {
		t.Fatalf("Run/resources not settled: %+v", s)
	}
	if exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String())) || exists(filepath.Join(f.state, "created")) {
		t.Fatal("live resources remain")
	}
	content, err := artifacts.NewLocalStore(f.a.Config.ArtifactRoot)
	must(t, err)
	for _, a := range s.Artifacts {
		data, err := content.Read(t.Context(), f.id, a.URI)
		must(t, err)
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != fmt.Sprint(a.Metadata["sha256"]) {
			t.Fatal("artifact integrity mismatch")
		}
		if output && !bytes.Contains(data, []byte("Fake container workload completed: Execute fixture")) {
			t.Fatalf("%s lost fake output", a.Kind)
		}
	}
}

func TestManagedRunLifecycleAndRepeatedDelivery(t *testing.T) {
	f := newFixture(t, nil)
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- f.a.Execute(t.Context(), f.id) }()
	}
	succeeded := 0
	for range 2 {
		if <-done == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent deliveries executed %d times", succeeded)
	}
	f.assertReleased(t, "succeeded", true)
	before := testsupport.Observe(t, f.a.Pool, f.id)
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("duplicate execution receipt accepted")
	}
	must(t, f.a.Reconcile(t.Context(), f.id))
	after := testsupport.Observe(t, f.a.Pool, f.id)
	if f.starts(t) != 1 || len(after.Events) != len(before.Events) {
		t.Fatal("repeated delivery repeated side effects")
	}
}

func TestAdmissionIsAtomicAndReconnectsToTheSameRun(t *testing.T) {
	f := newFixture(t, nil)
	id, err := Admit(t.Context(), f.a.Pool, f.input)
	must(t, err)
	if id != f.id {
		t.Fatal("reconnect created a fresh Run")
	}
	other := uuid.New()
	sql(t, f.a.Pool, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) SELECT $1,project_id,repository_id,'Other','Prepared','open','{}',completion_policy FROM tasks WHERE id=$2`, other, f.input.TaskID)
	_, err = Admit(t.Context(), f.a.Pool, postgres.RunLaunchInput{TaskID: other, AgentID: f.input.AgentID})
	if err == nil {
		t.Fatal("single-schema evaluation limit not enforced")
	}
	var runs, sessions int
	must(t, f.a.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM work_sessions)`).Scan(&runs, &sessions))
	if runs != 1 || sessions != 1 {
		t.Fatalf("admission rollback left runs=%d sessions=%d", runs, sessions)
	}
}

func TestRetentionFailurePreservesOutputAndFencesTheOldOwner(t *testing.T) {
	f := newFixture(t, nil)
	sql(t, f.a.Pool, `ALTER TABLE events ADD CONSTRAINT fault_artifact CHECK(type<>'artifact.created')`)
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("retention failure hidden")
	}
	before := testsupport.Observe(t, f.a.Pool, f.id)
	if before.Run.Status != "failed" || !exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String())) {
		t.Fatal("failed retention discarded source")
	}
	sql(t, f.a.Pool, `ALTER TABLE events DROP CONSTRAINT fault_artifact`)
	f.expire(t)
	must(t, f.a.Reconcile(t.Context(), f.id))
	f.assertReleased(t, "failed", true)
	after := testsupport.Observe(t, f.a.Pool, f.id)
	if before.Run.Error == nil || after.Run.Error == nil || *after.Run.Error != *before.Run.Error || f.starts(t) != 1 {
		t.Fatal("recovery changed the primary failure or reran coding")
	}
	old, err := postgres.NewResources(f.a.Pool, "temporal-execute:"+f.id.String())
	must(t, err)
	if err = old.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error { return r.RecordFailure("stale", nil) }); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("old owner not fenced: %v", err)
	}
}

func TestCleanupFailureKeepsOriginalBackendFailure(t *testing.T) {
	f := newFixture(t, map[string]any{"fake_workload": true, "remove_fails": true})
	sql(t, f.a.Pool, `UPDATE agents SET backend_config='{"failure":"after_first_event"}' WHERE id=$1`, f.input.AgentID)
	if err := f.a.Execute(t.Context(), f.id); err == nil || !strings.Contains(err.Error(), "fake backend reported injected_failure") {
		t.Fatalf("primary failure lost: %v", err)
	}
	before := testsupport.Observe(t, f.a.Pool, f.id)
	if before.Count("workspace.failed") < 1 {
		t.Fatal(before.Types())
	}
	f.setOptions(t, map[string]any{"fake_workload": true})
	f.expire(t)
	must(t, f.a.Reconcile(t.Context(), f.id))
	f.assertReleased(t, "failed", true)
	after := testsupport.Observe(t, f.a.Pool, f.id)
	if after.Run.Error == nil || *after.Run.Error != "fake backend reported injected_failure" || after.Count("run.failed") != 1 || f.starts(t) != 1 {
		t.Fatal("cleanup replaced failure or repeated coding")
	}
}

func TestAPICancellationRetainsOutputAndCleansResources(t *testing.T) {
	f := newFixture(t, nil)
	f.a.Config.FakeDelayMS = 1000
	done := make(chan error, 1)
	go func() { done <- f.a.Execute(t.Context(), f.id) }()
	waitFor(t, "backend output", func() bool {
		return exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String(), "circular-result-"+f.id.String()+".txt"))
	})
	testsupport.Cancel(t, f.a.Pool, f.id)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not finish cleanup")
	}
	f.assertReleased(t, "cancelled", true)
	if f.starts(t) != 1 {
		t.Fatal("cancellation repeated invocation")
	}
}

func TestDockerCLIHelper(t *testing.T) { testsupport.DockerCLIHelper() }

func TestReceiptBeforeClaimNeverStartsTheAgent(t *testing.T) {
	f := newFixture(t, nil)
	sql(t, f.a.Pool, `UPDATE temporal_eval_admissions SET execution_started=true`)
	must(t, f.a.Reconcile(t.Context(), f.id))
	s := testsupport.Observe(t, f.a.Pool, f.id)
	if s.Run.Status != "failed" || s.Run.WorkerID != nil || s.Workspace != nil || f.starts(t) != 0 {
		t.Fatalf("receipt/claim gap repeated execution: %+v", s)
	}
}

func TestReconcileWaitsForTheLiveLease(t *testing.T) {
	f := newFixture(t, nil)
	claim, err := postgres.NewQueue(f.a.Pool).Acquire(t.Context(), "live-owner")
	must(t, err)
	if claim == nil {
		t.Fatal("missing claim")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err = f.a.Reconcile(ctx, f.id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live lease was not respected: %v", err)
	}
	s := testsupport.Observe(t, f.a.Pool, f.id)
	if s.Run.WorkerID == nil || *s.Run.WorkerID != "live-owner" || f.starts(t) != 0 {
		t.Fatal("live owner was replaced")
	}
	f.expire(t)
	must(t, f.a.Reconcile(t.Context(), f.id))
}

func TestInitRefusesOrdinarySchema(t *testing.T) {
	f := newFixture(t, nil)
	cfg := f.a.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = "public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	defer pool.Close()
	if err = Init(t.Context(), pool); err == nil {
		t.Fatal("ordinary schema accepted")
	}
}
