package temporaleval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"
)

func temporalClient(t *testing.T) client.Client {
	t.Helper()
	address := os.Getenv("TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("TEMPORAL_ADDRESS is required for real Temporal service checks")
	}
	c, err := client.Dial(client.Options{HostPort: address})
	must(t, err)
	t.Cleanup(c.Close)
	return c
}
func register(w worker.Worker, a Activities) {
	w.RegisterWorkflow(RunWorkflow)
	w.RegisterActivityWithOptions(a.Execute, activity.RegisterOptions{Name: "execute"})
	w.RegisterActivityWithOptions(a.Reconcile, activity.RegisterOptions{Name: "reconcile"})
}
func newWorker(c client.Client, queue string, a Activities) worker.Worker {
	w := worker.New(c, queue, worker.Options{MaxHeartbeatThrottleInterval: 500 * time.Millisecond, DefaultHeartbeatThrottleInterval: 250 * time.Millisecond})
	register(w, a)
	return w
}
func runWorker(t *testing.T, c client.Client, queue string, a Activities) {
	t.Helper()
	w := newWorker(c, queue, a)
	must(t, w.Start())
	t.Cleanup(w.Stop)
}
func awaitWorkflow(t *testing.T, run client.WorkflowRun) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	return run.Get(ctx, nil)
}

func replay(t *testing.T, c client.Client, run client.WorkflowRun, label string, definition any) {
	t.Helper()
	history := &historypb.History{}
	it := c.GetWorkflowHistory(t.Context(), run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		event, err := it.Next()
		must(t, err)
		history.Events = append(history.Events, event)
	}
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(definition)
	must(t, r.ReplayWorkflowHistory(nil, history))
	if dir := os.Getenv("TEMPORAL_EVIDENCE_DIR"); dir != "" {
		must(t, os.MkdirAll(dir, 0700))
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(history)
		must(t, err)
		must(t, os.WriteFile(filepath.Join(dir, label+"-history.json"), data, 0600))
	}
	t.Logf("replayed %d real service history events", len(history.Events))
}

func TestLiveAdmissionRecoversLostAcknowledgementAndReplays(t *testing.T) {
	c := temporalClient(t)
	f := newFixture(t, nil)
	queue := "temporal-eval-" + f.id.String()
	runWorker(t, c, queue, f.a)
	sql(t, f.a.Pool, `ALTER TABLE temporal_eval_admissions ADD CONSTRAINT fault_ack CHECK(NOT dispatched)`)
	if _, err := Dispatch(t.Context(), f.a.Pool, c, queue); err == nil {
		t.Fatal("lost DB acknowledgement not injected")
	}
	sql(t, f.a.Pool, `ALTER TABLE temporal_eval_admissions DROP CONSTRAINT fault_ack`)
	run, err := Dispatch(t.Context(), f.a.Pool, c, queue)
	must(t, err)
	must(t, awaitWorkflow(t, run))
	again, err := Dispatch(t.Context(), f.a.Pool, c, queue)
	must(t, err)
	if again.GetID() != run.GetID() {
		t.Fatal("duplicate dispatch changed identity")
	}
	f.assertReleased(t, "succeeded", true)
	if f.starts(t) != 1 {
		t.Fatal("lost acknowledgement repeated backend")
	}
	replay(t, c, run, "admission", RunWorkflow)
}

func TestLiveCancellationUsesExistingAPIPolicyAndShieldedCleanup(t *testing.T) {
	c := temporalClient(t)
	f := newFixture(t, nil)
	f.a.Config.FakeDelayMS = 1000
	queue := "temporal-eval-" + f.id.String()
	runWorker(t, c, queue, f.a)
	run, err := Dispatch(t.Context(), f.a.Pool, c, queue)
	must(t, err)
	waitFor(t, "backend output", func() bool {
		return exists(filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String(), "circular-result-"+f.id.String()+".txt"))
	})
	testsupport.Cancel(t, f.a.Pool, f.id)
	must(t, c.CancelWorkflow(t.Context(), run.GetID(), run.GetRunID()))
	if err = awaitWorkflow(t, run); err == nil {
		t.Fatal("Workflow cancellation was reported as success")
	}
	f.assertReleased(t, "cancelled", true)
	if f.starts(t) != 1 {
		t.Fatal("cancellation repeated agent")
	}
	replay(t, c, run, "cancellation", RunWorkflow)
}

type childConfig struct {
	DSN, Schema, Address, Queue string
	Config                      execution.Config
}

