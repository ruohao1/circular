package postgres_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
)

const completionRecord = `{"type":"circular.task.completion.reported","report":{"result":"Verified fixture","evidence":[]}}` + "\n"

func TestCompletionInvocationFrozenAndLegacyOmitted(t *testing.T) {
	raw, err := json.Marshal(postgres.ProvisioningContext{})
	if err != nil || strings.Contains(string(raw), "completion_contract") {
		t.Fatal("legacy invocation serialization changed", string(raw), err)
	}
	pool := database(t)
	run := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "completion-input")
	store, _ := postgres.NewResources(pool, "completion-input")
	if err = store.WithRun(t.Context(), run, func(r *postgres.RunResources) error {
		input, err := r.InvocationInput()
		if err != nil {
			return err
		}
		if input.CompletionContract == nil || input.CompletionContract.Policy.Mode != "direct_completion" {
			t.Fatal("frozen contract omitted", input)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionSupervisorReportAndProcessOutcome(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			exit := 0
			if failure {
				exit = 23
			}
			f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(completionRecord + codexMessage + codexUsage)}}, exit)
			q := postgres.NewQueue(f.pool)
			s, _ := execution.NewSupervisor(f.pool, "completion-worker", f.config)
			err := s.Execute(t.Context(), *acquire(t, q, "completion-worker"), "completion-worker")
			if (err != nil) != failure {
				t.Fatal(err)
			}
			var reports int
			if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM native_completion_reports WHERE run_id=$1 AND invocation=1`, f.id).Scan(&reports); err != nil || reports != 1 {
				t.Fatal(reports, err)
			}
			stage := reconcileCompletionFixture(t, f)
			if (stage == "completed") == failure {
				t.Fatal("process outcome ignored", stage)
			}
		})
	}
}

func reconcileCompletionFixture(t *testing.T, f supervisorFixture) string {
	t.Helper()
	var session uuid.UUID
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM work_sessions WHERE run_id=$1`, f.id).Scan(&session); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = postgres.ReconcileTaskCompletion(t.Context(), tx, session, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var stage string
	if err = f.pool.QueryRow(t.Context(), `SELECT t.status FROM tasks t JOIN runs r ON r.task_id=t.id WHERE r.id=$1`, f.id).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	return stage
}

func TestCompletionDelegationUsesFinalInvocation(t *testing.T) {
	for _, finalReport := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "reported"}[finalReport], func(t *testing.T) {
			target := uuid.New()
			request := delegations.Request{AgentID: target, RequestKey: "inspect", Objective: delegations.DelegationObjective{Title: "Inspect", Description: "Check source"}}
			raw, _ := json.Marshal(map[string]any{"type": "circular.delegation.requested", "request": request})
			f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(completionRecord + codexMessage + codexUsage + string(raw) + "\n")}}, 0)
			var parent, project uuid.UUID
			if err := f.pool.QueryRow(t.Context(), `SELECT a.id,a.project_id FROM agents a JOIN runs r ON r.agent_id=a.id WHERE r.id=$1`, f.id).Scan(&parent, &project); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Inspector','codex','Inspect','{}',true)`, target, project); err != nil {
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
			s, _ := execution.NewSupervisor(f.pool, "completion-handoff", f.config)
			if err := s.Execute(t.Context(), *acquire(t, q, "completion-handoff"), "completion-handoff"); err != nil {
				t.Fatal(err)
			}
			if stage := reconcileCompletionFixture(t, f); stage == "completed" {
				t.Fatal("handoff completed parent")
			}
			optionsRaw, err := os.ReadFile(filepath.Join(f.base, "docker", "docker-options.json"))
			if err != nil {
				t.Fatal(err)
			}
			var options map[string]string
			if err = json.Unmarshal(optionsRaw, &options); err != nil {
				t.Fatal(err)
			}
			setOutput := func(output string) {
				script, _ := json.Marshal(protocolScript{Chunks: []protocolChunk{{"stdout", []byte(output)}}})
				if err := os.WriteFile(options["output_script"], script, 0600); err != nil {
					t.Fatal(err)
				}
			}
			setOutput(codexMessage + codexUsage)
			child := acquire(t, q, "completion-handoff")
			if child.RunID == f.id {
				t.Fatal("parent resumed before child")
			}
			if err = s.Execute(t.Context(), *child, "completion-handoff"); err != nil {
				t.Fatal(err)
			}
			if finalReport {
				setOutput(completionRecord + codexMessage + codexUsage)
			}
			continued := acquire(t, q, "completion-handoff")
			if continued == nil || continued.RunID != f.id {
				t.Fatal("parent did not continue")
			}
			if err = s.Execute(t.Context(), *continued, "completion-handoff"); err != nil {
				t.Fatal(err)
			}
			if stage := reconcileCompletionFixture(t, f); (stage == "completed") != finalReport {
				t.Fatal("wrong invocation report", stage)
			}
		})
	}
}
