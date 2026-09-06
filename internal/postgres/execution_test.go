package postgres_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestProvisioningContextUsesTheClaimedRunAndRejectsStaleOwners(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	queue := postgres.NewQueue(pool)
	acquire(t, queue, "execution-owner")
	repository := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repositories (id,project_id,name,clone_url,default_branch,external_refs)
		SELECT $1,tasks.project_id,'context fixture','https://example.invalid/fixture.git','release','{}'
		FROM tasks JOIN runs ON runs.task_id=tasks.id WHERE runs.id=$2`, repository, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE tasks SET repository_id=$2,title='Implement context',description='Preserve the Task'
		WHERE id=(SELECT task_id FROM runs WHERE id=$1)`, id, repository); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE agents SET instructions='Keep tests green',backend='not-the-run-backend',backend_config='{"delay_ms":25}'
		WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, id); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewResources(pool, "execution-owner")
	if err != nil {
		t.Fatal(err)
	}
	var inputs postgres.ProvisioningContext
	err = store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		var err error
		inputs, err = r.ProvisioningContext()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if inputs.RunID != id || inputs.WorkspaceID != postgres.WorkspaceID(id) || inputs.RepositoryID != repository ||
		inputs.CloneURL != "https://example.invalid/fixture.git" || inputs.BaseRef != "release" || inputs.Backend != "fake" ||
		inputs.TaskTitle != "Implement context" || inputs.TaskDescription != "Preserve the Task" || inputs.Instructions != "Keep tests green" {
		t.Fatalf("execution inputs changed their domain identities: %+v", inputs)
	}
	var config map[string]int
	if err := json.Unmarshal(inputs.BackendConfig, &config); err != nil || config["delay_ms"] != 25 {
		t.Fatalf("Agent configuration was not preserved: %s %v", inputs.BackendConfig, err)
	}
	expire(t, pool, id)
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.ProvisioningContext(); return err }); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("expired worker read execution inputs: %v", err)
	}
}

func TestBackendEventsPreserveNormalizedDataAndRawFailureForHTTPReplay(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "execution-owner")
	store, err := postgres.NewResources(pool, "execution-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if _, err := r.CreatePending(filepath.Join(t.TempDir(), id.String())); err != nil {
			return err
		}
		if _, err := r.RecordContainer("immutable-container-id"); err != nil {
			return err
		}
		_, err := r.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{"protocol_version": 1, "run_id": id.String(), "source": "fake-container-workload", "type": "agent.message.delta", "data": map[string]any{"delta": "hello 🌍"}}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		return r.AppendBackendEvent("agent.message.delta", map[string]any{"delta": "hello 🌍"}, raw)
	}); err != nil {
		t.Fatal(err)
	}
	failure := map[string]any{"protocol_version": 1, "run_id": id.String(), "error": map[string]any{"code": "injected_failure", "message": "test failure"}}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		return r.RecordFailure("bad\x00"+strings.Repeat("界", 4000), failure)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		return r.AppendBackendEvent("agent.message.delta", map[string]any{"delta": "too late"}, raw)
	}); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatalf("terminal Run accepted more backend output: %v", err)
	}
	s := testsupport.Observe(t, pool, id)
	s.AssertReplay(t)
	if s.Run.Status != "failed" || s.Run.Error == nil || *s.Run.Error != "bad�"+strings.Repeat("界", 3996) || s.Run.WorkerID == nil || *s.Run.WorkerID != "execution-owner" || len(s.Events) != 6 {
		t.Fatalf("failed outcome: %+v", s)
	}
	delta, failed := s.Events[4], s.Events[5]
	if delta.Type != "agent.message.delta" || delta.Source != "fake-container-workload" || failed.Type != "run.failed" {
		t.Fatal(s.Types())
	}
	testsupport.AssertJSON(t, delta.Data, map[string]any{"delta": "hello 🌍"})
	testsupport.AssertJSON(t, delta.Raw, raw)
	testsupport.AssertJSON(t, failed.Raw, failure)
	testsupport.AssertJSON(t, failed.Data, map[string]any{"error": *s.Run.Error})
}

