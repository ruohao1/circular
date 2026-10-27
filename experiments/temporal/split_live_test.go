package temporaleval

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/testsupport"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func newSplitWorker(c client.Client, queue string, a Activities) worker.Worker {
	w := worker.New(c, queue, worker.Options{MaxHeartbeatThrottleInterval: 500 * time.Millisecond, DefaultHeartbeatThrottleInterval: 250 * time.Millisecond})
	split := SplitActivities{a}
	w.RegisterWorkflow(SplitWorkflow)
	w.RegisterWorkflow(CleanupWorkflow)
	w.RegisterActivityWithOptions(a.Execute, activity.RegisterOptions{Name: "execute"})
	w.RegisterActivityWithOptions(split.ClaimCleanup, activity.RegisterOptions{Name: "claim-cleanup"})
	w.RegisterActivityWithOptions(split.Retain, activity.RegisterOptions{Name: "retain"})
	w.RegisterActivityWithOptions(split.Release, activity.RegisterOptions{Name: "release"})
	return w
}
func runSplitWorker(t *testing.T, c client.Client, queue string, a Activities) {
	t.Helper()
	w := newSplitWorker(c, queue, a)
	must(t, w.Start())
	t.Cleanup(w.Stop)
}
func startSplitWorkflow(t *testing.T, c client.Client, queue string, f fixture) client.WorkflowRun {
	t.Helper()
	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: WorkflowID(f.id), TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL, Memo: map[string]any{"circular_run_id": f.id.String()}}, SplitWorkflow, f.id)
	must(t, err)
	return run
}
func startCleanupWorkflow(t *testing.T, c client.Client, queue string, f fixture) client.WorkflowRun {
	t.Helper()
	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: "circular-cleanup/" + f.id.String(), TaskQueue: queue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL, Memo: map[string]any{"circular_run_id": f.id.String()}}, CleanupWorkflow, f.id)
	must(t, err)
	return run
}

