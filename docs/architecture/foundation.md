# Architectural foundation

Circular is a modular Go backend with a React/TypeScript frontend. The API and worker
are separate processes sharing PostgreSQL, not an in-process service call. Default
Compose services, database migrations, the deterministic runner, and test fixtures are
Go-only. The [migration](../development/go-migration.md) preserves existing data and
the HTTP/JSONL/artifact contracts.

## Module map

- `cmd/circular-api`: HTTP service composition root.
- `cmd/circular-worker-go`: durable queue consumer and bounded process shutdown.
- `cmd/circular-migrate`, `internal/migrate`: embedded, transactional SQL revisions.
- `internal/httpapi`: resource creation, execution snapshots, SSE, artifact downloads.
- `internal/worker`: claim-consumer loop and configuration.
- `internal/runstate`: deterministic Run transition policy.
- `internal/execution`: Supervisor, backend JSONL ingestion, finalization, retention.
- `internal/postgres`: queue, lease-fenced resources, events, Workspace/Artifact records.
- `internal/git`: Repository cache, isolated linked worktrees, ownership receipts, diffs.
- `internal/runtimes`: hardened Docker CLI adapter and recovery.
- `internal/artifacts`: immutable content publication and safe worktree archives.
- `internal/fakeworkload`, `cmd/circular-fake-workload`: deterministic JSONL fixture,
  built as one static binary in a scratch image.
- `contracts`: authoritative OpenAPI document embedded in Go and consumed by TypeScript.
- `apps/web`: launcher and Run execution UI.
- `internal/testsupport`: disposable PostgreSQL schemas and external Go CLI fixtures.

PostgreSQL is the source of truth. A worker claims a queued run with `FOR UPDATE SKIP
LOCKED` and changes it to `provisioning` in the same transaction. State changes pass
through the Run transition policy. Events are append-only and have both a
global database sequence and a per-run sequence. SSE uses the per-run sequence as its
resume cursor. Writes spanning a Run-owned resource and its Event lock the owning Run
first, then the resource row. The storage implementation reuses that Run lock for event
sequence allocation so Workspace and Artifact writes share one deterministic lock order.

## Isolation seam

The intended execution chain is Run → container → Git worktree → backend. The
`runtimes.Docker` adapter translates the backend-neutral runtime interface into hardened,
argv-only Docker CLI operations: one Run worktree is mounted read-write at `/workspace`,
the container root is read-only, and network, capabilities, resource limits, user, and
environment policy are explicit. The adapter never mounts the Docker socket, host SSH
directory, Repository cache, Artifact root, or control-plane credentials. A claimed Run
now creates a pending Workspace, refreshes its Repository cache, provisions its linked
worktree, and starts its container before one transaction makes the Workspace `ready`
and the Run `running`. Each durable resource identity is recorded before the next
lifecycle transition so later cleanup can reconcile partial allocations. A runtime handle
keeps its adapter-local live identity separate from its immutable persisted resource ID;
the provisioner returns both the ready Workspace and the original live handle. If the
post-start identity write fails, provisioning either records that immutable ID in the
failed Workspace transaction or permanently discards the just-created allocation through
the runtime compensation boundary.

The worker passes the exact live handle returned by provisioning to runtime execution.
Before consuming output, one database read verifies that the Run is `running`, its
Workspace is `ready`, and the persisted immutable resource ID matches the handle. The
ingestor then commits each normalized fake-workload event independently so polling and SSE
can expose progress before the process exits.

`execution.Supervisor` keeps a 60-second PostgreSQL lease alive, observes cancellation, and
owns a cancellation-shielded cleanup path. Finalization captures a binary-capable Git
patch with a private index before marking a successful Run terminal. Cleanup stops and
removes the exact container, retains the worktree output, and only then releases the
owned worktree. Artifacts and their integrity metadata live outside the worktree.
Cleanup failures are separate Workspace events and never replace the Run's primary error.
The supervisor retries terminal-decision persistence before cleanup. Cleanup and claim
release require a terminal Run; failed terminal writes retain the lease and resources
for recovery instead of leaving an ownerless active Run. Production and integration
tests use the same worker execution builder, including ownership and lease fencing.

An expired owner is fenced by the Run row lock. Recovery fails an interrupted attempt
and reconciles its persisted resources; it does not automatically repeat agent execution.
At most three cleanup recovery attempts are made. A still-unreleased Workspace then
requires operator attention. A fresh Run is the explicit execution retry boundary.

The web launcher creates a Task followed by a queued Run. The execution page combines a
coherent HTTP snapshot with ordered event history and cursor-based SSE. The frontend
schema types are generated from the checked-in OpenAPI document; `pnpm contracts:check`
detects stale generated types; HTTP tests verify the Go implementation.

Repository caches, worktrees, and artifacts are derived from internal UUIDs beneath
worker-owned roots. The Go Git and runtime adapters validate containment and translate a
worker-visible worktree into the equivalent Docker-host-visible source path. Process-
specific environment parsing remains in the Go composition roots. See
[execution-directories.md](../development/execution-directories.md) for the concrete
local and Compose mappings.

## Deliberately deferred

Real agent backends, authentication and RBAC, approval UI,
recursive delegation, Linear/GitHub/Slack adapters, LISTEN/NOTIFY wakeups,
billing, and distributed runners are not implemented. These have explicit seams
or storage fields where needed, but no speculative framework.
The current Supervisor builds only the deterministic fake workload specification;
real backend execution will need an explicit backend adapter without taking over claims
or resource ownership.
Production event-output backpressure or durable spooling and artifact garbage collection
remain future work. Worktree archives use disk-backed streaming without a fixed size cap;
disk exhaustion or other retention errors preserve the worktree and record cleanup failure
instead of deleting unretained output. Agent containers cannot use the shared Git metadata;
supporting commits
inside real backends requires a dedicated, isolated Git-metadata design.
