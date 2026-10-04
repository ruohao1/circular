package integrations_test

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"sync"
	"testing"
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
	return f, saved, request
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
	_, e = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($1,$2,$3,'Manual task','Manual prompt','open','{}')`, task, f.project, route.RepositoryID)
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
