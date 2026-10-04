# Circular

Circular is a self-hosted control plane for coding agents. It coordinates Projects,
Agents, Tasks, Runs, isolated Workspaces, Events, and retained Artifacts while leaving
reasoning loops to replaceable execution backends.

The backend is Go; the frontend is React/TypeScript. Python is not required to build,
run, migrate, or test the project.

## Quick start

```bash
cp .env.example .env
docker compose up --build
```

- Web: http://localhost:5173
- Wiki: http://localhost:5173/docs
- API documentation: http://localhost:8000/docs
- PostgreSQL: localhost:5432

Open **Setup** in the console to create a Project and add a Repository accessible
to the worker. Each Project includes a **Repository discovery** Agent: open
**Agents → Explore repository** to prepare an analysis of the codebase, its
priorities, and useful specialist Agents. It uses the configured Codex connection.
For a simulated task, create a custom Agent with backend **Test (fake)**,
then select **Go to Runs** and start a Run. Its page streams output and shows the
timeline, final diff, artifacts, usage, and cancellation. See the
[console setup guide](docs/development/console-setup.md) for the form fields and
project selection. The API reference remains available at `http://localhost:8000/docs`; fake Agent
`backend_config` supports `{"delay_ms":1000}` and `{"failure":"after_first_event"}`.

**Setup → Integrations** connects a GitHub App to select private repositories and
Linear OAuth to import issues as Tasks. The Connect buttons guide one-time app
registration in your browser, then authorize each connection in the console.
See the [integration setup guide](docs/development/integrations.md). These
connections are separate from the Codex login.

**Setup → MCP** connects an external coding agent to Circular using a ready-made
URL or one Codex command. The MCP server runs with the API; full-control and
read-only URLs are available. It uses your existing Circular connections.
See the [user guide](docs/user-guide/coding-agent.md), or the
[MCP reference](docs/development/control-mcp.md) for protocol and stdio alternatives.

**Docs** opens the Fumadocs user guide, with console walkthroughs, full-text
search, and troubleshooting. Published content lives in `docs/user-guide/`;
developer references remain in the other `docs/` folders. See
[editing the wiki](docs/development/local-development.md#editing-the-wiki).

Only the trusted worker receives the Docker socket. Each Run container receives its own
worktree, runs non-root, and is removed after cleanup. Fake Runs have networking disabled.
The resource API has no user authentication. The native API and Compose's API
port bind to the same computer by default. Changing `--listen` or
`CIRCULAR_API_BIND_HOST` exposes that API and its MCP tools to the chosen network.

For real coding tasks, enable the optional [Codex backend](docs/development/codex-backend.md).
It uses a dedicated ChatGPT subscription login by default; API-key billing is an explicit alternative.
It uses a dedicated provider credential, network access and ephemeral temporary storage;
the existing launcher, event stream, diff, artifacts and cancellation work for both backends.

## Local development

Use Go 1.27.1+, Node.js with Corepack/pnpm, Git, PostgreSQL, and Docker.
Copy `.env.example` to `.env` and configure `DATABASE_URL` for your development database.

```bash
corepack pnpm install --frozen-lockfile
go run ./cmd/circular-migrate
docker build -f infra/fake-agent-workload.Dockerfile -t circular-runner:dev .
```

Start these in separate terminals:

```bash
go run ./cmd/circular-api
go run ./cmd/circular-worker-go
corepack pnpm dev
```

Local Repository paths are accepted when accessible to the worker and permitted by Git's
ownership checks. `go run ./cmd/circular-worker-go --check` validates configuration without
claiming Runs or connecting to PostgreSQL/Docker; it is not a service health check.

The console uses shadcn/ui components and a shared dark theme. See
[console components](docs/development/ui-components.md) to add controls or update
the theme consistently. The [brand guide](docs/brand.md) records the approved
Orbit logo, color palette, typography, and voice.

## Verification

GitHub Actions runs the [Go-only CI workflow](.github/workflows/ci.yml) on pull requests
and pushes to `main`: full Go/PostgreSQL/Docker tests, frontend/contract checks, and
browser scenarios against both local and Compose deployments. No repository secrets
are needed. See [CI and local reproduction](docs/development/ci.md) for details.

Fast checks (database and Docker scenarios skip unless explicitly enabled):

```bash
go test -race ./...
go vet ./...
go build ./...
corepack pnpm contracts:check
corepack pnpm typecheck
corepack pnpm test
corepack pnpm build
```

For complete integration coverage, point at a disposable PostgreSQL database:

```bash
export TEST_DATABASE_URL=postgresql://circular:circular@localhost:5432/circular_test
CIRCULAR_RUN_DOCKER_TESTS=1 go test -race ./... -count=1 -timeout=300s
corepack pnpm exec playwright install chromium
corepack pnpm test:e2e
```

Each database test creates and removes its own random schema using the production Go
migrations. Browser tests build the Go stack and runner image, use another isolated schema,
and remove only their owned schema/directories on clean shutdown. Unfinished resources
remain available for recovery. Tests cover real worker SIGTERM/SIGKILL, cleanup-only lease
recovery, concurrent cancellation, isolation, artifact downloads, and SSE replay.

To test an already-running **disposable** Compose stack, set `CIRCULAR_E2E_COMPOSE=1`
and `CIRCULAR_EXECUTION_HOST_ROOT` to that stack's absolute host root, then run
`corepack pnpm test:e2e`. This mode leaves its records for inspection; it never resets
the database.

## Contracts and upgrades

`contracts/openapi.json` is the authoritative HTTP contract. Go embeds and serves that
document, and TypeScript is generated from it. After a contract change, run
`corepack pnpm contracts:generate` and commit both the contract and generated client.
HTTP integration tests verify the implementation, not just the generated types.

Existing databases and retained artifacts remain compatible. Migrations are forward-only
and retain the historical version ledger; no data reset is required. See
[Go migration and cutover](docs/development/go-migration.md).

See the [architecture](docs/architecture/foundation.md),
[execution directories](docs/development/execution-directories.md),
[Docker runtime](docs/development/docker-runtime.md), and
[fake workload protocol](docs/development/fake-agent-workload.md) for implementation boundaries.
GitHub draft pull requests, dedicated PR review agents, and Linear progress and
result delivery are available through the configured app identities. Additional
agent backends, Eino, and Slack remain future work.
