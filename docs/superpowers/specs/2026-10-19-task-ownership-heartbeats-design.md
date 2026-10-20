# Task ownership and heartbeats

Date: 2026-10-19

Issue: [ISQ-472](https://linear.app/isqrd/issue/ISQ-472)

Branch: `feat/isq-472-task-ownership`

Status: Written spec approved on 2026-10-20. Implementation plan requires its own review.

## Intent and scope

Users primarily work through external coding clients such as Claude Code and
Codex. Circular must tell those clients who owns each Task, prevent concurrent
claims, and retain an honest record of contact and work attempts. Native coding
Runs participate in the same ownership rule. A reusable Agent configuration or
preferred Agent is not the current owner.

The approved requirements are one current owner, durable sessions, linked
subtasks, eligibility checks, heartbeats, explicit recovery of abandoned work,
auditable outcomes, and rejection of superseded owners. This spec chooses the
storage, transaction, compatibility, and credential boundaries for those rules.

This issue delivers the Go persistence/service foundation, native Run
integration, dependency fields in existing planning contracts, and tests.
External work-reporting HTTP/MCP operations belong to ISQ-474; client skills to
ISQ-476; owner/Gantt presentation and controls to ISQ-477. Completion policies
belong to ISQ-473. Linear status synchronization belongs to ISQ-478. None of
those later features is claimed complete by this work.

The spec and eventual implementation plan remain on this branch and ship with
the implementation PR. There is no separate documentation-only PR.

## Existing behavior that matters

- `internal/postgres/run_launch.go` locks a Task and deduplicates a request key,
  but currently permits distinct simultaneous coding Runs for the same Task.
- `task_planning.go` already serializes planning edits with a Project lock,
  checks revisions, and protects content used by active Runs.
- Worker leases last 60 seconds. `queue.go` recovers expired leases and fails
  the abandoned Run before cleaning its resources. This is execution recovery,
  not permission to give its Task to another coding agent.
- Delegation releases the parent's worker lease while it waits, then resumes
  the same Run. The parent still needs its Task claim during that interval.
- A terminal Run can retain a Workspace or worker lease until cleanup succeeds.
  Run outcome alone is not proof that another Run can safely start.
- Read-only PR review Runs use a separate launch path and must remain separate
  from coding ownership.
- The private API trusts its deployment boundary. MCP client name/version are
  display metadata; they are not authenticated identities or user approvals.

## Chosen approach

Persist Work sessions in PostgreSQL and represent the current Task claim by the
session whose ownership has not been released. A partial unique index on Task
ID enforces one such session. Keep lifecycle receipts separately for audit and
retry safety. There is no second mutable owner pointer to keep synchronized.

Two alternatives were considered:

| Approach | Tradeoff |
| --- | --- |
| Reuse Run worker leases | Smaller schema, but external clients have no Run, expired leases permit takeover, and delegation intentionally drops the lease. It cannot satisfy the ownership policy. |
| Separate claim table plus session history | Valid, but introduces another mutable ownership record without a requirement for one session to own multiple Tasks. |
| One live Work session per Task, with history | Chosen. The database enforces exclusivity while Run execution keeps its existing recovery model. |

## Domain and persistence

A Work session belongs to exactly one Project and Task. It is either external
or bound to one native coding Run. Native sessions retain their Run/Agent
association. External sessions retain a stable session ID and bounded client
display metadata; they do not require creating an Agent configuration.

Persist the session ID, Task/Project IDs, kind, optional Run ID, external
credential digest, immutable admission identity, admitted Task revision,
claim time, optional work start time, last accepted contact, heartbeat sequence,
reported outcome/time, and ownership release time/reason. A native recovery
flag records that loss of the execution owner needs user resolution.

Claim time and actual work start are distinct. Native start comes from the
existing first `MarkRunning` transition; delegation continuation preserves it.
An external claim declares that tracked work starts at acceptance. It is a
client declaration, not a measurement of keystrokes or CPU activity. No operation
rewrites planned dates or invents a progress percentage.

The current claim is the session with no ownership release time, even if a
native Run has already reported an outcome. Every claim, accepted outcome,
release, and reassignment has an immutable lifecycle receipt with a server
timestamp, operation ID, actor category, and reason. Reassignment links the old
session to its replacement. Receipts contain no session secret. Ordinary
heartbeats update the latest contact and sequence; they do not create an
unbounded audit row for every ping.

Use database constraints for session shape, same-project references, a unique
native Run association to the same Task, and one unreleased session per Task.
Historical Runs keep their existing records and meaning. A Work session outcome
is separate from the Task's planning status and future completion-policy verdict.

## Eligibility and subtasks

New external claims require a Task in `ready`, a nonblank title and description,
a Repository belonging to the Project, and every declared prerequisite in
`completed`. A cancelled prerequisite does not count as completed. Return
specific missing-information or blocked-dependency reasons; do not invent the
missing requirements or ask an executor to discover them after claiming.

An explicit native launch may start an `open` or `ready` Task, applying the same
information and dependency checks. Starting an `open` Task is an explicit
preparation action by the existing console/API launch or authorized Linear
request; it is not a general bypass available to an autonomous external claim.
Delegation prepares its newly created child in its existing admission
transaction. None of these paths silently reopens `in_review`, `completed`, or
`cancelled`, or starts a second attempt on an `in_progress` Task. A retry needs
the previous claim resolved and the Task explicitly made ready again.

The successful claim and move to `in_progress` commit together, using existing
Task and board revision rules. A claim conflict returns the current owner's
safe session summary. Check a valid replay before re-evaluating mutable stage
or agent settings so a lost response cannot create a second attempt. For new
attempts, check existing ownership before stage eligibility so a competitor
receives the owner rather than only an `in_progress` validation error.

Add directed Task dependency links, scoped to one Project. Replacing a Task's
prerequisites uses the existing planning update's expected revision. Reject
missing/foreign Tasks, self-dependencies, and cycles, including concurrent
opposite edges. Dependency edits and claim admission share the Project lock.
Changes are disallowed while the dependent Task has a current claim. Reopening
a prerequisite later blocks future claims; it does not silently revoke work
already admitted. Record the prerequisite IDs/revisions checked at admission.

Add an optional parent Task link at Task creation and expose it in Task reads.
The parent must already exist in the same Project; the link is immutable.
Existing native delegation sets this link for newly created children. Each
child needs its own claim. Parent links do not create blocking dependencies,
copy ownership, or automatically complete the parent. Existing delegation
history remains readable without fabricating new sessions for old Runs.

## Contact and attention

External clients should heartbeat every 30 seconds. After 120 seconds without
accepted contact, the current session shows `needs_attention`. These are fixed
initial defaults, not new user settings. Contact time is server time. Each
session has an increasing heartbeat sequence; duplicate or older sequences
return current state without advancing contact time. A finished/revoked
external session cannot renew contact.

Attention is evaluated from stored facts and a server clock, so restart does
not reset it and no timer daemon must run to retain ownership. A valid new
heartbeat from the still-current external owner clears contact attention.
Silence never releases the claim or makes the Task eligible for another agent.
The owner can reconnect and report normally; otherwise the user must release
or reassign it.

Native contact is recorded by the existing fenced worker heartbeat. Queued
work has not contacted an executor yet. A parent suspended for an admitted
delegation is explicitly waiting, not a supposedly heartbeating process.
Expose that known state without fabricating timestamps. Its Task claim remains
current while the child owns and completes its separate Task.

After a native outcome, worker lease renewal may continue for cleanup, but it
does not advance the session's coding-contact timestamp or imply resumed work.
Show the known outcome and pending cleanup. Native loss detection keeps the
existing worker lease threshold rather than applying the external 120-second
window to resource recovery.

Native lease expiration still triggers bounded execution cleanup. It also
marks the Work session as needing user resolution. A recovery worker does not
become the Task's new coding owner, and successful recovery cleanup does not
automatically release the abandoned Task claim. Exhausted or failed cleanup
retains ownership and its reason. Ordinary successful/failed execution with
no owner-loss incident may close its session after cleanup normally.

## Commands, credentials, and retries

The foundation provides Go operations to claim, inspect, heartbeat, record an
outcome, release, and reassign. It also provides native hooks for start,
contact, terminal outcome, recovery, and completed cleanup. Transport adapters
must call these operations rather than write session rows themselves.

For an external claim, the caller generates and retains a random session ID
and a 256-bit secret before sending the request. The ID is the claim's
idempotency key. Circular stores only a digest of the secret. A repeated claim
with the same ID, credential, and immutable admission fields returns that
session's receipt and current state, even after restart or later release.
Changing those fields with the same ID conflicts. A missing/wrong credential
cannot recover another session or mutate it. Do not put credentials in URLs,
owner summaries, general Task/Run projections, logs, or lifecycle receipts.
Use cryptographic randomness and constant-time credential comparison.

Subsequent owner writes bind both the credential and the exact current session
ID. They cannot use only Task ID or a client name. Each terminal operation has
an operation ID and input digest. Repeating an identical accepted operation
returns its original receipt without a new write; a different operation after
release fails. Replaying an accepted receipt never reacquires ownership or
modifies a replacement session. Native launch keeps its existing request-key
contract and binds the new session to the single resulting Run.

Session credentials authorize only that session's reports. User release or
reassignment is a separate privileged service operation with a required reason
and expected current session ID. A stale admin request must not revoke the
replacement. For an external session, revocation and any replacement claim
commit atomically; the old credential is fenced immediately. The caller must
supply the replacement's new identity/credential before that transaction.
The replacement may retain `in_progress`, but must recheck planning/dependencies
and reject a closed Task. Failed replacement validation leaves the old claim
unchanged.

The current deployment has one trusted private control boundary, not per-user
RBAC. Its owner-level clients are already administrators. Client name/version
does not prove which human or process made a request, and adding session
secrets does not remove existing administrator powers. ISQ-472 exposes no new
public endpoint. ISQ-474 must preserve the private boundary, distinguish owner
reports from administrator actions, and must not infer user approval from a
client-supplied label. Full multi-user authentication is outside this issue.

Revoking an external session prevents further Circular writes. It cannot prove
that the old process stopped editing its local checkout. Reassignment receipts
must retain that distinction.

## Native completion, cancellation, and locking

Native terminal transitions record the Work session outcome. They retain the
Task claim while the worker or Workspace still requires cleanup. The existing
`RunResources.ReleaseClaim` is the hook that can close normal native ownership
after cleanup; keep its worker-lease meaning clear in names and comments.
Cancellation before acquisition, with no worker or Workspace, can close the
session immediately. Cancellation after acquisition waits for cleanup.

Explicit release/reassignment of an active native owner uses the existing
cancellation path. It does not grant a replacement until the Run is terminal,
has no worker lease, and has no unreleased Workspace. While cleanup is pending,
return the existing owner and a cleanup-pending result. Once cleanup is proved,
the user can resolve a recovery-held claim and claim the Task for a new session.
No user command pretends to have stopped a still-running native process.

Recording an external finish in this foundation closes that work attempt and
audits its report; it does not mark the Task Done or In Review. Native Run
success also does not choose the Task's completion policy. Until ISQ-473 adds
the policy transaction, finishing/releasing preserves the current planning
stage, normally `in_progress`, preventing another automatic claim. A user may
explicitly move it back to `ready`. The later policy will combine accepted
outcome, ownership closure, and the permitted stage transition atomically.

Preserve these transaction orders:

- Admission and planning: Project, Task, then session; a new native Run is
  created within the same admission transaction.
- Native resource operations: Run, then its session. They never acquire a
  Project/Task planning lock while holding the Run lock.
- An admin command needing existing native execution state locks Project,
  Task, Run, then session. It must not lock a session and then wait for its Run.
- Delegation retains parent-before-child ordering. A child cleanup never waits
  for its parent. Native outcome/cleanup hooks do not edit planning stages.

Include caller-acquired locks in that audit. Linear request launch currently
locks Agent/Repository rows before a Task insert acquires the Project lock;
delegation takes the Project lock before Agent locks. Align those paths with
Project-before-Agent ordering when integrating common admission, and test
launch against delegation and provider configuration changes.

Dependency checks use the same serialized Project state. The unique live-session
index is a final backstop, not the only concurrency check. Native worker
acquisition must refuse to execute a coding Run without its matching current
session. PR review acquisition is exempt. Recheck native session association
under the existing Run fence; keep worker/invocation fencing intact.

Protect content and Repository association while an external claim is current,
including during attention or native cleanup. Preserve existing historical
Repository restrictions and extend them to Tasks with work history. Priority,
preferred Agent, planned dates, and ordering remain independent planning data.
Generic Task status changes never create, release, or transfer ownership.

## Compatibility and delivery boundaries

The migration is additive, but it must not guess an owner for pre-existing
overlapping Runs. Require an upgrade with writers stopped and coding work
drained: no nonterminal coding Run, no coding worker lease, and no unreleased
coding Workspace. Validate that precondition transactionally and fail with an
actionable diagnostic without changing data if it is unmet. The operator can
finish/cancel and clean up on the old version, then retry. Mixed old/new writers
are unsupported. Do not cancel Runs or delete resources during migration.

Preserve all historical Tasks, Runs, Events, schedules, and delegation records;
do not invent past ownership or heartbeat timestamps. The current schema head
is `0018`; implementation must select the next available revision when it lands.

The implementation reaches these existing seams:

| Area | Required integration |
| --- | --- |
| `internal/migrate`, `CONTEXT.md` | Schema, upgrade precondition, domain language, historical preservation tests. |
| `internal/postgres` | Shared ownership operations, dependencies, planning locks/capabilities, `CreateRun`, queue, resources, cancellation, delegation admission and continuation. |
| `internal/integrations/request_launch.go` | Native Linear launches use the shared guard; ownership conflicts become an actionable waiting state instead of endless retries. |
| `internal/httpapi`, `contracts/openapi.json` | Existing launch conflicts expose safe owner information; planning reads/updates expose dependencies; Task creation/reads carry parent links. |
| `internal/controlmcp`, generated web contracts | Preserve existing launch/planning callers and surface their new errors; no new external lifecycle tools yet. |
| Existing console capability copy | Explain an active Work session lock where it replaces an active-Run-only explanation; no new owner/Gantt screen. |
| PostgreSQL, HTTP, integration, worker, browser fixtures | Remove assumptions that concurrent coding Runs on one Task are legal. Construct historical read fixtures explicitly without bypassing production admission in behavior tests. |

Automatic provider request processing must treat ownership conflicts separately
from invalid planning and temporary provider errors. PR review launch and
historical read queries remain compatible. Use existing dependencies and the
installed Go/PostgreSQL stack; add no broker, scheduler, or new service.

## Verification and acceptance

Use a controllable internal clock for contact projection and new session
operations. Never accept client timestamps as lease/contact authority. Use
barriers and separate PostgreSQL connections for races, not sleep-based timing.

Required behavior checks:

1. Two simultaneous external claims, and an external claim versus a native
   launch, yield one owner; the loser receives its safe summary. Failure to
   commit cannot leave a Run without its session or a stage change without
   ownership. An uncertain native commit never starts an unconfirmed executor.
2. Lost first response, process restart, repeated claim, and repeated finish
   recover the same session/receipt. Changed input, wrong credentials, and
   credentials for another Task fail without exposing secrets.
3. At the 120-second boundary, attention changes while ownership remains.
   Duplicate/out-of-order heartbeats do not postpone attention. A valid new
   heartbeat clears external contact attention without claiming progress.
4. Reassignment racing heartbeat/finish is serializable. A revoked owner
   cannot renew, finish, or affect its replacement. Stale admin commands cannot
   release the replacement. Audit receipts distinguish replay from new work.
5. Missing planning, invalid stage, unmet prerequisites, foreign references,
   self-links, and cycles fail. Concurrent opposing dependency edits cannot
   admit a cycle. Claim versus content/dependency edit has one valid result.
6. Native queue, cancellation before/after acquisition, terminal cleanup,
   lease recovery, exhausted cleanup, and stale worker/invocation writes retain
   the right ownership. Recovery alone never makes abandoned work available.
7. Parent and child have separate claims; suspension retains the parent,
   continuation uses its existing session, and cancellation keeps the current
   cleanup fences. PR reviews never compete for a coding claim.
8. Migration preserves historical data and refuses active/unreleased coding
   work without partial changes. Fresh databases and repeat migration work.
9. Existing planning/schedule, native API/MCP launch, Linear request, and browser
   flows handle the intentional eligibility/conflict changes. No private
   session credential appears in a public/read-only projection.

Run the relevant Go tests with a disposable PostgreSQL database, then the
repository's Go/race, contract, frontend, and affected browser checks. Exercise
the affected real worker cleanup scenarios with the existing Docker fixtures.
Production deployment and live model calls are not part of design validation.