// The parent kills this process group; no graceful worker or Supervisor exit runs.
func TestActivityWorkerProcess(t *testing.T) {
	path := os.Getenv("CIRCULAR_TEMPORAL_EVAL_CHILD")
	if path == "" {
		t.Skip("worker subprocess helper")
	}
	data, err := os.ReadFile(path)
	must(t, err)
	var cfg childConfig
	must(t, json.Unmarshal(data, &cfg))
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	must(t, err)
	poolConfig.ConnConfig.RuntimeParams["search_path"] = cfg.Schema
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	must(t, err)
	defer pool.Close()
	c, err := client.Dial(client.Options{HostPort: cfg.Address})
	must(t, err)
	defer c.Close()
	w := newWorker(c, cfg.Queue, Activities{Pool: pool, Config: cfg.Config})
	must(t, w.Start())
	defer w.Stop()
	select {}
}

func crashWorker(t *testing.T, f fixture, queue, entrypoint string) func() {
	t.Helper()
	cfg := childConfig{DSN: f.a.Pool.Config().ConnConfig.ConnString(), Schema: f.a.Pool.Config().ConnConfig.RuntimeParams["search_path"], Address: os.Getenv("TEMPORAL_ADDRESS"), Queue: queue, Config: f.a.Config}
	data, err := json.Marshal(cfg)
	must(t, err)
	path := filepath.Join(f.base, "worker.json")
	must(t, os.WriteFile(path, data, 0600))
	executable, err := os.Executable()
	must(t, err)
	cmd := exec.Command(executable, "-test.run=^"+entrypoint+"$")
	cmd.Env = append(os.Environ(), "CIRCULAR_TEMPORAL_EVAL_CHILD="+path, "GORACE=atexit_sleep_ms=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log, err := os.Create(filepath.Join(f.base, "worker.log"))
	must(t, err)
	t.Cleanup(func() { log.Close() })
	cmd.Stdout, cmd.Stderr = log, log
	must(t, cmd.Start())
	var once sync.Once
	stop := func() { once.Do(func() { must(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)); _ = cmd.Wait() }) }
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(f.base, "worker.log"))
			t.Log(string(data))
		}
	})
	t.Cleanup(stop)
	return stop
}

func TestLiveWorkerLossNeverRepeatsAgentInvocation(t *testing.T) {
	for _, stage := range []string{"partial_provisioning", "uncertain_start", "retention", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			testWorkerLoss(t, stage, false)
		})
	}
}

