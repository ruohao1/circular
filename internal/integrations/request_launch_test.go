package integrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"strings"
	"sync"
	"testing"
	"time"
)

func preparedRequest(t *testing.T) (fixture, integrations.RequestRoute, integrations.ExternalRequestDetail) {
	t.Helper()
	f, r := agentRequestFixture(t)
	r.Mode = "approval"
	saved, e := f.service.SaveRequestRoute(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	id := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "Use precisely this approved request."))
	request, e := f.service.ExternalRequest(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	requestTask(t, f, saved, request)
	return f, saved, request
}

func requestTask(t *testing.T, f fixture, route integrations.RequestRoute, request integrations.ExternalRequestDetail) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := f.pool.QueryRow(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy)
		SELECT $1,$2,$3,title,prompt,'ready',jsonb_build_object('linear',jsonb_build_object('issue_id',issue_id,'account_id',source->>'organization_id')),'{"mode":"direct_completion","requirements":[]}'
		FROM external_requests WHERE id=$4 ON CONFLICT(project_id,(external_refs->'linear'->>'issue_id')) WHERE external_refs->'linear'->>'issue_id' IS NOT NULL DO UPDATE SET external_refs=tasks.external_refs RETURNING id`, id, route.ProjectID, route.RepositoryID, request.ID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func requestWorkTx(t *testing.T, f fixture, action func(pgx.Tx) error) {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = action(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalRequestWaitsForTaskOwner(t *testing.T) {
	for _, kind := range []string{"external", "native-cleanup"} {
		t.Run(kind, func(t *testing.T) {
			f, route, request := preparedRequest(t)
			task := requestTask(t, f, route, request)
			var session, run uuid.UUID
			var resources *postgres.Resources
			requestWorkTx(t, f, func(tx pgx.Tx) error {
				if kind == "external" {
					result, err := postgres.ClaimExternalWork(t.Context(), tx, postgres.ExternalWorkClaim{SessionID: uuid.New(), TaskID: task, ExpectedRevision: 1, ClientName: "Codex", Secret: bytes.Repeat([]byte{42}, 32)}, time.Now())
					session = result.Session.ID
					return err
				}
				result, err := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: task, AgentID: uuid.MustParse(route.AgentID)})
				run = result.RunID
				return err
			})
			if kind == "native-cleanup" {
				if _, err := postgres.NewQueue(f.pool).Acquire(t.Context(), "native-owner"); err != nil {
					t.Fatal(err)
				}
				resources, _ = postgres.NewResources(f.pool, "native-owner")
				if err := resources.WithRun(t.Context(), run, func(r *postgres.RunResources) error { return r.RecordFailure("Needs cleanup", nil) }); err != nil {
					t.Fatal(err)
				}
				if err := f.pool.QueryRow(t.Context(), `SELECT id FROM work_sessions WHERE run_id=$1`, run).Scan(&session); err != nil {
					t.Fatal(err)
				}
			}
			result, err := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
			if err != nil || result.Status != "waiting_for_active_run" || result.RunID != "" || !strings.Contains(result.Reason, session.String()) || !strings.Contains(result.Reason, "/tasks?taskId="+task.String()) {
				t.Fatal(result, err)
			}
			var runs int
			want := 0
			if kind == "native-cleanup" {
				want = 1
			}
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != want {
				t.Fatal("owner bypassed", runs, err)
			}
			if resources != nil {
				if err = resources.WithRun(t.Context(), run, func(r *postgres.RunResources) error { return r.ReleaseClaim() }); err != nil {
					t.Fatal(err)
				}
			} else {
				requestWorkTx(t, f, func(tx pgx.Tx) error {
					_, err := postgres.ReleaseTaskWork(t.Context(), tx, postgres.WorkAdminInput{TaskID: task, ExpectedSessionID: session, OperationID: uuid.New(), Reason: "Hand back task"}, time.Now())
					return err
				})
			}
			if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || worked {
				t.Fatal("release automatically restarted request", worked, err)
			}
		})
	}
}

func TestExternalRequestIneligibleNeedsPreparation(t *testing.T) {
	for _, mode := range []string{"description", "prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			f, route, request := preparedRequest(t)
			task := requestTask(t, f, route, request)
			route.Mode = "automatic"
			if _, err := f.service.SaveRequestRoute(t.Context(), route); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE external_requests SET status='ready' WHERE id=$1`, request.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "description" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET description=' ' WHERE id=$1`, task); err != nil {
					t.Fatal(err)
				}
			} else {
				blocker := uuid.New()
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Blocker','Finish first','open','{}')`, blocker, f.project); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO task_dependencies(project_id,task_id,prerequisite_task_id) VALUES($1,$2,$3)`, f.project, task, blocker); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || !worked {
				t.Fatal(worked, err)
			}
			got, err := f.service.ExternalRequest(t.Context(), request.ID)
			if err != nil || got.Status != "awaiting_approval" || !strings.Contains(got.Reason, "/tasks?taskId="+task.String()) {
				t.Fatal(got, err)
			}
			var runs int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
				t.Fatal(runs, err)
			}
		})
	}
}

func TestExternalRequestLaunchLockOrder(t *testing.T) {
	for _, competing := range []string{"delegation", "configuration"} {
		t.Run(competing, func(t *testing.T) {
			f, route, request := preparedRequest(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			query, id := `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, f.project
			if competing == "configuration" {
				query, id = `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, route.AgentID
			}
			if _, err = tx.Exec(ctx, query, id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := f.service.StartExternalRequest(ctx, request.ID, request.InputFingerprint)
				done <- err
			}()
			for {
				var blocked bool
				if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("launch did not reach lock barrier")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if competing == "delegation" {
				// Delegation admission and its policy writer both take Project before Agent.
				err = postgres.SetDelegationTargets(ctx, tx, uuid.MustParse(route.AgentID), nil)
			} else {
				_, err = tx.Exec(ctx, `UPDATE agents SET instructions='New instructions' WHERE id=$1`, route.AgentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if competing == "configuration" && !errors.Is(err, integrations.ErrRequestConflict) || competing == "delegation" && err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestExternalRequestLaunchAtomicConcurrentAndSnapshot(t *testing.T) {
	f, route, request := preparedRequest(t)
	other, e := integrations.New(f.pool, f.config)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan integrations.ExternalRequest, 8)
	fail := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			service := f.service
			if i%2 == 0 {
				service = other
			}
			r, e := service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
			results <- r
			fail <- e
		})
	}
	wg.Wait()
	close(results)
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	run := ""
	for r := range results {
		if run == "" {
			run = r.RunID
		}
		if r.RunID != run || run == "" {
			t.Fatal("duplicate or missing run", r)
		}
	}
	var runs, inputs, links int
	if e = f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM external_run_inputs),(SELECT count(*) FROM external_session_runs)`).Scan(&runs, &inputs, &links); e != nil || runs != 1 || inputs != 1 || links != 1 {
		t.Fatal(runs, inputs, links, e)
	}
	_, e = f.pool.Exec(t.Context(), `UPDATE agents SET instructions='changed after approval',backend_config='{"delay_ms":99}',enabled=false WHERE id=$1`, route.AgentID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.pool.Exec(t.Context(), `UPDATE tasks SET description='changed task after approval'`)
	if e != nil {
		t.Fatal(e)
	}
	if replay, e := other.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint); e != nil || replay.RunID != run {
		t.Fatal("keyed retry checked mutable agent", e)
	}
	owner := "external-snapshot-owner"
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='provisioning',worker_id=$2,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, run, owner); e != nil {
		t.Fatal(e)
	}
	resources, e := postgres.NewResources(f.pool, owner)
	if e != nil {
		t.Fatal(e)
	}
	var input postgres.ProvisioningContext
	e = resources.WithRun(t.Context(), uuid.MustParse(run), func(r *postgres.RunResources) error { var e error; input, e = r.ProvisioningContext(); return e })
	if e != nil || input.TaskDescription != request.Prompt || input.Instructions != "Preserve the requested behavior." || string(input.BackendConfig) != "{}" {
		t.Fatal("approved inputs drifted", input, e)
	}
	_, e = f.pool.Exec(t.Context(), `UPDATE repositories SET clone_url='/changed' WHERE id=$1`, route.RepositoryID)
	if e != nil {
		t.Fatal(e)
	}
	e = resources.WithRun(t.Context(), uuid.MustParse(run), func(r *postgres.RunResources) error { _, e := r.ProvisioningContext(); return e })
	if !errors.Is(e, postgres.ErrResourceConflict) {
		t.Fatal("repository drift did not conflict", e)
	}
}
func TestExternalRequestLaunchRejectsStaleApprovalAndRollsBack(t *testing.T) {
	f, route, request := preparedRequest(t)
	if _, e := f.pool.Exec(t.Context(), `UPDATE agents SET instructions='Review this change first' WHERE id=$1`, route.AgentID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint); !errors.Is(e, integrations.ErrRequestConflict) {
		t.Fatal("stale fingerprint accepted", e)
	}
	fresh, e := f.service.ExternalRequest(t.Context(), request.ID)
	if e != nil || fresh.InputFingerprint == request.InputFingerprint {
		t.Fatal("preview not refreshed", e)
	}
	if _, e = f.pool.Exec(t.Context(), `ALTER TABLE external_run_inputs ADD CONSTRAINT fixture_snapshot_failure CHECK(false) NOT VALID`); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.StartExternalRequest(t.Context(), request.ID, fresh.InputFingerprint); e == nil {
		t.Fatal("injected snapshot failure hidden")
	}
	var runs int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); e != nil || runs != 0 {
		t.Fatal("partial run survived failed transaction", runs, e)
	}
}
func TestExternalRequestLaunchWaitsForActiveIssueAndNeverAutoReplays(t *testing.T) {
	f, route, first := preparedRequest(t)
	if _, e := f.service.StartExternalRequest(t.Context(), first.ID, first.InputFingerprint); e != nil {
		t.Fatal(e)
	}
	second := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "Another request"))
	r, e := f.service.ExternalRequest(t.Context(), second)
	if e != nil {
		t.Fatal(e)
	}
	got, e := f.service.StartExternalRequest(t.Context(), second, r.InputFingerprint)
	if e != nil || got.Status != "waiting_for_active_run" || got.RunID != "" {
		t.Fatal(got, e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded'`); e != nil {
		t.Fatal(e)
	}
	if worked, e := f.service.ProcessExternalRequest(t.Context()); e != nil || worked {
		t.Fatal("waiting request automatically launched", worked, e)
	}
	route.Enabled = false
	if _, e = f.service.SaveRequestRoute(t.Context(), route); e != nil {
		t.Fatal(e)
	}
	r, e = f.service.ExternalRequest(t.Context(), second)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.StartExternalRequest(t.Context(), second, r.InputFingerprint); e == nil {
		t.Fatal("disabled route launched")
	}
}
func TestExternalInputCannotBeForgedThroughRunReferences(t *testing.T) {
	f, route, _ := preparedRequest(t)
	task := uuid.NewString()
	refs, _ := json.Marshal(map[string]string{"external_request_id": uuid.NewString(), "linear_session_id": uuid.NewString()})
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	_, e = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,'Manual task','Manual prompt','open','{}','{"mode":"direct_completion","requirements":[]}')`, task, f.project, route.RepositoryID)
	if e != nil {
		t.Fatal(e)
	}
	r, e := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: uuid.MustParse(task), AgentID: uuid.MustParse(route.AgentID), ExternalRefs: refs})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	var links int
	if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM external_session_runs WHERE run_id=$1`, r.RunID).Scan(&links); e != nil || links != 0 {
		t.Fatal("trusted arbitrary refs", e)
	}
}

