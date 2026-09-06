# Architectural foundation

The control plane is a modular monorepo with three deployable processes: a React web
application, a FastAPI application, and an asynchronous Python worker. The API and
worker compose the same domain packages but do not call one another in-process.

An [incremental Go migration](../development/go-migration.md) now provides an opt-in
worker. Go owns its queue claims, expired-owner recovery, and executor process
supervision; a temporary Python per-Run executor retains the resource lifecycle
described below. The default deployment and HTTP contracts are unchanged.

## Package map

- `domain`: dependency-light entities, identifiers, and domain vocabulary.
- `orchestration`: the deterministic Run lifecycle and transition policy.
- `events`: normalized event names and backend-neutral event envelopes.
- `agents`: backend capability contract plus the deterministic fake adapter.
- `fake-agent-workload`: isolated deterministic process fixture for the container
  execution path; it is not another control-plane process.
- `storage`: SQLAlchemy mappings, repositories, event persistence, and queue claiming.
- `runners`: execution coordination across backend, workspace, and event seams, including
  safe managed execution-path derivation.
- `runtimes`: container-runtime interface and per-run container specification.
- `git`: the local Repository checkout cache and worktree-provisioning interface.
- `integrations`: reserved adapters for Linear and GitHub; domain code never imports them.
- `apps/api`: HTTP/OpenAPI/SSE composition root.
- `apps/worker`: durable queue consumer composition root.

PostgreSQL is the source of truth. A worker claims a queued run with `FOR UPDATE SKIP
LOCKED` and changes it to `provisioning` in the same transaction. State changes pass
through the orchestration transition policy. Events are append-only and have both a
global database sequence and a per-run sequence. SSE uses the per-run sequence as its
resume cursor. Writes spanning a Run-owned resource and its Event lock the owning Run
first, then the resource row. The storage implementation reuses that Run lock for event
sequence allocation so Workspace and Artifact writes share one deterministic lock order.

## Isolation seam

The intended execution chain is Run → container → Git worktree → backend. The
`DockerRuntime` adapter translates the backend-neutral runtime interface into hardened,
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

`RunSupervisor` keeps a 60-second PostgreSQL lease alive, observes cancellation, and
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
schema types are generated from FastAPI's OpenAPI document; `pnpm contracts:check`
detects stale checked-in contracts.

Repository caches, worktrees, and artifacts are derived from internal UUIDs beneath
worker-owned roots. The `runners` path module validates containment and translates a
worker-visible worktree into the equivalent Docker-host-visible source path. Process-
specific environment parsing remains in `apps/worker`. See
[execution-directories.md](../development/execution-directories.md) for the concrete
local and Compose mappings.

## Deliberately deferred

Real agent backends, authentication and RBAC, approval UI,
recursive delegation, Linear/GitHub adapters, LISTEN/NOTIFY wakeups,
billing, and distributed runners are not implemented. These have explicit seams
or storage fields where needed, but no speculative framework.
The fake-workload spec factory stays injected behind the generic spec-factory port; a
separate production factory can replace it when real backend execution is introduced.
Production event-output backpressure or durable spooling and artifact garbage collection
remain future work. Worktree archives use disk-backed streaming without a fixed size cap;
disk exhaustion or other retention errors preserve the worktree and record cleanup failure
instead of deleting unretained output. Agent containers cannot use the shared Git metadata;
supporting commits
inside real backends requires a dedicated, isolated Git-metadata design.
