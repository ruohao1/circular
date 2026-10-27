package temporaleval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/postgres"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// Init refuses ordinary databases. No production process imports this module.
func Init(ctx context.Context, pool *pgxpool.Pool) error {
	var schema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	if !strings.HasPrefix(schema, "circular_go_test_") {
		return errors.New("evaluation requires a disposable testsupport schema")
	}
	// shortcut: one Run per disposable schema, replace queue-wide acquisition before any wider evaluation.
	_, err := pool.Exec(ctx, `CREATE TABLE temporal_eval_admissions (
  run_id uuid PRIMARY KEY REFERENCES runs(id), singleton boolean UNIQUE NOT NULL DEFAULT true CHECK(singleton),
  dispatched boolean NOT NULL DEFAULT false, execution_started boolean NOT NULL DEFAULT false)`)
	return err
}

// Admit commits the native Work session, Run and outbox identity together.
func Admit(ctx context.Context, pool *pgxpool.Pool, in postgres.RunLaunchInput) (uuid.UUID, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	run, err := postgres.CreateRun(ctx, tx, in)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO temporal_eval_admissions(run_id) VALUES($1) ON CONFLICT(run_id) DO NOTHING`, run.RunID); err != nil {
		return uuid.Nil, err
	}
	return run.RunID, tx.Commit(ctx)
}

func WorkflowID(id uuid.UUID) string { return "circular-run/" + id.String() }

// Dispatch can repeat after an uncertain response; it never allocates a fresh identity.
func Dispatch(ctx context.Context, pool *pgxpool.Pool, c client.Client, queue string) (client.WorkflowRun, error) {
	var id uuid.UUID
	var dispatched, started bool
	if err := pool.QueryRow(ctx, `SELECT run_id,dispatched,execution_started FROM temporal_eval_admissions`).Scan(&id, &dispatched, &started); err != nil {
		return nil, err
	}
	if dispatched {
		return c.GetWorkflow(ctx, WorkflowID(id), ""), nil
	}
	var run client.WorkflowRun
	// A durable receipt must still stop execution after Temporal's history retention expires.
	if started {
		if err := verifyWorkflow(ctx, c, id); err != nil {
			return nil, err
		}
		run = c.GetWorkflow(ctx, WorkflowID(id), "")
	} else {
		var err error
		run, err = c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID: WorkflowID(id), TaskQueue: queue,
			WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
			WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowExecutionErrorWhenAlreadyStarted: true,
			Memo:                                     map[string]any{"circular_run_id": id.String()},
		}, RunWorkflow, id)
		var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &duplicate) {
			if err = verifyWorkflow(ctx, c, id); err != nil {
				return nil, err
			}
			run = c.GetWorkflow(ctx, WorkflowID(id), "")
		} else if err != nil {
			return nil, err
		}
	}
	_, err := pool.Exec(ctx, `UPDATE temporal_eval_admissions SET dispatched=true WHERE run_id=$1`, id)
	return run, err
}

func verifyWorkflow(ctx context.Context, c client.Client, id uuid.UUID) error {
	existing, err := c.DescribeWorkflowExecution(ctx, WorkflowID(id), "")
	if err != nil {
		return err
	}
	info := existing.WorkflowExecutionInfo
	var recorded string
	if info == nil || info.Type.GetName() != "RunWorkflow" || info.Memo == nil {
		return errors.New("existing Workflow identity conflicts with admission")
	}
	if err = converter.GetDefaultDataConverter().FromPayload(info.Memo.Fields["circular_run_id"], &recorded); err != nil {
		return err
	}
	if recorded != id.String() {
		return fmt.Errorf("existing Workflow belongs to another Run")
	}
	return nil
}
