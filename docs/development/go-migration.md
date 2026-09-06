# Incremental Go migration

Stage 1 provides an opt-in Go worker. This is **not yet a Go-only backend**: the
worker invokes the existing Python execution module once per claimed Run. The
default Compose worker remains Python, and there are no schema, HTTP/OpenAPI,
SSE, or frontend changes. See the [decision](../adr/0001-incremental-go-backend.md).

## Current ownership

| Responsibility | Implementation in stage 1 |
| --- | --- |
| Queue polling, transactional claims, expired-owner recovery | Go `internal/postgres` and `internal/worker` |
| Executor process supervision and exit reconciliation | Go `internal/worker` |
| Execution-time heartbeats and cancellation observation | Python `RunSupervisor` |
| Docker isolation, Git worktrees, finalization, artifacts, cleanup | Existing Python execution modules |
| HTTP, OpenAPI, SSE, schema migrations | Existing Python API and Alembic |

`Queue.Acquire` commits before `Executor.Execute` can allocate anything. The
temporary executor accepts only a Run UUID, the explicit claim owner, and a
recovery flag; it never polls or claims the queue. Task text and Agent configuration
are not used as process arguments. Backend commands remain inside Run containers.

Both workers use PostgreSQL Run-row locks, the same 60-second lease, and the same
three-attempt cleanup recovery limit. Go can recover a Python claim and Python's
resource guards reject the stale owner. A recovered attempt is failed and cleaned
up, never automatically re-executed. Existing terminal decisions are preserved.

## Run locally

Use Go 1.27.1 or later on Linux/macOS, Python 3.12 or later, PostgreSQL, Git, and
Docker. Run from the repository root after following the normal setup in the
[README](../../README.md). Python dependencies are still required:

```bash
uv sync --frozen --all-packages
go build -o /tmp/circular-worker-go ./cmd/circular-worker-go
export CIRCULAR_EXECUTOR_PYTHON="$PWD/.venv/bin/python"
/tmp/circular-worker-go --check
/tmp/circular-worker-go
```

The Go CLI loads `.env`, with explicit environment variables taking precedence.
Use the existing `postgresql+psycopg://...` `DATABASE_URL`; Go translates its driver
prefix for pgx. Existing polling and execution-root settings are unchanged.
`CIRCULAR_EXECUTOR_PYTHON` is a trusted interpreter path, not a shell command. Set
distinct `CIRCULAR_WORKER_ID` values when running several workers, or leave it
unset for a generated identity. Each worker executes one Run at a time.

`--check` validates configuration, imports, and the database URL/driver without
connecting to PostgreSQL, contacting Docker, or claiming Runs. It is not a live
dependency health check.

## Opt in through Compose

```bash
docker compose -f compose.yaml -f compose.go.yaml up --build -d
```

The override replaces only the worker command and image. The Go image deliberately
contains Python, Git, and the Docker CLI until the execution migration is complete.
Keep the same host-visible execution root when switching workers.

Rollback without changing data or deleting volumes:

```bash
docker compose -f compose.yaml -f compose.go.yaml stop worker
docker compose -f compose.yaml up --build -d worker
```

SIGTERM stops new claims and gives the Python executor up to 80 seconds to settle
and clean up, within Compose's 90-second grace period. An executor crash records a
generic failure if no terminal decision exists, retaining its claim for lease-based
cleanup recovery. If the Go parent alone is killed abruptly on a local host, its
Python child may continue renewing the lease and completing cleanup. A replacement
must respect that live lease. Killing the whole worker container leaves expired
claims for the next worker to recover. Do not manually clear ownership or delete
resource roots to force a retry.

## Verification

Use a disposable PostgreSQL database. The Python suite requires an otherwise empty,
migrated database with no running queue workers; Compose browser tests retain their
records, so use a separate fresh database for the regression suite.

```bash
export TEST_DATABASE_URL=postgresql+psycopg://circular:circular@localhost:5432/circular_test
export CIRCULAR_EXECUTOR_PYTHON="$PWD/.venv/bin/python"
DATABASE_URL="$TEST_DATABASE_URL" uv run alembic upgrade head
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/circular-worker-go ./cmd/circular-worker-go
CIRCULAR_RUN_DOCKER_TESTS=1 CIRCULAR_E2E_GO_WORKER=/tmp/circular-worker-go uv run pytest -q
CIRCULAR_E2E_GO_WORKER=/tmp/circular-worker-go pnpm test:e2e
```

The Go PostgreSQL tests create a uniquely named schema per test using the actual
Alembic migrations and remove only that schema afterward. They check concurrent and
mixed-language claims, rollback, cancellation, stale-owner fencing, bounded recovery,
atomic failure events, and terminal-outcome preservation. Without `TEST_DATABASE_URL`
these checks are skipped; ordinary Go tests still run.

The Python integration suite additionally exercises the real Go binary's SIGTERM
handling and executor-crash recovery, including artifacts and resource cleanup.
These two tests require `CIRCULAR_E2E_GO_WORKER`; the crash test also requires Linux
process inspection. The existing browser scenarios run against either the local Go
binary or the Go Compose stack and verify success, cancellation, failure, SSE replay,
retained artifacts, and container removal.

## Remaining migration

1. Port the Docker runtime module with its existing ownership, isolation, process,
   output, and compensation tests. Preserve the runtime interface and fake-workload
   protocol; do not combine this with introducing a real agent backend.
2. Port Repository/worktree management, artifact persistence, and finalization with
   their symlink, containment, crash-recovery, and retention guarantees.
3. Replace the per-Run Python executor with a Go supervisor, including heartbeats,
   event ingestion, cancellation, and shielded cleanup; rerun the full parity suite
   before making Go the default worker.
4. Migrate HTTP/OpenAPI/SSE and schema tooling behind the checked-in contracts. Remove
   Python control-plane dependencies only after their replacements pass parity tests.

Eino remains a separate optional agent-backend choice. It is not added in stage 1
and must not own Circular's queue claims, resource cleanup, or Run lifecycle. No
performance or image-size improvement is claimed for this temporary dual-runtime
stage; benchmark the Go-only implementation when it exists.
