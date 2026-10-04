---
title: "Go-only backend cutover"
description: "Upgrade existing installations to the Go backend without resetting application data."
---

The incremental migration is complete for the first isolated execution slice. The API,
worker, migrations, fake runner, and backend test tooling are Go. React/TypeScript remains
the frontend. No Python interpreter, package manager, execution bridge, or fallback
image is part of the project.

## Existing deployments

1. Back up PostgreSQL and the worker-owned execution roots.
2. Stop the old worker and allow its cleanup to finish (Compose provides 90 seconds).
3. Remove `CIRCULAR_GO_EXECUTOR` and `CIRCULAR_EXECUTOR_PYTHON` from local configuration.
   Native Go is the only executor; selecting the retired fallback fails configuration.
4. Use ordinary `docker compose up --build`. The separate Go override was retired.
   The migration service runs before the API and worker.

Do **not** delete volumes, databases, execution roots, ownership receipts, or retained
artifacts during this upgrade. Existing queued Runs remain claimable; interrupted claims
become eligible for cleanup-only recovery when their leases expire. Recovery never repeats
agent execution. Create a fresh Run to retry execution.

## Preserved contracts

- PostgreSQL tables, UUIDs, queue ordering, 60-second leases, and three recovery attempts.
- Transactional Run locking for lifecycle writes, event sequences, allocation fences,
  Workspace handoff, artifact persistence, and cleanup-gated claim release.
- HTTP routes and checked-in OpenAPI, execution snapshots, cursor-based SSE, and artifact
  download ownership/integrity checks.
- Fake workload version-1 JSONL, strict validation, raw backend diagnostics, and exit codes.
- Deterministic worktree branches, cross-process locks, device/inode ownership receipts,
  immutable artifact URIs, and the existing PAX archive framing.

The Go migrator embeds revisions `0001` through `0008`. It deliberately retains the
historical `alembic_version` **table name** so old databases are recognized in place.
That is persisted-format compatibility, not an Alembic/Python dependency. Fresh creation,
upgrades from revisions `0001`/`0002`/`0003`/`0004`/`0005`/`0006`/`0007`, and repeated revision-`0008` startup are supported. Unknown or
multiple heads fail closed. DDL and ledger writes share one transaction and a per-schema
advisory lock; there is no automatic downgrade or reset.

Revision `0003` adds [provider authorization](integrations.md) and unique imported
resource identities while retaining existing Integration metadata and domain records.
Revision `0004` adds provider app registration from the console and preserves existing
connection credentials and pending authorizations.
Revision `0005` adds the supplied Repository discovery Agent to existing Projects
and a stable preset identity, while preserving custom Agents, Tasks, Runs, and
provider connections. New Projects receive that Agent atomically when created.
Revision `0006` adds durable Agent proposals and their review status without
changing existing Agents or creating Tasks or Runs.
Revision `0007` adds a model recommendation explanation to proposals. Existing
proposal identities, review status, Agent settings, and instructions are preserved.
Revision `0008` adds optional, Task-scoped Run request keys for retry-safe launches
from [external MCP clients](control-mcp.md). Existing Runs receive a null key;
their IDs, attempts, state and output remain unchanged.

`DATABASE_URL` should use `postgresql://`. The old `postgresql+psycopg://` prefix is
accepted for existing configurations and normalized without rewriting URL values.
API creation requests are limited to 16 MiB; validation errors do not echo input values.

## Verification and rollback

See [Local development](local-development.md#verification) for the Go, frontend, Docker, and browser
commands. PostgreSQL tests migrate their own unique schemas; CLI fault tests execute a
Go helper process. Historical cross-language comparators were replaced with stable wire
expectations and public-interface behavior checks after the compatibility checks passed.
The real-process tests cover normal completion, injected failure, two workers, scoped
cancellation, SIGTERM cleanup, and SIGKILL followed by cleanup-only replacement.

A configuration-only Python fallback no longer exists. If rollback is necessary, stop
Go workers and deploy a previously built application revision against a compatible
database/artifact backup. Never run old and new workers casually against production to
test rollback, and never reset data as a migration shortcut.

## Still outside this slice

Additional agent backends and optional Eino orchestration, GitHub publishing, Linear
result updates, Slack integration,
authentication/RBAC, approval UI, recursive delegation, distributed runners, durable
output spooling, and artifact garbage collection remain separate work.

The subsequent [Codex backend](codex-backend.md) adds the first opt-in real agent
adapter without changing the migration or recovery contracts above.
