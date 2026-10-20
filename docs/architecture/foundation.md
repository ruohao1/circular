---
title: "Architectural foundation"
description: "How the control plane, worker, execution backends, and isolated workspaces fit together."
---

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
- `internal/backends`: fake and Codex invocation preparation and event decoding.
- `internal/codexworkload`, `cmd/circular-codex-workload`: isolated Codex CLI entrypoint.
- `internal/codexauth`: persistent subscription login validation and cross-process locking.
- `internal/codexlogin`, `cmd/circular-codex-auth`: dedicated containerized login, status and logout.
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

## Task ownership

A Work session owns a Task independently of its preferred Agent or a Run's worker
lease. PostgreSQL permits one unreleased session per Task, shared by external
coding clients and Circular coding Runs. PR review Runs are exempt. New work
requires a nonblank title and description, a Repository in the same Project, and
completed prerequisites. External claims require Ready; explicit native launches
allow Open or Ready. Admission records the Task/prerequisite revisions and moves
the Task to In progress atomically. Reporting an outcome or releasing ownership
does not mark the Task Done or change its planned dates.

Prerequisites are same-Project, directed Task links; edits reject duplicates,
self-links, missing references and cycles. A parent link is separate, immutable,
and does not imply a prerequisite. Active ownership protects instructions,
Repository selection and prerequisites. Ownership history fixes the Repository
even after release. Independent planning metadata remains editable.

External clients supply a session UUID and a random 256-bit secret before
claiming. Only the secret digest is persisted. Keep the original secret securely
for reconnects; it is never returned in Task, Run, session or conflict projections.
Client name/version is display metadata, not authenticated identity or approval.
The credential permits reports for that session only; user release/reassignment
is a separate privileged service operation.

External clients should report contact every 30 seconds with a monotonically
increasing sequence. Duplicate or lower sequences do not renew contact; accepted
contact never moves backward when the server clock changes. At 120 seconds
without contact the session needs attention, but still owns its Task. Heartbeats
show recent contact, not coding progress. Terminal external reports release the
claim; explicit release or atomic external reassignment fences subsequent reports
from the old credential. Revocation cannot stop an external process editing files.
The user must arrange that process's handoff separately.

Session IDs and operation IDs make uncertain responses replayable. Exact retries
return the stored receipt and current session view; changed payloads conflict.
Claim, outcome, release, reassignment and cancellation requests retain audit
receipts. Native launch keys still replay their original Run after completion; a
new key requires an eligible, unowned Task.

Native sessions record worker acquisition/renewal as contact and the Run's actual
first start time. Outcomes precede release: normal ownership ends only after the
Workspace is released and the worker lease is cleared. Cleanup heartbeats do not
extend coding contact. Expired leases show attention immediately; recovery sets a
durable hold that survives cleanup and requires explicit user release. Native
administrative release cancels active work and reports pending cleanup rather
than granting a replacement prematurely.

Admission and planning edits lock Project then Task. Native lifecycle hooks use
Run then session, without planning locks; native administrative release takes
Project, Task, Run, then session. Linear launch preserves provider/grant/route
locks before taking Project, then Agent/Repository. An owner conflict leaves the
request waiting for explicit restart; invalid preparation requires approval.

These lifecycle services live in `internal/postgres`. This foundation adds parent
and prerequisite fields to existing planning API/MCP tools and safe native launch
conflicts; external claim/heartbeat/finish MCP tools, completion policy, status
sync and ownership visualizations are separate follow-ups.

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
ingestor then commits each normalized backend event independently so polling and SSE
can expose progress before the process exits.

Backend adapters prepare the trusted invocation and decode output; claims, allocation,
terminal decisions and cleanup stay with the Supervisor. The optional Codex adapter
uses bridge networking and a bounded `/tmp` tmpfs, mounts a dedicated ChatGPT login
directory by default (or passes an explicitly configured API key through stdin),
and requires a successful turn record plus a zero exit. Fake Runs retain their original
network and mount policy. See [Codex backend](../development/codex-backend.md).

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

## Delegation lifecycle

Owner-managed `agent_delegation_targets` opt a coding Agent into one specialist
handoff. The private `delegate_task` tool saves a bounded objective, target and
request key; its `pending_handoff` response does not create work. The Supervisor
requires a clean invocation exit, exact source checkpoint and released parent
container before transactional admission. Project, Task and Run locks protect
policy, source identity and the unique parent/child relationship. A child gets a
new Task and Run, frozen input and an isolated worktree hydrated from the raw
checkpoint. Git attributes cannot rewrite these bytes. Child patches use the
checkpoint commit, while the parent's final patch uses its original base.

The parent waits without a worker lease in `waiting_for_input`; its Task ownership
and `suspended` Workspace are retained. The child Task has its immutable parent
link and its own Work session. Waiting does not fabricate parent contact.
Terminal child status alone is insufficient: only after
child cleanup and worker-lease release can the queue atomically claim parent invocation
2. The invocation fence rejects stale workers. A fresh backend invocation uses
frozen parent input and verified read-only context containing progress, child
status, report and patch. Native CLI sessions are not resumed, and child patches
are never applied by the platform. The same Run and attempt emit `run.continued`
instead of another `run.started`; it reuses the original Work session/start time.

Parent cancellation locks the parent before its child, stops unfinished child work
and prevents continuation. Independent child failure or cancellation remains an
honest result for the parent. Recovery preserves safely parked parents and cleans
abandoned terminal parents; interrupted active invocations are not replayed.
Child cleanup never locks the parent. Automatic provider queues and consumers both
exclude delegated children, preserving the root Linear session. Parent usage sums
the latest cumulative sample per invocation, excluding child usage. Private Run
reads expose linkage and invocation; SSE remains live across suspension.

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

Additional agent backends, authentication and RBAC, approval UI,
recursive delegation, Linear/GitHub/Slack adapters, LISTEN/NOTIFY wakeups,
billing, and distributed runners are not implemented. These have explicit seams
or storage fields where needed, but no speculative framework.
The first real backend is Codex with subscription and API-key authentication; a credential proxy,
outbound destination restrictions and backend-aware queue routing remain deferred.
Production event-output backpressure or durable spooling and artifact garbage collection
remain future work. Worktree archives use disk-backed streaming without a fixed size cap;
disk exhaustion or other retention errors preserve the worktree and record cleanup failure
instead of deleting unretained output. Agent containers cannot use the shared Git metadata;
supporting commits
inside real backends requires a dedicated, isolated Git-metadata design.
