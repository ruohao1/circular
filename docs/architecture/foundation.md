# Architectural foundation

The control plane is a modular monorepo with three deployable processes: a React web
application, a FastAPI application, and an asynchronous Python worker. The API and
worker compose the same domain packages but do not call one another in-process.

## Package map

- `domain`: dependency-light entities, identifiers, and domain vocabulary.
- `orchestration`: the deterministic Run lifecycle and transition policy.
- `events`: normalized event names and backend-neutral event envelopes.
- `agents`: backend capability contract plus the deterministic fake adapter.
- `fake-agent-workload`: isolated deterministic process fixture for the future container
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

Runner-side event ingestion can consume and persist fake-workload output, but the worker
still retains the in-process fake backend after provisioning as a temporary compatibility
path. Composing those two completed boundaries is the next integration step.

Repository caches, worktrees, and artifacts are derived from internal UUIDs beneath
worker-owned roots. The `runners` path module validates containment and translates a
worker-visible worktree into the equivalent Docker-host-visible source path. Process-
specific environment parsing remains in `apps/worker`. See
[execution-directories.md](../development/execution-directories.md) for the concrete
local and Compose mappings.

## Deliberately deferred

Real agent backends, container-event worker composition, authentication and RBAC, approval UI,
recursive delegation, Linear/GitHub adapters, LISTEN/NOTIFY wakeups, generated frontend
contracts, billing, and distributed runners are not implemented. The fake workload image
now exercises provisioning, and its output adapter is implemented, but the two are not yet
connected in worker composition. These have explicit seams or storage fields where needed,
but no speculative framework.
Cross-process container recovery and retained-container cleanup also remain worker
lifecycle work. Active-cancellation coordination, including the executor preflight
read-then-act race, remains in ISQ-175. Compose Docker CLI/socket access, runner-image
composition, and writable worktree ownership for the container UID remain in ISQ-176.
The fake-workload spec factory stays injected behind the generic spec-factory port; a
separate production factory can replace it when real backend execution is introduced.
Production output backpressure or durable spooling also remains worker lifecycle work.
