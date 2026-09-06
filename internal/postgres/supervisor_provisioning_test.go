package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestSupervisorRetainsContainerIdentityWhenProvisioningHandoffFails(t *testing.T) {
	for _, stage := range []string{"identity", "ready"} {
		t.Run(stage, func(t *testing.T) {
			f := newSupervisorFixture(t, nil)
			fault := `ALTER TABLE events ADD CONSTRAINT reject_started CHECK (type <> 'run.started')`
			if stage == "identity" {
				fault = `CREATE SEQUENCE identity_attempts;
CREATE FUNCTION reject_first_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF nextval('identity_attempts')=1 THEN RAISE EXCEPTION 'simulated identity outage'; END IF; RETURN NEW; END $$;
CREATE TRIGGER identity_outage BEFORE UPDATE ON workspaces FOR EACH ROW WHEN (OLD.container_id IS NULL AND NEW.container_id IS NOT NULL) EXECUTE FUNCTION reject_first_identity()`
			}
			if _, err := f.pool.Exec(t.Context(), fault); err != nil {
				t.Fatal(err)
			}
			claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err := supervisor.Execute(t.Context(), *claim, "supervisor-owner"); err == nil {
				t.Fatal("failed handoff was reported as success")
			}
			s := testsupport.Observe(t, f.pool, f.id)
			s.AssertReplay(t)
			if s.Run.Status != "failed" || s.Run.WorkerID != nil || s.Workspace == nil || s.Workspace.Status != "released" || s.Workspace.ContainerID == nil {
				t.Fatalf("handoff: %+v", s)
			}
			if len(s.Events) < 4 {
				t.Fatal(s.Types())
			}
			testsupport.AssertJSON(t, s.Types()[:4], []string{"workspace.provisioning", "workspace.provisioning", "workspace.failed", "run.failed"})
			if s.Events[2].Source != "worker" || s.Count("run.failed") != 1 || s.Count("run.started") != 0 || s.Count("run.completed") != 0 {
				t.Fatal(s.Types())
			}
			if _, err := os.Stat(filepath.Join(f.base, "docker", "fake-docker-state", "created")); !os.IsNotExist(err) {
				t.Fatal("failed handoff left its container behind")
			}
		})
	}
}

func TestSupervisorDiscardsUncommittedContainerWhenIdentityAndFailureWritesStayUnavailable(t *testing.T) {
	f := newSupervisorFixture(t, nil)
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE workspaces ADD CONSTRAINT reject_identity CHECK (container_id IS NULL);
ALTER TABLE events ADD CONSTRAINT reject_failure CHECK (type <> 'run.failed')`); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewQueue(f.pool)
	claim := acquire(t, queue, "supervisor-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Execute(t.Context(), *claim, "supervisor-owner"); err == nil {
		t.Fatal("uncommitted identity was reported as success")
	}
	store, err := postgres.NewResources(f.pool, "supervisor-owner")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context(), f.id)
	if err != nil || state.Status != "provisioning" || state.Workspace.ContainerID != nil {
		t.Fatalf("uncommitted failure abandoned its claim: %+v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(f.base, "docker", "fake-docker-state", "created")); !os.IsNotExist(err) {
		t.Fatal("uncommitted allocation was not compensated")
	}
	if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); err != nil {
		t.Fatal("uncommitted failure discarded recoverable worktree output")
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE workspaces DROP CONSTRAINT reject_identity; ALTER TABLE events DROP CONSTRAINT reject_failure`); err != nil {
		t.Fatal(err)
	}
	expire(t, f.pool, f.id)
	recovery := acquire(t, queue, "recovery-owner")
	cleaner, err := execution.NewSupervisor(f.pool, "recovery-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if recovery == nil || !recovery.Recovery {
		t.Fatal("interrupted handoff was not recoverable")
	}
	if err := cleaner.Execute(t.Context(), *recovery, "recovery-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); !os.IsNotExist(err) {
		t.Fatal("recovery did not finish cleanup")
	}
}

func TestSupervisorPrestartupGuardsDoNotAllocateResources(t *testing.T) {
	for _, scenario := range []string{"stale_owner", "cancelled", "shutdown", "invalid_recovery"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSupervisorFixture(t, nil)
			claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
			owner := "supervisor-owner"
			if scenario == "stale_owner" {
				owner = "stale-owner"
			}
			if scenario == "invalid_recovery" {
				claim.Recovery = true
			}
			if scenario == "cancelled" {
				testsupport.Cancel(t, f.pool, f.id)
			}
			supervisor, err := execution.NewSupervisor(f.pool, owner, f.config)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "shutdown" {
				cancel()
			}
			err = supervisor.Execute(ctx, *claim, owner)
			switch scenario {
			case "stale_owner":
				if !errors.Is(err, postgres.ErrLeaseLost) {
					t.Fatalf("stale owner was accepted: %v", err)
				}
			case "cancelled":
				if err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown did not settle its claimed attempt: %v", err)
				}
			case "invalid_recovery":
				if !errors.Is(err, postgres.ErrResourceState) {
					t.Fatalf("active Run accepted cleanup-only recovery: %v", err)
				}
			}
			for _, path := range []string{f.config.Git.WorktreeRoot, f.config.Git.RepositoryCacheRoot, filepath.Join(f.base, "docker", "fake-docker-state", "create-started")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("prestartup guard allocated resources")
				}
			}
			s := testsupport.Observe(t, f.pool, f.id)
			s.AssertReplay(t)
			if scenario == "cancelled" || scenario == "shutdown" {
				want := "failed"
				if scenario == "cancelled" {
					want = "cancelled"
				}
				if s.Run.Status != want || s.Run.WorkerID != nil {
					t.Fatal(s.Run)
				}
			} else {
				if s.Run.Status != "provisioning" || s.Run.WorkerID == nil || *s.Run.WorkerID != "supervisor-owner" {
					t.Fatal(s.Run)
				}
			}
			for _, e := range s.Events {
				if strings.HasPrefix(e.Type, "workspace.") {
					t.Fatal("prestartup guard allocated a Workspace")
				}
			}
		})
	}
}

func TestSupervisorFencesRecoveryWhileStartingAndHandingOffTheContainer(t *testing.T) {
	f := newSupervisorFixture(t, map[string]any{"fake_workload": true, "start_delay": 1.0})
	f.config.PollInterval = 2 * time.Second
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION shorten_test_lease() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
IF NEW.status='provisioning' THEN NEW.lease_expires_at=clock_timestamp()+interval '400 milliseconds'; END IF; RETURN NEW; END $$;
CREATE TRIGGER short_test_lease BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION shorten_test_lease()`); err != nil {
		t.Fatal(err)
	}
	queue := postgres.NewQueue(f.pool)
	claim := acquire(t, queue, "supervisor-owner")
	attempt := launchSupervisor(t, f, *claim, "supervisor-owner")
	marker := filepath.Join(f.base, "docker", "fake-docker-state", "start-invoked")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("container startup did not begin")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the shortened lease expire while Docker's startup is still in flight.
	time.Sleep(500 * time.Millisecond)
	if replacement := acquire(t, queue, "replacement-owner"); replacement != nil {
		t.Fatal("recovery crossed an in-flight allocation fence")
	}
	if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER short_test_lease ON runs`); err != nil {
		t.Fatal(err)
	}
	if err := attempt.wait(t); err != nil {
		t.Fatal(err)
	}
}
