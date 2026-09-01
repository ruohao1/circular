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

See [docs/architecture/foundation.md](docs/architecture/foundation.md) for the package
map and intentionally deferred work.