func testWorkerLoss(t *testing.T, stage string, split bool) {
	t.Helper()
	c := temporalClient(t)
	options := map[string]any{"fake_workload": true}
	switch stage {
	case "partial_provisioning":
		options["create_hangs_after_creation"], options["create_delay"] = true, 5
	case "cleanup":
		options["remove_delay"] = 5
	}
	f := newFixture(t, options)
	if stage == "uncertain_start" {
		f.a.Config.FakeDelayMS = 1000
		original := f.a.Config.Docker.DockerExecutable
		blocked, allow := filepath.Join(f.base, "inspection-blocked"), filepath.Join(f.base, "allow-inspection")
		wrapper := filepath.Join(f.base, "uncertain-start")
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		script := "#!/bin/sh\nif [ \"$1\" = container ] && [ \"$2\" = inspect ] && [ \"$3\" = --format ] && [ ! -f " + quote(allow) + " ]; then\n touch " + quote(blocked) + "\n sleep 5\nfi\nexec " + quote(original) + " \"$@\"\n"
		must(t, os.WriteFile(wrapper, []byte(script), 0700))
		f.a.Config.Docker.DockerExecutable = wrapper
	}

	if stage == "retention" {
		sql(t, f.a.Pool, `CREATE FUNCTION pause_artifact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(10); RETURN NEW; END $$;
CREATE TRIGGER pause_artifact BEFORE INSERT ON artifacts FOR EACH ROW EXECUTE FUNCTION pause_artifact()`)
	}
	queue := "temporal-eval-" + f.id.String()
	entrypoint := "TestActivityWorkerProcess"
	if split {
		entrypoint = "TestSplitActivityWorkerProcess"
	}
	kill := crashWorker(t, f, queue, entrypoint)
	var run client.WorkflowRun
	var err error
	if split {
		run = startSplitWorkflow(t, c, queue, f)
	} else {
		run, err = Dispatch(t.Context(), f.a.Pool, c, queue)
	}
	must(t, err)
	marker := filepath.Join(f.state, "created")
	switch stage {
	case "uncertain_start":
		marker = filepath.Join(f.a.Config.Git.WorktreeRoot, f.id.String(), "circular-result-"+f.id.String()+".txt")
	case "retention":
		marker = filepath.Join(f.a.Config.ArtifactRoot, f.id.String(), "git-diff.patch")
	case "cleanup":
		marker = filepath.Join(f.state, "rm-started")
	}
	waitFor(t, stage, func() bool { return exists(marker) })
	if stage == "uncertain_start" {
		waitFor(t, "withheld start acknowledgement", func() bool { return exists(filepath.Join(f.base, "inspection-blocked")) })
		var status string
		var container *string
		must(t, f.a.Pool.QueryRow(t.Context(), `SELECT r.status,w.container_id FROM runs r JOIN workspaces w ON w.run_id=r.id WHERE r.id=$1`, f.id).Scan(&status, &container))
		if status != "provisioning" || container != nil {
			t.Fatal("start acknowledgement was already committed")
		}
	}
	kill()
	if stage == "uncertain_start" {
		must(t, os.WriteFile(filepath.Join(f.base, "allow-inspection"), nil, 0600))
	}
	f.setOptions(t, map[string]any{"fake_workload": true})
	if stage == "retention" {
		sql(t, f.a.Pool, `DROP TRIGGER pause_artifact ON artifacts`)
	}
	// Controlled expiry avoids a minute-long test; PostgreSQL still decides takeover.
	f.expire(t)
	if split {
		runSplitWorker(t, c, queue, f.a)
	} else {
		runWorker(t, c, queue, f.a)
	}
	if err = awaitWorkflow(t, run); err == nil {
		t.Fatal("lost execution acknowledgement was hidden")
	}
	expected := "failed"
	if stage == "cleanup" {
		expected = "succeeded"
	}
	f.assertReleased(t, expected, stage == "uncertain_start" || stage == "retention" || stage == "cleanup")
	wantStarts := 1
	if stage == "partial_provisioning" {
		wantStarts = 0
	}
	if f.starts(t) != wantStarts {
		t.Fatalf("backend starts=%d want=%d", f.starts(t), wantStarts)
	}
	if err = f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("repeated Activity delivery was accepted")
	}
	old, err := postgres.NewResources(f.a.Pool, "temporal-execute:"+f.id.String())
	must(t, err)
	if _, err = old.Read(t.Context(), f.id); err != postgres.ErrLeaseLost {
		t.Fatalf("stale owner was not fenced: %v", err)
	}
	snapshot := testsupport.Observe(t, f.a.Pool, f.id)
	if stage != "cleanup" && (snapshot.Run.Error == nil || *snapshot.Run.Error != "worker lease expired") {
		t.Fatalf("recovery reason changed: %+v", snapshot.Run)
	}
	t.Logf("SIGKILL at %s: Run=%s, backend starts=%d, verified artifacts=%d, Workspace=released", stage, snapshot.Run.Status, f.starts(t), len(snapshot.Artifacts))
	if split {
		replay(t, c, run, "split-"+stage, SplitWorkflow)
	} else {
		replay(t, c, run, stage, RunWorkflow)
	}
}

func TestLiveRealDockerFakeBackend(t *testing.T) {
	if os.Getenv("CIRCULAR_TEMPORAL_REAL_DOCKER") != "1" {
		t.Skip("CIRCULAR_TEMPORAL_REAL_DOCKER=1 enables the real container check")
	}
	c := temporalClient(t)
	f := newFixture(t, nil)
	f.a.Config.Image = "circular-temporal-eval-runner:test"
	f.a.Config.Docker.DockerExecutable = "docker"
	f.a.Config.Docker.OperationTimeout = 10 * time.Second
	// Use the unprivileged host owner of this disposable worktree.
	f.a.Config.Docker.ContainerUser = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	queue := "temporal-eval-" + f.id.String()
	runWorker(t, c, queue, f.a)
	run, err := Dispatch(t.Context(), f.a.Pool, c, queue)
	must(t, err)
	must(t, awaitWorkflow(t, run))
	f.assertReleased(t, "succeeded", true)
	s := testsupport.Observe(t, f.a.Pool, f.id)
	if s.Count("run.started") != 1 || s.Count("run.completed") != 1 {
		t.Fatal(s.Types())
	}
	if s.Workspace.ContainerID == nil {
		t.Fatal("missing immutable container receipt")
	}
	remaining, err := exec.Command("docker", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+*s.Workspace.ContainerID).CombinedOutput()
	must(t, err)
	if strings.TrimSpace(string(remaining)) != "" {
		t.Fatal("real container was not removed")
	}

	replay(t, c, run, "real-docker", RunWorkflow)
}

func TestLiveReceiptPreventsRestartWhenHistoryIsMissing(t *testing.T) {
	c := temporalClient(t)
	f := newFixture(t, nil)
	sql(t, f.a.Pool, `UPDATE temporal_eval_admissions SET execution_started=true`)
	if _, err := Dispatch(t.Context(), f.a.Pool, c, "unused"); err == nil {
		t.Fatal("receipt with missing history started a Workflow")
	}
	if f.starts(t) != 0 {
		t.Fatal("missing history repeated invocation")
	}
	must(t, f.a.Reconcile(t.Context(), f.id))
}
