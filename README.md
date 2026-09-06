# Circular

Circular is a self-hosted control plane for coding agents. It coordinates projects,
specialized agents, tasks, execution attempts, isolated workspaces, approvals, events,
and integrations while leaving reasoning loops to replaceable execution backends.

## Quick start

```bash
cp .env.example .env
docker compose up --build
```

- Web: http://localhost:5173
- API and OpenAPI: http://localhost:8000 and http://localhost:8000/docs
- PostgreSQL: localhost:5432

Register a Project, a Repository (an HTTPS clone URL accessible to the worker), and an
enabled fake Agent using `/docs`. Then open the web launcher, enter a task, and start a Run.
The Run page streams output and exposes its timeline, final diff, retained artifacts, usage,
and cancellation. Try Agent `backend_config` values `{"delay_ms": 1000}` or
`{"failure": "after_first_event"}` to exercise progress and failure.

The Docker socket is mounted into the **trusted worker only**. Each agent gets only its
own worktree, runs non-root with network disabled, and is removed after completion.
This initial self-hosted slice has no authentication; keep it on a trusted local network.

For local development without Compose:

```bash
uv sync --all-packages
uv run alembic upgrade head
uv run pytest
corepack enable
pnpm install
pnpm --filter @circular/web dev
uv run circular-api
uv run circular-worker
```

Before starting a local worker, build its runner image:

```bash
docker build -f infra/fake-agent-workload.Dockerfile -t circular-runner:dev .
```

A local repository path is also
accepted when it is accessible to that worker and Git's ownership checks pass.

An opt-in Go worker is available as the first stage of the backend migration. It
currently delegates per-Run execution to Python; the default stack is unchanged.
See [Go migration](docs/development/go-migration.md) for setup, rollback, verification,
and the remaining work.

## Verification

Use a disposable migrated PostgreSQL database, with no unrelated workers attached:

```bash
export TEST_DATABASE_URL=postgresql+psycopg://circular:circular@localhost:5432/circular
DATABASE_URL="$TEST_DATABASE_URL" uv run alembic upgrade head
CIRCULAR_RUN_DOCKER_TESTS=1 uv run pytest -q
pnpm contracts:check
pnpm typecheck
pnpm test
pnpm build
pnpm exec playwright install chromium
pnpm test:e2e
```

The browser suite starts a disposable local API/worker/Vite stack. It creates and launches
tasks through the UI, reconnects during streaming, and verifies success, cancellation,
failure, artifact retention, reload/replay, and container cleanup. It removes its uniquely
named database fixtures and temporary directories on shutdown. Docker tests additionally
exercise concurrent workers, isolated mounts, cleanup retries, and expired-owner fencing.
Without the database/Docker environment flags, those integration tests are skipped.

To exercise the same browser scenarios against an already-running **disposable** Compose
stack, set `CIRCULAR_E2E_COMPOSE=1` and `CIRCULAR_EXECUTION_HOST_ROOT` to the absolute host
root used by that stack, then run `pnpm test:e2e`. This mode leaves its test records in that
stack's database for inspection; it never resets the database.

After changing an API schema, run `pnpm contracts:generate` and commit both
`contracts/openapi.json` and `apps/web/src/generated/api.ts`.

See [docs/architecture/foundation.md](docs/architecture/foundation.md) for the package
map and intentionally deferred work. Worker-owned filesystem roots and their local and
Compose mappings are documented in
[docs/development/execution-directories.md](docs/development/execution-directories.md).
The deterministic process and image used to test the container execution path are
documented in
[docs/development/fake-agent-workload.md](docs/development/fake-agent-workload.md).