func TestExecutionDecisionKeepsItsClaimUntilWorkspaceReleaseAndCannotBeOverwritten(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "execution-owner")
	store, err := postgres.NewResources(pool, "execution-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if _, err := r.CreatePending(filepath.Join(t.TempDir(), id.String())); err != nil {
			return err
		}
		if _, err := r.RecordContainer("immutable-container-id"); err != nil {
			return err
		}
		_, err := r.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { return r.ReleaseClaim() }); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatalf("active Run released its claim: %v", err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if err := r.BeginFinalizing(); err != nil {
			return err
		}
		return r.Complete()
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { return r.ReleaseClaim() }); !errors.Is(err, postgres.ErrResourceState) {
		t.Fatalf("unreleased Workspace lost its recovery claim: %v", err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if err := r.RecordFailure("late failure must not replace success", nil); err != nil {
			return err
		}
		if err := r.ReleaseWorkspace(); err != nil {
			return err
		}
		return r.ReleaseClaim()
	}); err != nil {
		t.Fatal(err)
	}
	s := testsupport.Observe(t, pool, id)
	if s.Run.Status != "succeeded" || s.Run.Error != nil || s.Run.WorkerID != nil || s.Run.StartedAt == nil || s.Run.FinishedAt == nil || s.Run.FinishedAt.Before(*s.Run.StartedAt) {
		t.Fatalf("completed outcome: %+v", s.Run)
	}
	var released bool
	if err := pool.QueryRow(t.Context(), "SELECT lease_expires_at IS NULL FROM runs WHERE id=$1", id).Scan(&released); err != nil || !released {
		t.Fatal("lease not released", err)
	}
	s.AssertTypes(t, "workspace.provisioning", "workspace.provisioning", "workspace.ready", "run.started", "run.completed", "workspace.released")
	testsupport.AssertJSON(t, s.Events[4].Data, map[string]any{})
	if s.Events[4].Source != "worker" {
		t.Fatal(s.Events[4])
	}
	if _, err := store.Heartbeat(t.Context(), id); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("released claim was renewed: %v", err)
	}
}

func TestHeartbeatsRenewLiveOwnershipAndObserveAPICancellationWithoutRevivingExpiredLeases(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	queue := postgres.NewQueue(pool)
	acquire(t, queue, "execution-owner")
	store, err := postgres.NewResources(pool, "execution-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE runs SET lease_expires_at=$2 WHERE id=$1`, id, time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Heartbeat(t.Context(), id); err != nil || status != "provisioning" {
		t.Fatalf("live heartbeat failed: %s %v", status, err)
	}
	var renewed bool
	if err := pool.QueryRow(t.Context(), "SELECT worker_id='execution-owner' AND lease_expires_at>now()+interval '55 seconds' FROM runs WHERE id=$1", id).Scan(&renewed); err != nil || !renewed {
		t.Fatal("heartbeat not renewed", err)
	}
	testsupport.Cancel(t, pool, id)
	if status, err := store.Heartbeat(t.Context(), id); err != nil || status != "cancelled" {
		t.Fatalf("heartbeat missed API cancellation: %s %v", status, err)
	}
	expire(t, pool, id)
	if _, err := store.Heartbeat(t.Context(), id); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("expired lease was revived: %v", err)
	}
	if claim := acquire(t, queue, "recovery-owner"); claim == nil || !claim.Recovery || claim.RunID != id {
		t.Fatal("expired cancelled attempt was not available for cleanup recovery")
	}
	if _, err := store.Heartbeat(t.Context(), id); !errors.Is(err, postgres.ErrLeaseLost) {
		t.Fatalf("stale owner renewed someone else's lease: %v", err)
	}
}

func TestProvisioningFailureCommitsTheContainerIdentityWorkspaceAndRunTogether(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "execution-owner")
	store, err := postgres.NewResources(pool, "execution-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		_, err := r.CreatePending(filepath.Join(t.TempDir(), id.String()))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Reject the last Event, after all preceding writes. No internal persistence
	// mocks: PostgreSQL itself must roll back the identity and both state changes.
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_execution_failure CHECK (type <> 'run.failed')`); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		return r.FailProvisioning("container handoff failed", "late-container-id")
	}); err == nil {
		t.Fatal("database fault did not reject the final Event")
	}
	state, err := store.Read(t.Context(), id)
	if err != nil || state.Status != "provisioning" || state.Workspace.Status != "pending" || state.Workspace.ContainerID != nil {
		t.Fatalf("failed transaction leaked a partial handoff: %+v %v", state, err)
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT reject_execution_failure`); err != nil {
		t.Fatal(err)
	}
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		return r.FailProvisioning("container handoff failed", "late-container-id")
	}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Read(t.Context(), id)
	if err != nil || state.Status != "failed" || state.Workspace.Status != "failed" || state.Workspace.ContainerID == nil || *state.Workspace.ContainerID != "late-container-id" {
		t.Fatalf("failure did not retain the cleanup identity: %+v %v", state, err)
	}
	s := testsupport.Observe(t, pool, id)
	s.AssertTypes(t, "workspace.provisioning", "workspace.provisioning", "workspace.failed", "run.failed")
	if s.Events[1].Data["container_id"] != "late-container-id" || s.Events[2].Data["container_id"] != "late-container-id" || s.Events[2].Source != "worker" || s.Events[3].Raw != nil {
		t.Fatal(s.Events)
	}
	testsupport.AssertJSON(t, s.Events[3].Data, map[string]any{"error": "container handoff failed"})
}
