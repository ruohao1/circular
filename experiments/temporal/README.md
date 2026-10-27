# Temporal managed Run evaluation

Disposable managed Run orchestration spike. Do not point it at a running Circular stack. The nested Go module does not enter the production build or change its dependencies. Tests use the existing migrations, API observation, atomic Work-session admission, Supervisor, resource fences, Git and artifact store. Each test owns one random PostgreSQL schema and one Run. The fake backend is the only admitted backend.

From the repository root, with Go 1.27.1, Git and Docker available:

```sh
docker compose -f experiments/temporal/compose.yaml up -d --wait
export TEST_DATABASE_URL=postgresql://postgres:temporal-eval@127.0.0.1:55492/temporal_eval
export TEMPORAL_ADDRESS=127.0.0.1:57233
export TEMPORAL_EVIDENCE_DIR=/tmp/circular-temporal-evidence
go -C experiments/temporal test -race -v -count=1 -timeout=240s ./...
```

The process-loss checks run on Linux. They SIGKILL a separate Activity worker process group at four boundaries for both Workflow variants, then replace it. Separate checks kill workers inside the new retention and release Activities. They advance only the disposable Run's lease timestamp to avoid a 60-second wait. PostgreSQL still performs takeover and fencing. Fault cases use the existing external Docker CLI simulator and actual fake-workload code. Exported service histories are replayed with the Go SDK.

For the additional real Docker container check, build the fake backend from this checkout:

```sh
runner_dir=$(mktemp -d /tmp/circular-temporal-runner.XXXXXX)
CGO_ENABLED=0 go build -trimpath -o "$runner_dir/runner" ./cmd/circular-fake-workload
docker build -f experiments/temporal/runner.Dockerfile -t circular-temporal-eval-runner:test "$runner_dir"
CIRCULAR_TEMPORAL_REAL_DOCKER=1 go -C experiments/temporal test -race -v -count=1 -timeout=240s ./...
```

The follow-up compares `RunWorkflow` with `SplitWorkflow`. Initial execution is identical. The latter splits cleanup recovery into `claim-cleanup`, `retain`, and `release` Activities, using a persisted Workflow timer for live lease waits. Its standalone `CleanupWorkflow` is also tested. The added claim-response-loss test discards a committed return value; it does not inject a transport failure. Read [source-comparison.json](evidence/source-comparison.json) for the measured code increase.

Tests skip PostgreSQL, Temporal service, or real-container checks when their corresponding variables are absent. The worker/helper entry points are intentionally skipped during an ordinary run. The real-container check uses the non-root host user that owns its disposable worktree.

To remove only these evaluation services and their disposable state:

```sh
docker compose -f experiments/temporal/compose.yaml down -v
```

Read the [decision and evidence report](../../docs/superpowers/reports/2026-10-26-temporal-evaluation.md) for the recommendation, Activity boundaries, operating costs and limitations. Production migration, optional review/publication, and delegation workflows are outside this spike.
