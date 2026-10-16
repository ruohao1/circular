package postgres_test

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
	"path/filepath"
	"strings"
	"testing"
)

type admissionFixture struct {
	supervisorFixture
	store                   *postgres.Resources
	parent, project, target uuid.UUID
	request                 delegations.Request
	checkpoint              delegations.Checkpoint
}

func delegationAdmissionFixture(t *testing.T) admissionFixture {
	t.Helper()
	f := admissionFixture{supervisorFixture: newSupervisorFixture(t, nil)}
	if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&f.parent, &f.project); err != nil {
		t.Fatal(err)
	}
	f.target = uuid.New()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Specialist','fake','Review carefully','{}',true)`, f.target, f.project); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.SetDelegationTargets(t.Context(), tx, f.parent, []uuid.UUID{f.target}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	acquire(t, postgres.NewQueue(f.pool), "delegation-owner")
	f.store, err = postgres.NewResources(f.pool, "delegation-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
		if _, err := r.InvocationInput(); err != nil {
			return err
		}
		if _, err := r.CreatePending(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); err != nil {
			return err
		}
		if _, err := r.RecordContainer("exited-container"); err != nil {
			return err
		}
		_, err := r.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.request = delegations.Request{AgentID: f.target, RequestKey: strings.Repeat("界", 200), Objective: delegations.DelegationObjective{Title: "Inspect", Description: "Find mistakes."}}
	f.checkpoint = delegations.Checkpoint{BaseCommit: strings.Repeat("a", 40), BaseRef: "main", SourceCommit: strings.Repeat("b", 40)}
	if err = f.pool.QueryRow(t.Context(), `SELECT input_sha256 FROM run_invocations WHERE run_id=$1 AND number=1`, f.id).Scan(&f.checkpoint.InputSHA256); err != nil {
		t.Fatal(err)
	}
	source, _ := artifacts.URI(f.id, "delegation-source.tar")
	progress, _ := artifacts.URI(f.id, "delegation-progress.json")
	f.checkpoint.Source = artifacts.Content{URI: source, SizeBytes: 1024, SHA256: strings.Repeat("c", 64)}
	f.checkpoint.Progress = artifacts.Content{URI: progress, SizeBytes: 2, SHA256: strings.Repeat("d", 64)}
	return f
}

func TestDelegationAdmissionReplayAndConflict(t *testing.T) {
	f := delegationAdmissionFixture(t)
	type answer struct {
		record delegations.Record
		err    error
	}
	answers := make(chan answer, 8)
	for range 8 {
		go func() {
			record, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
			answers <- answer{record, err}
		}()
	}
	var first delegations.Record
	for range 8 {
		got := <-answers
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.ID == uuid.Nil {
			first = got.record
		} else if first.ID != got.record.ID {
			t.Fatal("duplicate admission")
		}
	}
	if first.ChildRunID == nil || first.ChildTaskID == nil || first.Status != "waiting" {
		t.Fatal(first)
	}
	changed := f.request
	changed.Objective.Description = "changed"
	if _, err := f.store.SuspendForDelegation(t.Context(), f.id, changed, f.checkpoint); !errors.Is(err, postgres.ErrDelegationConflict) {
		t.Fatal("changed replay accepted", err)
	}
	changed = f.request
	changed.RequestKey = "other"
	if _, err := f.store.SuspendForDelegation(t.Context(), f.id, changed, f.checkpoint); !errors.Is(err, postgres.ErrDelegationConflict) {
		t.Fatal("second child accepted", err)
	}
	var tasks, runs int
	var status, workspace string
	var owner, container *string
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM tasks),(SELECT count(*) FROM runs),r.status,r.worker_id,w.status,w.container_id FROM runs r JOIN workspaces w ON w.run_id=r.id WHERE r.id=$1`, f.id).Scan(&tasks, &runs, &status, &owner, &workspace, &container); err != nil || tasks != 2 || runs != 2 || status != "waiting_for_input" || owner != nil || workspace != "suspended" || container != nil {
		t.Fatal(tasks, runs, status, owner, workspace, container, err)
	}
	var input []byte
	var key string
	var parent uuid.UUID
	if err := f.pool.QueryRow(t.Context(), `SELECT i.input,r.request_key,r.parent_run_id FROM runs r JOIN run_invocations i ON i.run_id=r.id WHERE r.id=$1`, *first.ChildRunID).Scan(&input, &key, &parent); err != nil || key != first.ID.String() || parent != f.id {
		t.Fatal(key, parent, err)
	}
	var frozen postgres.ProvisioningContext
	if err := json.Unmarshal(input, &frozen); err != nil || frozen.TaskTitle != "Inspect" || frozen.Instructions != "Review carefully" || frozen.BaseRef != f.checkpoint.SourceCommit || len(frozen.DelegationTargets) != 0 {
		t.Fatal(frozen, err)
	}
	if err := postgres.NewQueue(f.pool).ReconcileExit(t.Context(), f.id, "delegation-owner"); err != nil {
		t.Fatal(err)
	}
	var completed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE run_id=$1 AND type IN ('run.completed','run.failed')`, f.id).Scan(&completed); err != nil || completed != 0 {
		t.Fatal("handoff finalized parent", completed, err)
	}
}

