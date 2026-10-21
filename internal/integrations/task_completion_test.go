package integrations_test

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
)

func TestCompletionNativeReconciliation(t *testing.T) {
	for _, scenario := range []string{"report", "missing", "failed", "recovery", "reopened", "earlier invocation", "cancelled task"} {
		t.Run(scenario, func(t *testing.T) {
			f, _, request := preparedRequest(t)
			launched, err := f.service.StartExternalRequest(t.Context(), request.ID, request.InputFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.MustParse(launched.RunID)
			var task uuid.UUID
			if err = f.pool.QueryRow(t.Context(), `SELECT task_id FROM runs WHERE id=$1`, id).Scan(&task); err != nil {
				t.Fatal(err)
			}
			if _, err = postgres.NewQueue(f.pool).Acquire(t.Context(), "completion-worker"); err != nil {
				t.Fatal(err)
			}
			resources, err := postgres.NewResources(f.pool, "completion-worker")
			if err != nil {
				t.Fatal(err)
			}
			if err = resources.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
				if _, err := r.InvocationInput(); err != nil {
					return err
				}
				if _, err := r.CreatePending(filepath.Join(t.TempDir(), id.String())); err != nil {
					return err
				}
				if _, err := r.RecordContainer("completion-fixture"); err != nil {
					return err
				}
				if _, err := r.MarkRunning(); err != nil {
					return err
				}
				if scenario != "missing" {
					if err := r.SaveCompletionReport(1, completion.Report{Result: "Native result"}); err != nil {
						return err
					}
				}
				if scenario == "failed" {
					return r.RecordFailure("Work failed", nil)
				}
				if err := r.BeginFinalizing(); err != nil {
					return err
				}
				return r.Complete()
			}); err != nil {
				t.Fatal(err)
			}
			// The process outcome alone cannot bypass worker/Workspace cleanup.
			if _, err = f.service.ProcessTaskCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			var stage string
			if err = f.pool.QueryRow(t.Context(), `SELECT status FROM tasks WHERE id=$1`, task).Scan(&stage); err != nil || stage != "in_progress" {
				t.Fatal("premature promotion", stage, err)
			}
			if scenario == "recovery" {
				if _, err = f.pool.Exec(t.Context(), `UPDATE work_sessions SET needs_user_resolution=true WHERE run_id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "earlier invocation" {
				if _, err = f.pool.Exec(t.Context(), `UPDATE runs SET invocation=2 WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				resources, err = resources.ForInvocation(2)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = resources.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
				if err := r.ReleaseWorkspace(); err != nil {
					return err
				}
				return r.ReleaseClaim()
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "reopened" || scenario == "cancelled task" {
				tx, err := f.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				var revision, board int64
				if err = tx.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, task).Scan(&revision, &board); err != nil {
					t.Fatal(err)
				}
				target := "ready"
				if scenario == "cancelled task" {
					target = "cancelled"
				}
				if _, err = postgres.MoveTask(t.Context(), tx, task, postgres.TaskMoveInput{ExpectedRevision: revision, ExpectedBoardRevision: board, Status: target, Placement: "end"}); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := integrations.New(f.pool, f.config)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			fail := make(chan error, 2)
			for _, service := range []*integrations.Service{f.service, restarted} {
				wg.Go(func() { _, err := service.ProcessTaskCompletion(t.Context()); fail <- err })
			}
			wg.Wait()
			close(fail)
			for err := range fail {
				if err != nil {
					t.Fatal(err)
				}
			}
			var decisions int
			if err = f.pool.QueryRow(t.Context(), `SELECT t.status,(SELECT count(*) FROM task_completions WHERE task_id=t.id AND applied_at IS NOT NULL) FROM tasks t WHERE id=$1`, task).Scan(&stage, &decisions); err != nil {
				t.Fatal(err)
			}
			want := "in_progress"
			if scenario == "report" {
				want = "completed"
			}
			if scenario == "reopened" {
				want = "ready"
			}
			if scenario == "cancelled task" {
				want = "cancelled"
			}
			if stage != want || scenario == "report" && decisions != 1 || scenario != "report" && decisions != 0 {
				t.Fatal("bad reconciliation", stage, decisions)
			}
			var outcome string
			if err = f.pool.QueryRow(t.Context(), `SELECT status FROM runs WHERE id=$1`, id).Scan(&outcome); err != nil || scenario != "failed" && outcome != "succeeded" {
				t.Fatal("completion changed process outcome", outcome, err)
			}
		})
	}
}
