package integrations_test

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
)

func linkDelegationChild(t *testing.T, pool *pgxpool.Pool, child string) string {
	t.Helper()
	parent, agent, task := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2::uuid,a.project_id,$2::text,a.backend,'Parent','{}',true FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, child, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) SELECT $2,t.project_id,t.repository_id,'Parent','','open','{}' FROM tasks t JOIN runs r ON r.task_id=t.id WHERE r.id=$1`, child, task); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','waiting_for_input',1,'{}')`, parent, task, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO delegations(id,parent_run_id,target_agent_id,child_task_id,child_run_id,request_key,input_fingerprint,objective_json,objective,depth,status) SELECT $1,$2,agent_id,task_id,id,'publish',$4,'{"title":"Inspect","description":"Inspect"}','Inspect',1,'waiting' FROM runs WHERE id=$3`, uuid.New(), parent, child, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestDelegationPublicationRootOnly(t *testing.T) {
	t.Run("triggers", func(t *testing.T) {
		f := newDeliveryFixture(t, false)
		parent := linkDelegationChild(t, f.pool, f.run)
		if _, err := f.service.SetGitHubRunDeliverySettings(t.Context(), f.project, true); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `DELETE FROM linear_run_updates WHERE run_id=$1`, f.run); err != nil {
			t.Fatal(err)
		}
		transition(t, f.fixture, f.run, "running")
		transition(t, f.fixture, f.run, "succeeded")
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM github_run_deliveries WHERE run_id=$1)+(SELECT count(*) FROM linear_run_updates WHERE run_id=$1)`, f.run).Scan(&count); err != nil || count != 0 {
			t.Fatal("child automatic publication", count, err)
		}
		transition(t, f.fixture, parent, "succeeded")
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM github_run_deliveries WHERE run_id=$1`, parent).Scan(&count); err != nil || count != 1 {
			t.Fatal("root publication lost", count, err)
		}
	})
	t.Run("already-queued", func(t *testing.T) {
		f := newDeliveryFixture(t, false)
		if _, err := f.service.SetGitHubRunDeliverySettings(t.Context(), f.project, true); err != nil {
			t.Fatal(err)
		}
		f.queue(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE github_run_deliveries SET automatic=true WHERE run_id=$1`, f.run); err != nil {
			t.Fatal(err)
		}
		linkDelegationChild(t, f.pool, f.run)
		f.process(t)
		if f.remote.writes != 0 {
			t.Fatal("queued child published to GitHub", f.remote.writes)
		}
		for range 4 {
			if _, err := f.service.ProcessLinearUpdate(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if f.provider.CommentCreates.Load() != 0 {
			t.Fatal("queued child published to Linear")
		}
	})
	t.Run("manual-publication", func(t *testing.T) {
		f := newDeliveryFixture(t, false)
		linkDelegationChild(t, f.pool, f.run)
		f.queue(t)
		f.process(t)
		got, err := f.service.GitHubRunDelivery(t.Context(), f.run)
		if err != nil || got.Status != "delivered" {
			t.Fatal("manual publication changed", got, err)
		}
	})
}

func TestDelegationNativeSessionRemainsRoot(t *testing.T) {
	f, _, request := preparedRequest(t)
	root, err := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	var rootRun string
	if err = f.pool.QueryRow(t.Context(), `SELECT run_id FROM external_requests WHERE id=$1`, root.ID).Scan(&rootRun); err != nil {
		t.Fatal(err)
	}
	child := linearRun(t, f)
	if _, err = f.pool.Exec(t.Context(), `INSERT INTO delegations(id,parent_run_id,target_agent_id,child_task_id,child_run_id,request_key,input_fingerprint,objective_json,objective,depth,status) SELECT $1,$2,agent_id,task_id,id,'native',$4,'{"title":"Inspect","description":"Inspect"}','Inspect',1,'waiting' FROM runs WHERE id=$3`, uuid.New(), rootRun, child, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	// Even a stale session association must not grant child publication.
	if _, err = f.pool.Exec(t.Context(), `INSERT INTO external_session_runs(run_id,request_id) VALUES($1,$2)`, child, request.ID); err != nil {
		t.Fatal(err)
	}
	transition(t, f, rootRun, "running")
	drainActivities(t, f)
	transition(t, f, rootRun, "waiting_for_input")
	transition(t, f, child, "succeeded")
	drainActivities(t, f)
	var childActivities int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM linear_agent_activities WHERE semantic_key LIKE $1`, "run:"+child+":%").Scan(&childActivities); err != nil || childActivities != 0 {
		t.Fatal("native child activity", childActivities, err)
	}
	detail, err := f.service.ExternalRequest(t.Context(), request.ID)
	if err != nil || detail.Status != "running" || detail.RunID != rootRun {
		t.Fatal(detail, err)
	}
	for _, a := range f.provider.LinearActivities() {
		if strings.Contains(string(a.Content), `"type":"response"`) {
			t.Fatal("final during handoff", string(a.Content))
		}
	}
	// A preexisting candidate is checked again by the delivery consumer.
	if _, err = f.pool.Exec(t.Context(), `INSERT INTO linear_agent_activities(id,request_id,identity_id,session_id,actor_id,semantic_key) SELECT gen_random_uuid(),q.id,q.identity_id,q.session_id,i.actor_id,$2 FROM external_requests q JOIN provider_identities i ON i.id=q.identity_id WHERE q.id=$1`, request.ID, "run:"+child+":failed"); err != nil {
		t.Fatal(err)
	}
	drainActivities(t, f)
	for _, a := range f.provider.LinearActivities() {
		if strings.Contains(string(a.Content), "PR review") {
			t.Fatal("queued child activity published")
		}
	}
	transition(t, f, rootRun, "running")
	transition(t, f, rootRun, "succeeded")
	drainActivities(t, f)
	detail, err = f.service.ExternalRequest(t.Context(), request.ID)
	if err != nil || detail.Status != "succeeded" || detail.RunID != rootRun {
		t.Fatal("lost root session", detail, err)
	}
}