func TestDelegationAdmissionPolicyRace(t *testing.T) {
	f := delegationAdmissionFixture(t)
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = postgres.SetDelegationTargets(t.Context(), tx, f.parent, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint)
		result <- err
	}()
	// Task insertion uses the same Project lock as policy changes and admission.
	if _, err = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Concurrent','','open','{}')`, uuid.New(), f.project); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err == nil {
		t.Fatal("revocation lost race")
	}
	var children int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&children); err != nil || children != 0 {
		t.Fatal(children, err)
	}
}

func TestDelegationAdmissionRejectsInvalidScope(t *testing.T) {
	for _, kind := range []string{"disabled", "foreign", "review", "nested", "missing-repository", "invocation", "checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			f := delegationAdmissionFixture(t)
			var err error
			switch kind {
			case "disabled":
				_, err = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, f.target)
			case "foreign":
				other := seed(t, f.pool, 1)[0]
				err = f.pool.QueryRow(t.Context(), `SELECT agent_id FROM runs WHERE id=$1`, other).Scan(&f.request.AgentID)
			case "review":
				snapshot := testsupport.SeedPRReviewSource(t, f.pool, f.project)
				review, e := postgres.NewPRReviewStore(f.pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snapshot, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}})
				if e != nil {
					t.Fatal(e)
				}
				f.id = review.RunID
				f.parent = snapshot.Reviewer.AgentID
				acquire(t, postgres.NewQueue(f.pool), "delegation-owner")
				tx, e := f.pool.Begin(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				if e = postgres.SetDelegationTargets(t.Context(), tx, f.parent, []uuid.UUID{f.target}); e != nil {
					t.Fatal(e)
				}
				if e = tx.Commit(t.Context()); e != nil {
					t.Fatal(e)
				}
				err = f.store.WithRun(t.Context(), f.id, func(r *postgres.RunResources) error {
					if _, e := r.InvocationInput(); e != nil {
						return e
					}
					if _, e := r.CreatePending(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); e != nil {
						return e
					}
					if _, e := r.RecordContainer("exited-review"); e != nil {
						return e
					}
					_, e := r.MarkRunning()
					return e
				})
				f.checkpoint.Source.URI, _ = artifacts.URI(f.id, "delegation-source.tar")
				f.checkpoint.Progress.URI, _ = artifacts.URI(f.id, "delegation-progress.json")
				f.checkpoint.BaseRef = snapshot.PR.HeadSHA
				if e = f.pool.QueryRow(t.Context(), `SELECT input_sha256 FROM run_invocations WHERE run_id=$1`, f.id).Scan(&f.checkpoint.InputSHA256); e != nil {
					t.Fatal(e)
				}
			case "nested":
				other := seed(t, f.pool, 1)[0]
				_, err = f.pool.Exec(t.Context(), `INSERT INTO delegations(id,parent_run_id,target_agent_id,child_run_id,request_key,input_fingerprint,objective_json,objective,depth,status) VALUES($1,$2,$3,$4,'nested',$5,'{"title":"Prior","description":"Prior"}','Prior',1,'waiting')`, uuid.New(), other, f.parent, f.id, strings.Repeat("a", 64))
			case "missing-repository":
				_, err = f.pool.Exec(t.Context(), `UPDATE tasks SET repository_id=NULL WHERE id=(SELECT task_id FROM runs WHERE id=$1)`, f.id)
			case "invocation":
				_, err = f.pool.Exec(t.Context(), `UPDATE runs SET invocation=2 WHERE id=$1`, f.id)
			case "checkpoint":
				f.checkpoint.Source.URI = "artifact://" + uuid.NewString() + "/delegation-source.tar"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.SuspendForDelegation(t.Context(), f.id, f.request, f.checkpoint); err == nil {
				t.Fatal("invalid admission accepted")
			}
			var children int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&children); err != nil || children != 0 {
				t.Fatal(children, err)
			}
		})
	}
}
