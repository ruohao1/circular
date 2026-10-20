# Task Ownership and Heartbeats Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give external coding clients and native coding Runs one durable, exclusive Task owner, with honest contact history and safe recovery.

**Architecture:** PostgreSQL Work sessions hold ownership; a partial unique index permits one unreleased session per Task. Existing Project/Task planning locks serialize admission and dependency changes; native execution retains Run/worker fencing and appends session facts without taking planning locks. External session credentials authorize reports, while privileged release and reassignment remain separate operations.

**Tech Stack:** Existing Go 1.27.1, pgx/v5, PostgreSQL 17, Go MCP SDK, OpenAPI, React/TypeScript, Vitest, and Playwright. No new production dependency.

**Spec:** [Approved design](../specs/2026-10-19-task-ownership-heartbeats-design.md), approved 2026-10-20; [ISQ-472](https://linear.app/isqrd/issue/ISQ-472).

## Global Constraints

- One unreleased Work session per Task; a saved/preferred Agent is not its owner.
- External clients should heartbeat every 30 seconds; attention starts at 120 seconds without accepted contact. Neither threshold is a new setting.
- Missing contact never releases ownership. Native worker lease duration remains 60 seconds; recovery cleanup does not release a recovery-held Task claim.
- External admission requires `ready`; explicit native admission permits `open` or `ready`. Both require nonblank title/description, a same-project Repository, and completed prerequisites.
- A successful initial claim moves the Task to `in_progress` atomically. Outcome/release preserves its planning stage; completion policy is ISQ-473.
- Parent ownership survives delegation; each child owns a distinct Task. PR reviews do not acquire coding claims.
- Credential secrets are 256 bits, stored only as digests, never returned in safe projections or logs. MCP client metadata is not authenticated identity or user approval.
- Migration requires drained coding work and stopped writers. Preserve history; never cancel work, delete resources, or fabricate historical contact during migration.
- Preserve planned dates, current private deployment access, and existing worker/invocation fences. No public lifecycle endpoint, Gantt feature, client skill, Linear status sync, or new service in this issue.
- Keep this plan and spec on `feat/isq-472-task-ownership` with the implementation PR. Do not create a standalone plan PR or deploy from this plan.

## Review Focus

- A reused session ID targeting a different Task must fail without changing either Task, even when claims race across Projects: Task 3.
- Replaying an old admin action after its replacement has finished must return history, never revive or revoke another owner: Task 4.
- A backward server-clock step must not move accepted contact backward or let a duplicate heartbeat extend its deadline: Task 3.
- An older API silently ignoring `parent_task_id` must be detected before MCP creates an incorrectly unlinked Task: Task 7.
- An unowned/mismatched coding Run must not start or prevent a valid coding/PR review Run from being acquired: Task 6.

---

## Working context and file boundaries

Worktree: `/home/ubuntu/projects/circular/.worktrees/isq-472-task-ownership`.
Design commit: `3f1a304`; implementation base: `a40613f`. The main checkout has
unrelated changes; leave it untouched. Before implementation, refresh remote
state and check for overlapping migration/contract changes without discarding
this branch's documents. The next migration is `0019.sql` on this base; renumber
the new migration and its tests together if another migration lands first.

| File or existing seam | Responsibility |
| --- | --- |
| `internal/migrate/0019.sql`, `migrate.go`, `work_sessions_test.go` | Upgrade guard, Task links, session/receipt tables and constraints. |
| `internal/postgres/work_sessions.go` | Safe session types/reads, credential checks, contact projection. |
| `internal/postgres/work_claims.go` | Shared admission, external claim/replay and heartbeat. |
| `internal/postgres/work_reports.go` | Finish, privileged release/reassignment, immutable receipts. |
| `internal/postgres/work_run_hooks.go` | Native start/contact/outcome/recovery/cleanup hooks. |
| `internal/postgres/task_dependencies.go`, `task_planning.go` | Prerequisite graph checks and planning capabilities/revisions. |
| `run_launch.go`, `queue.go`, `resources.go`, `execution.go`, `workspaces.go`, `run_cancel.go`, `delegation_*.go` under `internal/postgres/` | Integrate the shared owner with existing native execution. |
| Existing HTTP/MCP planning and launch handlers, `contracts/openapi.json` | Parent/dependency fields, safe conflict responses, existing tool compatibility. |
| Existing task detail/launcher UI and fixtures | Accurate lock/error text and required launch inputs; no ownership screen. |

The new session files remain in `internal/postgres`, following its transactional
functions. Do not add a parallel repository layer or a generic workflow engine.
New functions accept a trusted `now time.Time` argument for deterministic tests;
production callers supply server UTC time, never a request timestamp. Each
caller commits or rolls back the supplied transaction. Heartbeats lock only the
session; native hooks lock Run then session and never acquire planning locks.

Before Task 1, establish Go 1.27.1+, Node.js 22/Corepack, and a disposable
PostgreSQL test database using `docs/development/local-development.md`. No Go
binary was on PATH during planning. The optional workload runner does not
replace the full race/Docker verification environment. Run baseline Go tests
with `TEST_DATABASE_URL` set, plus contract/type checks, and record any existing
failures before edits. Do not aim test or cleanup commands at the live stack.

## Task 1: Add durable ownership storage and a safe upgrade

**Files:** Create `internal/migrate/0019.sql`, `internal/migrate/work_sessions_test.go`. Modify `internal/migrate/migrate.go` and historical migration fixtures only where they incorrectly represent unfinished work as history.

**Interfaces:** Consumes existing `migrate.Up(context.Context, *pgxpool.Pool) error` and `legacyDatabase(t, version)`. Produces:

- `tasks.parent_task_id`: nullable, same-project, immutable after creation, non-self parent. Add the required composite Task key.
- `task_dependencies(project_id, task_id, prerequisite_task_id)`: unique directed edge, same-project foreign keys, no self-edge.
- `work_sessions`: ID, Project/Task IDs, `kind` (`external`/`native`), optional unique Run ID, credential/admission digests, client name/version, admitted revision/prerequisite snapshot, claimed/start/contact/finish/release times, heartbeat sequence, terminal outcome, release reason, and `needs_user_resolution`.
- `work_session_receipts`: ID, session ID, operation ID, kind, input digest, actor (`external`/`worker`/`user`/`system`), reason, timestamp, optional replacement ID. Unique `(session_id, operation_id)`; application code inserts receipts and never updates them. Native admission is a system action, not a worker heartbeat; existing Run Events retain the exact native cancellation source.

- [x] **Step 1: Add failing migration tests.** `TestWorkSessionMigrationPreservesHistory` upgrades revision 18 twice, compares prior Task/Run/Event/schedule/delegation values, and expects zero invented sessions. `TestWorkSessionMigrationRequiresDrainedCoding` covers queued/running coding, terminal coding with a worker, and terminal coding with an unreleased Workspace. Each rejection leaves the schema ledger at 18 and all original rows intact. A terminal/released coding history and PR-review-only rows are allowed.

  ```go
  if err := migrate.Up(t.Context(), pool); err == nil {
      t.Fatal("upgrade accepted undrained coding work")
  }
  if version != "0018" || sessionsTableExists {
      t.Fatal("rejected migration partially applied")
  }
  ```

  `TestWorkSessionConstraints` attempts two current sessions, a foreign-project Task, a native Run belonging to another Task, invalid external/native credential shapes, and an outcome without its timestamp. Reject each; allow a second session after explicit release.

- [x] **Step 2: Confirm red.** With `TEST_DATABASE_URL` pointing to a disposable database, run `go test ./internal/migrate -run 'TestWorkSession' -count=1 -v`. Expected: new assertions fail against revision 18, not a database skip.
- [x] **Step 3: Add migration and register its head.** Serialize the precondition with writes to Runs/Workspaces inside the existing migration transaction. Reject undrained coding work with a diagnostic identifying the required drain/cleanup. Add the tables, scope/shape checks, native Run/Task binding, and partial unique index `work_sessions(task_id) WHERE released_at IS NULL`. Require `secret_digest` only for external sessions and enforce digest/sequence/timestamp shapes. Add parent immutability validation; leave existing parent links null and preserve existing delegation history.
- [x] **Step 4: Confirm green.** Run `go test ./internal/migrate -count=1 -v`. Expected: all migrations pass with database coverage, including rollback of the new precondition. Run `git diff --check`.
- [x] **Step 5: Commit.** Stage only this task's migration and tests; commit `feat: persist task work ownership and guard upgrades`.

## Task 2: Make prerequisites and subtask links part of planning

**Files:** Create `internal/postgres/task_dependencies.go`, `internal/postgres/task_dependencies_test.go`, `internal/httpapi/task_dependencies_test.go`. Modify `internal/postgres/task_planning.go`, `internal/httpapi/api.go`, `task_planning.go`, `task_planning_read.go`, `contracts/openapi.json`, `apps/web/src/generated/api.ts`, and typed Task/planning fixtures.

**Interfaces:** Retain existing planning function signatures. Add `PrerequisitesSet bool` and `PrerequisiteTaskIDs []uuid.UUID` to `TaskPlanningPatch`. Produce `TaskPrerequisites(ctx context.Context, tx pgx.Tx, taskID uuid.UUID) ([]uuid.UUID, error)` and private `replaceTaskPrerequisites(ctx context.Context, tx pgx.Tx, task planningTask, ids []uuid.UUID) (bool, error)`; its caller already holds Project/Task locks. Add `DependenciesEditable bool`/`DependenciesLockReason *string` to `TaskEditCapabilities`.

- [x] **Step 1: Add failing graph and contract tests.** `TestTaskPrerequisitesValidateAndSerialize` covers missing/foreign IDs, self-links, repeated IDs, direct/transitive cycles, and two opposing edges raced on separate connections. `TestTaskPrerequisitePatchSemantics` covers omission (unchanged), `[]` (clear), null (422), normalized order/no-op revisions, stale revisions (409), and combined content/dependency edits (one revision/board increment). A current session forbids a changed dependency set. `TestTaskParentCreateAndImmutability` covers same-project parent creation/read, foreign/missing parent rejection, and PATCH rejection of parent changes.

  ```go
  if acceptedEdges != 1 || cycleExists {
      t.Fatal("concurrent opposite edges admitted a cycle")
  }
  if afterRevision != beforeRevision || afterBoard != beforeBoard {
      t.Fatal("equivalent prerequisite set changed revisions")
  }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/postgres ./internal/httpapi -run 'TestTask(Prerequisite|Parent)' -count=1 -v`. Expected: missing helpers/fields or unmet new assertions.
- [x] **Step 3: Implement through existing planning transactions.** Normalize dependency order, validate references and recursive reachability under the Project lock, and reuse existing change/revision handling. Unchanged sets are no-ops. Add nullable `parent_task_id` to `TaskCreate`, `TaskRead`, and physical-column Task summaries; validate it before insert. Add `prerequisite_task_ids` to the planning PATCH and planning detail response, plus dependency capabilities. Keep the derived array out of generic `TaskRead` column projection. Protect ownership-sensitive edits using the session table; keep source-managed content rules, dates, priority, preferred Agent, and ordering semantics. Repository history recognizes sessions as well as Runs, with `has_work_sessions` where no Run history exists.
- [x] **Step 4: Confirm green.** Run `go test ./internal/postgres ./internal/httpapi -run 'TestTask' -count=1 -v`, `corepack pnpm contracts:generate`, `corepack pnpm contracts:check`, and `corepack pnpm typecheck`. Expected: passing graph/planning/schedule regressions and synchronized generated types. Update typed fixtures with empty prerequisite lists/null parent fields where required; do not weaken types.
- [x] **Step 5: Commit.** Commit the listed implementation, contract, and fixture files as `feat: validate task prerequisites and subtask links`.

## Task 3: Claim external work and record trustworthy contact

**Files:** Create `internal/postgres/work_sessions.go`, `work_claims.go`, `work_sessions_test.go`. Extend `task_planning_test.go` for ownership-sensitive edits.

**Interfaces:** Use these shared types and signatures; JSON-safe views must not contain credentials or input digests.

```go
type ExternalWorkClaim struct {
    SessionID, TaskID uuid.UUID
    ExpectedRevision int64
    ClientName, ClientVersion string
    Secret []byte `json:"-"`
}
type WorkCredential struct { SessionID uuid.UUID; Secret []byte `json:"-"` }
type WorkSessionView struct {
    ID, ProjectID, TaskID uuid.UUID
    RunID, AgentID *uuid.UUID
    Kind, ClientName, ClientVersion string
    ClaimedAt time.Time
    StartedAt, LastContactAt, FinishedAt, ReleasedAt *time.Time
    HeartbeatSequence int64
    Outcome *runstate.Status
    ReleaseReason string
    NeedsUserResolution bool
    ContactState string
}
type WorkClaimResult struct { Session WorkSessionView; Created bool }
type WorkOwnershipConflict struct { Owner WorkSessionView }
func (e *WorkOwnershipConflict) Error() string
func ClaimExternalWork(context.Context, pgx.Tx, ExternalWorkClaim, time.Time) (WorkClaimResult, error)
func HeartbeatWork(context.Context, pgx.Tx, WorkCredential, int64, time.Time) (WorkSessionView, error)
func CurrentTaskWork(context.Context, pgx.Tx, uuid.UUID, time.Time) (*WorkSessionView, error)
func InspectWorkSession(context.Context, pgx.Tx, uuid.UUID, time.Time) (WorkSessionView, error)
```

Give safe view fields their snake_case JSON names. `ContactState` is derived;
use `queued`, `current`,
`needs_attention`, `waiting_for_child`, `cleanup_pending`, or `released` as
appropriate; native projection is completed in Task 6. Keep digest-bearing row
types private. Define `ErrWorkInput`, `ErrWorkCredential`, `ErrWorkReplayConflict`,
`ErrWorkRevoked`, and typed `WorkIneligibleError{Reasons []string, Blockers []uuid.UUID}`.
Implement `Error() string` on the ineligibility error. The ownership error's
bounded detail includes client name and session UUID, so existing HTTP/MCP
error consumers can identify the owner without a new error transport.

- [x] **Step 1: Add failing admission/contact tests.** Use `planningSeed`, a ready Task, a fixed UTC clock, and a client-generated 32-byte secret. `TestExternalWorkClaimsSerialize` races two claims and expects one session, one claim receipt, one stage/revision change, and the winner in the loser's conflict. `TestExternalWorkReplayAfterRestart` opens a new connection and repeats the exact request after stage/settings change. Changed identity/input, wrong credentials, and the same ID against another Task must fail without side effects. Test concurrent reuse across Projects as well.

  `TestWorkHeartbeatDeadlineAndSequence` checks 119 seconds, exactly 120 seconds, duplicate/lower sequence, valid reconnect, and backward clock movement. `TestExternalWorkEligibilityAndEditRace` covers all stages, whitespace-only requirements, missing/foreign Repository, unmet/cancelled prerequisites, stale selected revision, and a claim racing a content/dependency edit. Rollback leaves neither ownership nor an `in_progress` stage change.

  ```go
  if replay.Created || replay.Session.ID != first.Session.ID { t.Fatal("claim replay forked") }
  if at119.ContactState != "current" || at120.ContactState != "needs_attention" { t.Fatal("deadline") }
  if !duplicate.LastContactAt.Equal(*first.Session.LastContactAt) { t.Fatal("replay renewed contact") }
  if movedBack.LastContactAt.Before(*accepted.LastContactAt) { t.Fatal("clock moved contact backward") }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/postgres -run 'Test(ExternalWork|WorkHeartbeat)' -count=1 -v`. Expected: missing operations, then behavior failures until implemented.
- [x] **Step 3: Implement atomic admission and heartbeat.** Validate nonzero IDs, safe revisions/sequences, exactly 32 secret bytes, and valid bounded client metadata (80 runes each; nonblank name, no NUL). Hash credentials and canonical admission fields with standard-library SHA-256; compare credentials in constant time. Handle session-ID races without committing partial state. Authenticate exact replay before mutable admission checks; check current ownership before eligibility for new attempts. Use `lockPlanningTask`, Task 2 dependencies, and `MoveTask` to append to `in_progress` and advance revisions once. Record admitted Task/prerequisite revisions. Initial external contact/start equals acceptance; sequence starts at zero. Greater positive sequences update contact using the later of prior contact and server time; duplicates/older values do not. All credential fields carry `json:"-"`; error strings never include them. Returning a conflict never mutates the current owner.
- [x] **Step 4: Confirm green.** Run `go test -race ./internal/postgres -run 'Test(ExternalWork|WorkHeartbeat|TaskPlanning)' -count=1 -v`. Expected: real PostgreSQL concurrency tests pass. Verify marshaled safe views/errors contain neither the supplied secret nor its digest.
- [x] **Step 5: Commit.** Commit these files as `feat: claim external task work and track contact`.

## Task 4: Finish, release, and reassign external sessions safely

**Files:** Create `internal/postgres/work_reports.go`, `work_reports_test.go`. Extend session types/receipt reading in `work_sessions.go`.

**Interfaces:** Consume Task 3 identity/admission helpers and safe views. Produce:

```go
type WorkFinishInput struct {
    Credential WorkCredential
    OperationID uuid.UUID
    Outcome runstate.Status
    Summary string
}
type WorkAdminInput struct {
    TaskID, ExpectedSessionID, OperationID uuid.UUID
    Reason string
}
type WorkReceipt struct {
    ID, SessionID, OperationID uuid.UUID
    Kind, Actor, Reason string
    At time.Time
    ReplacementSessionID *uuid.UUID
}
type WorkChangeResult struct {
    Session WorkSessionView
    Receipt WorkReceipt
    Replacement *WorkSessionView
    PendingCleanup bool
}
func FinishExternalWork(context.Context, pgx.Tx, WorkFinishInput, time.Time) (WorkChangeResult, error)
func ReleaseTaskWork(context.Context, pgx.Tx, WorkAdminInput, time.Time) (WorkChangeResult, error)
func ReassignExternalWork(context.Context, pgx.Tx, WorkAdminInput, ExternalWorkClaim, time.Time) (WorkChangeResult, error)
func WorkSessionReceipts(context.Context, pgx.Tx, uuid.UUID) ([]WorkReceipt, error)
```

- [x] **Step 1: Add failing lifecycle/replay tests.** `TestExternalWorkFinishIsIdempotent` accepts only `succeeded`, `failed`, or `cancelled`, records outcome/release together, preserves planning stage/dates, and returns the same receipt for an exact retry. `TestExternalWorkReassignmentFencesOldOwner` races replacement against heartbeat/finish, checks distinct credentials/IDs and audit links, and rejects all late mutations. `TestWorkAdminReplayCannotReviveOwnership` replays an old operation after the replacement also finishes: receipt stays identical; no claim reappears. Wrong expected owner, missing reason, changed operation payload, blocked replacement, and a closed Task fail atomically.

  ```go
  if retry.Receipt.ID != first.Receipt.ID { t.Fatal("terminal replay duplicated audit") }
  if !errors.Is(lateHeartbeatErr, postgres.ErrWorkRevoked) { t.Fatal("superseded owner wrote") }
  if current != nil || receiptCountAfter != receiptCountBefore { t.Fatal("old admin replay changed ownership") }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/postgres -run 'Test(ExternalWorkFinish|ExternalWorkReassignment|WorkAdmin)' -count=1 -v`.
- [x] **Step 3: Implement terminal receipts and external administration.** Require nonzero operation IDs and nonblank admin reasons; bound reason/summary to 4000 runes and reject invalid text. Persist canonical input digests privately; include the replacement credential digest explicitly despite the secret field's `json:"-"` tag. Test changing only that secret on replay. Exact replay returns the original immutable receipt plus current safe state; it never executes again. Reassignment accepts only an external current owner and a new external session for the same Task; revalidate dependencies/planning, allowing `in_progress` for this explicit handoff. Revocation and replacement commit together. For now native release returns `ErrWorkInput`; Task 6 replaces that branch with fenced cancellation/cleanup. Privileged functions are internal server operations, not credential-authorized client tools. Preserve nondecreasing session transition timestamps if the server clock steps backward, and extend the finish test to check that case. Document that revocation cannot stop external file edits.
- [x] **Step 4: Confirm green.** Run `go test -race ./internal/postgres -run 'Test(ExternalWork|WorkAdmin|WorkHeartbeat)' -count=1 -v`. Expected: serializable race outcomes and exact receipts across new connections. Recheck contact cannot renew after finish/release and planning metadata remains untouched.
- [x] **Step 5: Commit.** Commit as `feat: audit work outcomes and fence reassigned sessions`.

## Task 5: Route every native coding launch through ownership

**Files:** Modify `internal/postgres/run_launch.go`, `run_launch_test.go`, `task_planning_test.go`, `internal/httpapi/api.go`, `run_requests_test.go`, `run_queue_test.go`, `contracts/openapi.json`, generated web contracts, and native-launch fixtures affected by admission. Add native admission helpers in `work_claims.go`.

**Interfaces:** Keep `CreateRun(ctx context.Context, tx pgx.Tx, in RunLaunchInput) (RunLaunchResult, error)` unchanged. Produce private `claimNativeWork(ctx context.Context, tx pgx.Tx, task planningTask, runID uuid.UUID, now time.Time) (WorkSessionView, error)` using the same ownership/eligibility checks as Task 3, with the explicit `open`/`ready` stage policy. `RunLaunchResult` retains its existing fields. Introduce OpenAPI `TaskOwnershipConflict` with `detail`, `code: "task_owned"`, and a safe `owner` projection; use HTTP 409. Ineligible launch is HTTP 422 with actionable detail.

- [x] **Step 1: Add failing native admission tests.** `TestNativeAndExternalClaimsShareOneOwner` races CreateRun against external claim and expects only one owner and no orphan Run. `TestNativeLaunchReplayPrecedesEligibility` retries the original key after stage/Agent changes and after session release, returning the same Run; changed key/input cannot bypass ownership or reopen closed work. Cover empty/whitespace instructions, missing Repository, dependencies, and Task/board revision changes. Add HTTP assertions for 409 owner identity and secret absence.

  ```go
  if liveSessions != 1 || orphanCodingRuns != 0 { t.Fatal("native admission split ownership") }
  if replay.RunID != first.RunID || replay.Created { t.Fatal("native replay forked") }
  if response.Code != 409 || conflict.Owner.ID != winner.ID { t.Fatal("owner hidden by stage error") }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/postgres ./internal/httpapi -run 'Test(Native|Launch|TaskPlanning)' -count=1 -v`.
- [x] **Step 3: Integrate admission and error mapping.** Replace Task-only locking with Project/Task planning order. Retain keyed replay before mutable checks. Validate current ownership/eligibility before insert; create Run and native session, its claim receipt, and Task stage in one transaction. Native sessions have no client secret. Preserve PR review's separate path. Map typed ownership/ineligibility errors in the existing launch handler. For launch tests, provide valid requirements/Repository; tests of actual concurrent work use different Tasks. For pure historical read fixtures, insert explicit history rather than secretly launching concurrent production work. Do not add a production test bypass.
- [x] **Step 4: Confirm green.** Run the Step 2 command, `go test ./internal/httpapi -run 'TestRunQueue' -count=1 -v`, then `corepack pnpm contracts:generate` and `corepack pnpm contracts:check`. Expected: original keyed replay and new conflict/eligibility assertions pass. Fixture failures in other packages must be fixed where their now-invalid launch assumptions originate.
- [x] **Step 5: Commit.** Commit as `feat: share task ownership across native and external launches`.

## Task 6: Preserve ownership through execution, cleanup, and delegation

**Files:** Create `internal/postgres/work_run_hooks.go`, `work_run_hooks_test.go`. Modify `queue.go`, `resources.go`, `execution.go`, `workspaces.go`, `run_cancel.go`, `delegation_admission.go`, `delegation_continuation.go`, `work_reports.go`; extend queue/resource/delegation/worker-process tests and their shared `seed` fixtures.

**Interfaces:** Consume Task 5 native session association. Private hooks, all called while the corresponding Run is locked:

```go
func requireNativeWork(context.Context, pgx.Tx, uuid.UUID) error
func startNativeWork(context.Context, pgx.Tx, uuid.UUID, time.Time) error
func contactNativeWork(context.Context, pgx.Tx, uuid.UUID, time.Time) error
func finishNativeWork(context.Context, pgx.Tx, uuid.UUID, runstate.Status, time.Time) error
func markNativeWorkRecovery(context.Context, pgx.Tx, uuid.UUID, time.Time) error
func releaseNativeWorkAfterCleanup(context.Context, pgx.Tx, uuid.UUID, time.Time) error
```

No hook changes a Task planning stage or acquires its Project/Task lock. PR review
callers skip coding-session hooks. A repeated semantic native hook uses the
existing state/receipt rather than creating another operation. `ReleaseTaskWork`
gains native support without changing its signature.

- [x] **Step 1: Add failing execution tests.** `TestNativeWorkLifecycleAndCleanup` checks queued start/contact nulls, first running timestamp, terminal outcome before release, cleanup lease renewal without new coding contact, and release only after worker/Workspace cleanup. `TestNativeRecoveryKeepsTaskOwnership` expires a lease, confirms recovery/failure, completes cleanup, and still expects ownership/Needs attention until explicit user release. Cover exhausted recovery and cleanup failure.

  `TestNativeTaskReleaseWaitsForCleanup` covers cancellation before acquisition, active cancellation, terminal unreleased resources, and safe final release. `TestUnownedCodingRunDoesNotBlockValidQueue` checks missing/mismatched association is never executed while valid coding/PR reviews progress. Extend delegation tests: parent and child have distinct claims, parent remains owned without synthetic contact during suspension, invocation 2 reuses its session/start time, and parent cancellation retains both cleanup fences. Race admin cancellation with worker heartbeat/delegation and reject stale worker/invocation writes.

  ```go
  if afterRecovery.ReleasedAt != nil || !afterRecovery.NeedsUserResolution { t.Fatal("recovery freed task") }
  if !reflect.DeepEqual(afterCleanup.LastContactAt, atOutcome.LastContactAt) { t.Fatal("cleanup changed coding contact") }
  if resumed.ID != parent.ID || !resumed.StartedAt.Equal(*parent.StartedAt) { t.Fatal("continuation became new work") }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/postgres -run 'Test(NativeWork|NativeRecovery|NativeTaskRelease|UnownedCoding|Delegation)' -count=1 -v`.
- [x] **Step 3: Wire hooks at shared lifecycle seams.** Queue selection requires a matching current session for ordinary coding work and skips invalid rows so other work progresses; `Resources.WithRun` rechecks the association under its existing Run/lease/invocation fence. Record contact at successful worker acquisition and through fenced lease renewal only before outcome. In `MarkRunning`, use the actual Run `started_at` returned by its update for the session's first start. Record outcomes in both `executionTransition` and queue/cancel terminal paths, and the recovery flag before cleanup. `ReleaseClaim` clears the worker lease and releases normal ownership only after the existing Workspace check; recovery-held ownership remains. Parent suspension retains its session; child creation sets `parent_task_id`; continuation retains the original session. Compute native queued/waiting/cleanup/attention views from Run/delegation facts without invented contact; an expired execution lease shows attention before recovery polling catches up.

  Privileged native release locks Project, Task, Run, then session. If still active, use existing `CancelRun`, record `cancellation_requested`, return `PendingCleanup=true`, and commit cancellation; do not return an error that causes rollback. Exact replay returns that receipt/current state without re-running cancellation. There is no queued automatic replacement. After cleanup, a fresh operation ID/expected-owner command resolves any recovery-held claim; ordinary cancelled work may already be released by cleanup. Native handoff then uses an explicit ready/start action. Never pass a native owner through the atomic external reassignment function. Keep parent-before-child cancellation order and avoid any session-before-Run wait.

  Update execution seeds to create prepared Tasks and native sessions through admission; retain deliberate malformed rows only in rejection tests. Worker tests that previously ran competing attempts on one Task must use separate Tasks or complete/release/re-prepare the first attempt.

- [x] **Step 4: Confirm green.** Run `go test -race ./internal/postgres -count=1 -v`, with a disposable PostgreSQL database. Then enable existing Docker fixtures and run `CIRCULAR_RUN_DOCKER_TESTS=1 go test -race ./internal/postgres -run 'Test(RealWorker|DelegationFullWorker|DelegationRecoveryProcess)' -count=1 -timeout=300s -v`. Expected: actual resource/process scenarios pass; skipped Docker tests are not evidence of cleanup safety.
- [x] **Step 5: Commit.** Commit as `feat: retain task ownership through native execution recovery`.

## Task 7: Finish existing provider, MCP, and console launch paths

**Files:** Modify `internal/integrations/request_launch.go`, `request_launch_test.go`, `internal/controlmcp/mutations.go`, `task_planning.go`, and their tests; `apps/web/src/launch.ts`, `launch.test.ts`, `components/console/task-launcher-context.tsx`, `components/tasks/task-detail.tsx`, `components/external-request-list.tsx`, and affected browser fixtures/tests. Verify the existing `internal/controlmcp/client.go` detail handling without changing it unnecessarily.

**Interfaces:** Preserve existing `StartExternalRequest`/`ProcessExternalRequest`, `start_run`, `create_task`, `update_task`, `get_task_planning`, and `launchTask` signatures except adding optional parent/dependency fields to their existing inputs. Extend `requireTaskPlanning(ctx context.Context, requiredCreateFields ...string) error` to check additional TaskCreate fields when requested. Reuse the existing bounded MCP detail handling: Task 5's detail names the current owner/session, while HTTP also exposes the safe structured owner.

- [x] **Step 1: Add failing integration/compatibility tests.** `TestExternalRequestWaitsForTaskOwner` covers a current external session and native cleanup-held session: no new Run, actionable waiting state, no automatic retry after release. `TestExternalRequestIneligibleNeedsPreparation` distinguishes missing planning/dependencies from provider outages. Race launch against delegation and configuration changes with barriers to expose lock inversion.

  `TestMCPParentRequiresServerSupport` serves an old OpenAPI document and asserts zero create requests when a parent was requested. Test dependency omission/clear forwarding, owner details on `start_run` conflict, and absence of mutation tools on read-only MCP. Update start-run wording: a new key cannot bypass ownership/readiness. Browser tests cover blank launch description, preserved Task after a launch failure, and active-work/history lock explanations.

  ```go
  if request.Status != "waiting_for_active_run" || createdRuns != 0 { t.Fatal("provider bypassed owner") }
  if createRequests != 0 { t.Fatal("old server silently dropped parent link") }
  if strings.Contains(toolError, secret) || !strings.Contains(toolError, ownerID) { t.Fatal("unsafe/unhelpful conflict") }
  ```

- [x] **Step 2: Confirm red.** Run `go test ./internal/integrations ./internal/controlmcp -run 'Test(ExternalRequest|MCPParent|TaskPlanning)' -count=1 -v` and `corepack pnpm test`. Expected: new forwarding/waiting/validation assertions fail before changes.
- [x] **Step 3: Adapt existing callers.** Move Linear launch's Project lock ahead of Agent/Repository locks while preserving provider identity/grant/route/request ordering; verify competing configuration mutations use a compatible order. Convert ownership conflicts into existing `waiting_for_active_run`, with detail naming the session and Task console URL even when no Run exists. Display “Waiting for task owner” rather than assuming every owner has a Run. Invalid planning becomes `awaiting_approval`; temporary provider failures keep existing retry behavior.

  Add optional parent support to `create_task`, with the OpenAPI feature probe before any write. Forward prerequisite arrays through existing planning tools and preserve strict PATCH semantics. Verify the existing MCP error path retains the bounded owner/session detail; do not introduce external lifecycle tools. Require a description only when launching, allowing empty-description planning drafts. Update existing launcher/context paths together and explain `active_work`/`has_work_sessions` locks in task detail. Do not add an owner dashboard or rewrite task UI.

- [x] **Step 4: Confirm green.** Run `go test -race ./internal/integrations ./internal/controlmcp ./internal/httpapi -count=1 -v`, `corepack pnpm contracts:check`, `corepack pnpm typecheck`, and `corepack pnpm test`. Run affected browser scenarios with the isolated E2E stack: `corepack pnpm test:e2e tests/browser/tasks.spec.ts tests/browser/task-planning.spec.ts tests/browser/mcp.spec.ts tests/browser/linear-agent-execution.spec.ts tests/browser/delegation.spec.ts --forbid-only --reporter=line`. Expected: both mocked presentation and actual API/worker paths pass.
- [x] **Step 5: Commit.** Commit as `feat: enforce ownership across existing launch clients`.

## Task 8: Verify the whole change and document the upgrade boundary

**Files:** Update `docs/architecture/foundation.md`, `docs/development/local-development.md`, `infra/production/README.md`, this plan's checkboxes/results, and only remaining fixtures exposed by full verification. Keep the approved spec and `CONTEXT.md` with the PR.

**Interfaces:** No new production interface. Consume the previous tasks' migration, commands, contracts, and tests.

- [x] **Step 1: Run the complete required checks.** Use the isolated toolchain/database established before Task 1. Verify `TEST_DATABASE_URL` is set; a suite that skips database tests is incomplete. Do not use the live control database. Run:

  ```sh
  gofmt -l cmd internal contracts
  go mod verify
  go mod tidy -diff
  go vet ./...
  go build ./...
  CIRCULAR_RUN_DOCKER_TESTS=1 go test -race ./... -count=1 -timeout=300s
  corepack pnpm contracts:check
  corepack pnpm typecheck
  corepack pnpm test
  corepack pnpm build
  corepack pnpm test:e2e --forbid-only --reporter=line
  git diff --check
  ```

  Expected: no formatting/tidy drift, all checks pass with database/Docker coverage. Reproduce the existing disposable Compose browser job when needed to verify deployment migration/startup; use a unique Compose project and execution root, never an existing development/production stack. Record commands and actual results, including genuine skips/failures. Fix root causes and rerun affected checks before claiming completion.

- [x] **Step 2: Document operator and client limits.** Describe one-owner semantics, session-secret handling, explicit release, retry receipts, 30/120-second contact behavior, native recovery holds, and external revocation's inability to stop file edits. Document the exact migration diagnostic and the sequence: block launches, finish/cancel coding work, verify cleanup, stop writers, migrate, start only new binaries. Explain that historical Runs remain historical and no old contact is fabricated. No production rollout is performed by this task.
- [x] **Step 3: Review against the spec.** Check every acceptance item and the five Review Focus cases has passing evidence. Audit all `CreateRun`/`INSERT INTO runs`, terminal Run writes, and planning mutations for bypasses. Review credential projections and transaction order, including integration callers' pre-existing locks. Keep failures or unverified environments visible in the PR; do not call a skipped check passed.
- [ ] **Step 4: Commit and hand off the implementation PR.** Commit documentation/verified fixture changes as `docs: explain task ownership and upgrade requirements`. Request the selected execution workflow's whole-branch review, address material findings, and include this plan/spec in the implementation PR. No automatic merge or deployment is implied.

## Execution results

Native execution was approved on 2026-10-20. Tasks 1–7 are implemented on this
feature branch; Task 8 documentation, fixture corrections and full verification
are complete. The main checkout and private deployment were not changed.

- Migration, planning, external claims/reports, native launch, worker recovery,
  delegation, provider requests, MCP compatibility and UI eligibility each passed
  their focused checks after reproducing the missing behavior.
- Formatting, module verification/tidy, Go vet/build, generated contracts,
  TypeScript checks, 49 frontend tests and the production frontend build pass.
- The initial full Go run exposed one checkpoint fixture that manually forged a
  coding Run; it now uses real admission/acquisition and its regression passes.
  The full Go race suite now passes with PostgreSQL and Docker tests enabled.
- The first full browser run passed 159/164. Four launch scenarios lacked the
  now-required description; their fixtures are updated. The fifth failure had
  Chromium `ERR_NETWORK_CHANGED` while loading modules; the unchanged filter
  test passed three isolated reruns. An earlier Kanban keyboard timing failure
  also passed three unchanged reruns. The final full browser run passes all
  164 tests (7.3 minutes), including both previously intermittent cases.
- A uniquely named disposable Compose stack built and migrated successfully,
  returned healthy API status, and passed all three browser execution scenarios
  (success, cancellation and failure). Its containers/network/volume were removed.

Acceptance evidence from the passing suites:

| Design check | Coverage |
| --- | --- |
| Atomic external/native ownership and rollback | `TestExternalWorkClaimsSerialize`, `TestNativeAndExternalClaimsShareOneOwner`, native launch/queue tests |
| Restart, replay and credential isolation | `TestExternalWorkReplayAfterRestart`, `TestExternalWorkFinishIsIdempotent`, safe HTTP/MCP conflict projections |
| Contact deadline and backward clocks | `TestWorkHeartbeatDeadlineAndSequence` |
| Reassignment, late reports and old admin replay | `TestExternalWorkReassignmentFencesOldOwner`, `TestWorkAdminReplayCannotReviveOwnership` |
| Planning eligibility and dependency races | `TestTaskPrerequisitesValidateAndSerialize`, `TestExternalWorkEligibilityAndEditRace`, planning HTTP/MCP tests |
| Native execution, cancellation and recovery holds | `work_run_hooks_test.go`, real Docker worker and recovery tests |
| Parent/child separation and PR-review exemption | `TestDelegationRetainsParentWorkSession`, `TestDelegationCancellationRetainsBothCleanupClaims`, `TestUnownedCodingRunDoesNotBlockValidQueue` |
| Safe upgrade and preserved history | `TestWorkSessionMigrationPreservesHistory`, `TestWorkSessionMigrationRequiresDrainedCoding`, `TestWorkSessionConstraints`, fresh Compose migration |
| Existing client compatibility and no credential exposure | Full HTTP/MCP/provider/browser suites, including `TestMCPParentRequiresServerSupport` |

The production write audit found coding Run insertion only in shared admission;
PR review insertion remains separate. Native terminal writes use session hooks.
Provider import upserts do not overwrite existing instructions. Provider locks
precede Project/Task and Agent/Repository locks; native resource hooks never
acquire planning locks.

The final whole-branch review and implementation PR are pending. No merge,
production upgrade or live model/provider call is part of this verification.