func TestExternalRequestWaitsForCompletionPolicy(t *testing.T) {
	f, route := agentRequestFixture(t)
	if _, err := f.service.SaveRequestRoute(t.Context(), route); err != nil {
		t.Fatal(err)
	}
	id := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "Plan completion first"))
	if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	waiting, err := f.service.ExternalRequest(t.Context(), id)
	if err != nil || waiting.Status != "awaiting_approval" || waiting.RunID != "" || !strings.Contains(waiting.Reason, "completion policy") || !strings.Contains(waiting.Reason, "/tasks?taskId=") {
		t.Fatal(waiting, err)
	}
	if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || worked {
		t.Fatal("unplanned request retried automatically", worked, err)
	}
	var task uuid.UUID
	if err = f.pool.QueryRow(t.Context(), `SELECT id FROM tasks WHERE external_refs->'linear'->>'issue_id'=$1`, waiting.IssueID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	requestWorkTx(t, f, func(tx pgx.Tx) error {
		return postgres.SetTaskCompletionPolicy(t.Context(), tx, task, 1, completion.Policy{Mode: "direct_completion"})
	})
	got, err := f.service.StartExternalRequest(t.Context(), id, waiting.InputFingerprint)
	if err != nil || got.Status != "queued" || got.RunID == "" {
		t.Fatal("planned request did not reactivate", got, err)
	}
}
