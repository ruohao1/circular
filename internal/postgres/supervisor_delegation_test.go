package postgres_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"os"
	"path/filepath"
	"testing"
)

func TestDelegationSuspensionKeepsWorkspace(t *testing.T) {
	for _, exit := range []int{0, 23} {
		t.Run(string(rune('a'+exit)), func(t *testing.T) {
			target := uuid.New()
			request := delegations.Request{AgentID: target, RequestKey: "inspect", Objective: delegations.DelegationObjective{Title: "Inspect", Description: "Find mistakes."}}
			raw, _ := json.Marshal(map[string]any{"type": "circular.delegation.requested", "request": request})
			f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(codexMessage + codexUsage + string(raw) + "\n")}}, exit)
			var parent, project uuid.UUID
			if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&parent, &project); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Reviewer','codex','Review','{}',true)`, target, project); err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = postgres.SetDelegationTargets(t.Context(), tx, parent, []uuid.UUID{target}); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			claim := acquire(t, postgres.NewQueue(f.pool), "handoff-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "handoff-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			err = supervisor.Execute(t.Context(), *claim, "handoff-owner")
			snapshot := testsupport.Observe(t, f.pool, f.id)
			var children int
			if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&children); e != nil {
				t.Fatal(e)
			}
			if exit != 0 {
				if err == nil || children != 0 || snapshot.Run.Status != "failed" {
					t.Fatal(err, children, snapshot.Run)
				}
				return
			}
			if err != nil || children != 1 || snapshot.Run.Status != "waiting_for_input" || snapshot.Run.WorkerID != nil || snapshot.Workspace.Status != "suspended" || snapshot.Workspace.ContainerID != nil {
				t.Fatal(err, children, snapshot)
			}
			if _, err = os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String())); err != nil {
				t.Fatal("lost parent source", err)
			}
			if snapshot.Count("run.completed") != 0 || snapshot.Count("run.failed") != 0 || len(snapshot.Artifacts) != 0 {
				t.Fatal("handoff finalized parent", snapshot)
			}
			if err = postgres.NewQueue(f.pool).ReconcileExit(t.Context(), f.id, "handoff-owner"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDelegationContinuationVerifiesFrozenInputsAndWorkspace(t *testing.T) {
	for _, tamper := range []string{"none", "source", "git", "owner"} {
		t.Run(tamper, func(t *testing.T) {
			target := uuid.New()
			request := delegations.Request{AgentID: target, RequestKey: "inspect", Objective: delegations.DelegationObjective{Title: "Inspect", Description: "Find mistakes."}}
			raw, _ := json.Marshal(map[string]any{"type": "circular.delegation.requested", "request": request})
			f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(codexMessage + codexUsage + string(raw) + "\n")}}, 0)
			var parent, project uuid.UUID
			if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&parent, &project); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Reviewer','codex','Review','{}',true)`, target, project); err != nil {
				t.Fatal(err)
			}
			tx, _ := f.pool.Begin(t.Context())
			if err := postgres.SetDelegationTargets(t.Context(), tx, parent, []uuid.UUID{target}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			q := postgres.NewQueue(f.pool)
			s, err := execution.NewSupervisor(f.pool, "continuation-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Execute(t.Context(), *acquire(t, q, "continuation-owner"), "continuation-owner"); err != nil {
				t.Fatal(err)
			}
			optionsPath := filepath.Join(f.base, "docker", "docker-options.json")
			optionsRaw, err := os.ReadFile(optionsPath)
			if err != nil {
				t.Fatal(err)
			}
			var options map[string]string
			if err = json.Unmarshal(optionsRaw, &options); err != nil {
				t.Fatal(err)
			}
			script, _ := json.Marshal(protocolScript{Chunks: []protocolChunk{{"stdout", []byte(codexMessage + codexUsage)}}})
			if err = os.WriteFile(options["output_script"], script, 0600); err != nil {
				t.Fatal(err)
			}
			child := acquire(t, q, "continuation-owner")
			if child.RunID == f.id {
				t.Fatal("parent resumed before child")
			}
			if err = s.Execute(t.Context(), *child, "continuation-owner"); err != nil {
				t.Fatal("child", err)
			}
			if _, err = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=false,instructions='Changed',backend_config='{"not_valid":"setting"}' WHERE id=$1`, parent); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.config.Git.WorktreeRoot, f.id.String())
			if tamper == "source" {
				if err = os.WriteFile(filepath.Join(path, "README.md"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tamper == "git" {
				if err = os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /tmp/untrusted\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tamper == "owner" {
				if err = os.Remove(filepath.Join(f.config.Git.WorktreeRoot, "."+f.id.String()+".owner")); err != nil {
					t.Fatal(err)
				}
			}
			// A fresh supervisor must reconstruct continuation entirely from durable state.
			s, err = execution.NewSupervisor(f.pool, "resume-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			claim := acquire(t, q, "resume-owner")
			if !claim.Continuation || claim.RunID != f.id {
				t.Fatal(claim)
			}
			err = s.Execute(t.Context(), *claim, "resume-owner")
			got := testsupport.Observe(t, f.pool, f.id)
			if tamper != "none" {
				if err == nil || got.Run.Status != "failed" || got.Count("run.continued") != 0 {
					t.Fatal("tamper executed", err, got.Run)
				}
				return
			}
			if err != nil || got.Run.Status != "succeeded" || got.Run.WorkerID != nil || got.Count("run.started") != 1 || got.Count("run.continued") != 1 {
				t.Fatal(err, got.Run, got.Events)
			}
			if _, err = os.Lstat(filepath.Join(f.config.Git.WorktreeRoot, ".delegations", f.id.String())); !os.IsNotExist(err) {
				t.Fatal("context survived cleanup", err)
			}
			var result delegations.Result
			if err = f.pool.QueryRow(t.Context(), `SELECT result FROM delegations WHERE parent_run_id=$1`, f.id).Scan(&result); err != nil || result.Report == nil || result.Patch == nil {
				t.Fatal("missing child evidence", result, err)
			}
		})
	}
}

func delegationExecutionFixture(t *testing.T) supervisorFixture {
	t.Helper()
	target := uuid.New()
	request := delegations.Request{AgentID: target, RequestKey: "inspect", Objective: delegations.DelegationObjective{Title: "Inspect", Description: "Find mistakes."}}
	raw, _ := json.Marshal(map[string]any{"type": "circular.delegation.requested", "request": request})
	f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(codexMessage + codexUsage + string(raw) + "\n")}}, 0)
	var parent, project uuid.UUID
	if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&parent, &project); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Reviewer','codex','Review','{}',true)`, target, project); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = postgres.SetDelegationTargets(t.Context(), tx, parent, []uuid.UUID{target}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func delegationDockerOption(t *testing.T, f supervisorFixture, name string, value any) {
	t.Helper()
	path := filepath.Join(f.base, "docker", "docker-options.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var options map[string]any
	if err = json.Unmarshal(raw, &options); err != nil {
		t.Fatal(err)
	}
	if name == "output" {
		script, _ := json.Marshal(protocolScript{Chunks: []protocolChunk{{"stdout", []byte(value.(string))}}})
		if err = os.WriteFile(options["output_script"].(string), script, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	options[name] = value
	raw, _ = json.Marshal(options)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationRecoveryExecutionFaults(t *testing.T) {
	for _, stage := range []string{"checkpoint-publication", "parent-removal", "admission-commit", "child-terminal", "child-cleanup", "continuation-input", "continuation-allocation"} {
		t.Run(stage, func(t *testing.T) {
			f := delegationExecutionFixture(t)
			q := postgres.NewQueue(f.pool)
			s, err := execution.NewSupervisor(f.pool, "fault-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "checkpoint-publication" {
				if err = os.MkdirAll(filepath.Join(f.config.ArtifactRoot, f.id.String(), "delegation-source.tar"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "parent-removal" {
				delegationDockerOption(t, f, "remove_fails", true)
			}
			if stage == "admission-commit" {
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT fault_suspension CHECK(type<>'run.suspended')`); err != nil {
					t.Fatal(err)
				}
			}
			err = s.Execute(t.Context(), *acquire(t, q, "fault-owner"), "fault-owner")
			if stage == "checkpoint-publication" || stage == "parent-removal" || stage == "admission-commit" {
				if err == nil {
					t.Fatal("handoff fault accepted")
				}
				var children int
				if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&children); e != nil || children != 0 {
					t.Fatal(children, e)
				}
				if stage == "parent-removal" {
					delegationDockerOption(t, f, "remove_fails", false)
					expire(t, f.pool, f.id)
					claim := acquire(t, q, "fault-owner")
					if claim == nil || !claim.Recovery {
						t.Fatal(claim)
					}
					if e := s.Execute(t.Context(), *claim, "fault-owner"); e != nil {
						t.Fatal(e)
					}
				}
				got := testsupport.Observe(t, f.pool, f.id)
				if got.Run.Status != "failed" || got.Run.WorkerID != nil || got.Workspace.Status != "released" || got.Count("run.completed") != 0 {
					t.Fatal(got.Run, got.Workspace)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			delegationDockerOption(t, f, "output", codexMessage+codexUsage)
			child := acquire(t, q, "fault-owner")
			if child == nil || child.RunID == f.id {
				t.Fatal(child)
			}
			if stage == "child-terminal" {
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT fault_completed CHECK(type<>'run.completed')`); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "child-cleanup" {
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT fault_release CHECK(type<>'workspace.released')`); err != nil {
					t.Fatal(err)
				}
			}
			err = s.Execute(t.Context(), *child, "fault-owner")
			if stage == "child-terminal" || stage == "child-cleanup" {
				if err == nil {
					t.Fatal("child fault accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if stage == "child-terminal" {
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT fault_completed`); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "child-cleanup" {
				if c := acquire(t, q, "waiting"); c != nil {
					t.Fatal("continued before cleanup", c)
				}
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT fault_release`); err != nil {
					t.Fatal(err)
				}
				expire(t, f.pool, child.RunID)
				claim := acquire(t, q, "fault-owner")
				if claim == nil || !claim.Recovery || claim.RunID != child.RunID {
					t.Fatal(claim)
				}
				if err = s.Execute(t.Context(), *claim, "fault-owner"); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "continuation-input" {
				if _, err = f.pool.Exec(t.Context(), `ALTER TABLE run_invocations ADD CONSTRAINT fault_input CHECK(number<>2)`); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "continuation-allocation" {
				delegationDockerOption(t, f, "create_returns_failure", true)
			}
			claim := acquire(t, q, "fault-owner")
			if claim == nil || !claim.Continuation || claim.RunID != f.id {
				t.Fatal(claim)
			}
			err = s.Execute(t.Context(), *claim, "fault-owner")
			failed := stage == "continuation-input" || stage == "continuation-allocation"
			if (err != nil) != failed {
				t.Fatal(err)
			}
			got := testsupport.Observe(t, f.pool, f.id)
			if got.Run.WorkerID != nil || got.Workspace.Status != "released" || (got.Run.Status == "failed") != failed {
				t.Fatal(got.Run, got.Workspace)
			}
			var count int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE parent_run_id=$1`, f.id).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicated child", count, err)
			}
		})
	}
}