func TestSplitActivityWorkerProcess(t *testing.T) {
	path := os.Getenv("CIRCULAR_TEMPORAL_EVAL_CHILD")
	if path == "" {
		t.Skip("split worker subprocess helper")
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
	w := newSplitWorker(c, cfg.Queue, Activities{Pool: pool, Config: cfg.Config})
	must(t, w.Start())
	defer w.Stop()
	select {}
}

func TestLiveSplitWorkerLossNeverRepeatsAgentInvocation(t *testing.T) {
	for _, stage := range []string{"partial_provisioning", "uncertain_start", "retention", "cleanup"} {
		t.Run(stage, func(t *testing.T) { testWorkerLoss(t, stage, true) })
	}
}

func TestLiveSplitCancellationKeepsRetainedOutput(t *testing.T) {
	c := temporalClient(t)
	f := newFixture(t, nil)
	f.a.Config.FakeDelayMS = 1000
	queue := "temporal-split-" + f.id.String()
	runSplitWorker(t, c, queue, f.a)
	run := startSplitWorkflow(t, c, queue, f)
	waitFor(t, "backend output", func() bool {
		return exists(f.a.Config.Git.WorktreeRoot + "/" + f.id.String() + "/circular-result-" + f.id.String() + ".txt")
	})
	testsupport.Cancel(t, f.a.Pool, f.id)
	must(t, c.CancelWorkflow(t.Context(), run.GetID(), run.GetRunID()))
	if err := awaitWorkflow(t, run); err == nil {
		t.Fatal("cancelled Workflow completed successfully")
	}
	f.assertReleased(t, "cancelled", true)
	if f.starts(t) != 1 {
		t.Fatal("cancellation repeated execution")
	}
	replay(t, c, run, "split-cancellation", SplitWorkflow)
}

func TestLiveSplitLeaseWaitUsesARecordedTimer(t *testing.T) {
	c := temporalClient(t)
	f := newFixture(t, map[string]any{"fake_workload": true, "remove_fails": true})
	if err := f.a.Execute(t.Context(), f.id); err == nil {
		t.Fatal("cleanup failure was not injected")
	}
	f.setOptions(t, map[string]any{"fake_workload": true})
	queue := "temporal-split-" + f.id.String()
	runSplitWorker(t, c, queue, f.a)
	sql(t, f.a.Pool, `UPDATE runs SET lease_expires_at=CURRENT_TIMESTAMP+interval '3 seconds' WHERE id=$1`, f.id)
	run := startCleanupWorkflow(t, c, queue, f)
	must(t, awaitWorkflow(t, run))
	f.assertReleased(t, "succeeded", true)
	timers, claims := 0, 0
	it := c.GetWorkflowHistory(t.Context(), run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		event, err := it.Next()
		must(t, err)
		if event.GetTimerStartedEventAttributes() != nil {
			timers++
		}
		if event.GetActivityTaskScheduledEventAttributes().GetActivityType().GetName() == "claim-cleanup" {
			claims++
		}
	}
	if timers != 1 || claims != 2 {
		t.Fatalf("lease wait used timers=%d claim Activities=%d", timers, claims)
	}
	t.Logf("live lease: %d persisted timer, %d short claim Activities", timers, claims)
	replay(t, c, run, "split-lease-timer", CleanupWorkflow)
}

func TestLiveSplitRecoveryActivitiesSurviveWorkerLoss(t *testing.T) {
	for _, stage := range []string{"retain", "release"} {
		t.Run(stage, func(t *testing.T) {
			c := temporalClient(t)
			options := map[string]any{"fake_workload": true}
			if stage == "release" {
				options["remove_fails"] = true
			}
			f := newFixture(t, options)
			if stage == "retain" {
				sql(t, f.a.Pool, `ALTER TABLE events ADD CONSTRAINT fault_artifact CHECK(type<>'artifact.created')`)
			}
			if err := f.a.Execute(t.Context(), f.id); err == nil {
				t.Fatal("initial cleanup fault was not injected")
			}
			before := testsupport.Observe(t, f.a.Pool, f.id)
			f.setOptions(t, map[string]any{"fake_workload": true})
			if stage == "retain" {
				sql(t, f.a.Pool, `ALTER TABLE events DROP CONSTRAINT fault_artifact;
CREATE SEQUENCE entered_split_activity;
CREATE FUNCTION pause_split_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('entered_split_activity'); PERFORM pg_sleep(10); RETURN NEW; END $$;
CREATE TRIGGER pause_split_activity BEFORE INSERT ON artifacts FOR EACH ROW EXECUTE FUNCTION pause_split_activity()`)
			} else {
				sql(t, f.a.Pool, `CREATE SEQUENCE entered_split_activity;
CREATE FUNCTION pause_split_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('entered_split_activity'); PERFORM pg_sleep(10); RETURN NEW; END $$;
CREATE TRIGGER pause_split_activity BEFORE UPDATE ON runs FOR EACH ROW WHEN(OLD.worker_id IS NOT NULL AND NEW.worker_id IS NULL) EXECUTE FUNCTION pause_split_activity()`)
			}
			f.expire(t)
			queue := "temporal-split-" + f.id.String()
			kill := crashWorker(t, f, queue, "TestSplitActivityWorkerProcess")
			run := startCleanupWorkflow(t, c, queue, f)
			waitFor(t, "new "+stage+" Activity", func() bool {
				var entered bool
				must(t, f.a.Pool.QueryRow(t.Context(), `SELECT is_called FROM entered_split_activity`).Scan(&entered))
				return entered
			})
			var oldOwner string
			must(t, f.a.Pool.QueryRow(t.Context(), `SELECT worker_id FROM runs WHERE id=$1`, f.id).Scan(&oldOwner))
			kill()
			if stage == "retain" {
				sql(t, f.a.Pool, `DROP TRIGGER pause_split_activity ON artifacts`)
			} else {
				sql(t, f.a.Pool, `DROP TRIGGER pause_split_activity ON runs`)
			}
			f.expire(t)
			runSplitWorker(t, c, queue, f.a)
			must(t, awaitWorkflow(t, run))
			expected := "failed"
			if stage == "release" {
				expected = "succeeded"
			}
			f.assertReleased(t, expected, true)
			after := testsupport.Observe(t, f.a.Pool, f.id)
			if f.starts(t) != 1 || before.Run.Error != nil && (after.Run.Error == nil || *before.Run.Error != *after.Run.Error) {
				t.Fatal("separate Activity loss changed coding or its primary result")
			}
			if before.Count("workspace.released") == 0 && after.Count("workspace.released") != 1 {
				t.Fatal("recovery duplicated Workspace release")
			}
			a := SplitActivities{f.a}
			must(t, a.Retain(t.Context(), CleanupClaim{RunID: f.id, Owner: oldOwner}))
			must(t, a.Release(t.Context(), CleanupClaim{RunID: f.id, Owner: oldOwner}))
			t.Logf("SIGKILL in new %s Activity: Run=%s, backend starts=1, verified artifacts=2, Workspace=released", stage, after.Run.Status)
			replay(t, c, run, "split-activity-"+stage, CleanupWorkflow)
		})
	}
}
