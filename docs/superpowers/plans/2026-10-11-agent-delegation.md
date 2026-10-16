# Agent Delegation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Let a permitted Agent delegate one objective to a specialist, then automatically continue the same parent Run with the child's outcome and artifacts.

**Architecture:** Extend the existing delegation table, Go worker, private MCP tools, and retained artifacts. The parent ends its current invocation, checkpoints its work, releases its container and claim, and waits while an isolated child executes. A durable continuation claim starts a fresh parent invocation after child cleanup; no native Codex session resume is assumed.

**Tech Stack:** Existing Go toolchain and packages, PostgreSQL/pgx, MCP Go SDK, Git, Docker, and generated OpenAPI/TypeScript contracts. No new dependencies.

**Spec:** [Agent delegation design](../specs/2026-10-10-agent-delegation-design.md).

**Status:** Accepted for native execution on 2026-10-14. All eight implementation tasks, release checks and final independent review are complete. Both review findings are fixed and verified.

## Global Constraints

- “The proposed first increment supports one child Run per parent Run and one delegation level.” Treat these as fixed v1 limits, not configurable budgets.
- “These fields become the child Task's title and description.” Preserve `DelegationObjective{Title, Description}` with JSON fields `title` and `description`.
- “The worker derives the parent Run, Project, Repository, source snapshot, and effective permissions.” Only `agent_id`, `request_key`, and `objective` are model-selected arguments.
- “The parent automatically continues with the child's outcome and artifacts, within its approved permissions.” No new human-review gate on continuation.
- “Parent cancellation prevents continuation.” A failed or independently cancelled child still supplies an honest outcome to an eligible parent.
- “Delegated children do not automatically publish PRs or Linear updates.” Existing dedicated PR-review Runs remain separate.
- “A continuation belongs to the same logical parent Run; it is not a fresh execution attempt or a silent retry of failed work.”
- “Reuse the existing Go packages and dependencies.” No generic workflow engine, parallel children, nested delegation, new Task/Kanban interface, or new model selection policy.
- “Coordinate migration ordering and any shared schema/generated-client changes with the Task planning agent; do not reserve a migration number in this draft.”
- Keep the application private. This plan authorizes no deployment, real-model execution, merge, or provider publication.

## Review Focus

1. An owner changes policy or Agent settings during handoff: admission must use a coherent permitted target and immutable execution inputs. Tests: Tasks 2 and 4.
2. Source contains deleted tracked files, ignored/untracked files, binaries, or links: the child sees the exact checkpoint and its patch contains only child changes. Tests: Task 3.
3. The same worker resumes the same Run: old events, heartbeat callbacks, container handles, and usage must not affect the second invocation. Tests: Tasks 5 and 7.
4. A commit acknowledgement is lost or cancellation races with cleanup: there is at most one child and no ownerless resource leak or model replay. Tests: Tasks 4 and 6.
5. A child finishes under Project-wide publishing settings: no child publication or premature parent completion reaches GitHub/Linear. Tests: Task 7.

---

## Execution preparation and file ownership

Implement in an isolated checkout based on the integrated branch, not the dirty shared checkout. Record its commit and reconcile the existing Task planning migrations before editing shared files. At inspection, `migrate.Head` is `0017`; select the next unused four-digit SQL revision at execution time and update migration registration/tests together. Never modify an already-applied migration.

The new `internal/delegations` package owns request validation, local spooling, and delegation data contracts. Persistence stays in `internal/postgres`; process/resource coordination stays in `internal/execution`. Existing artifact, Git, runtime, and backend packages keep their current responsibilities. Each task below lists its production files and adjacent tests; update all callers of changed signatures in those packages.

Shared edits are limited to migration registration, HTTP/MCP registration, contracts/generated types, and Run event subscription. Do not edit Task planning handlers or components. Exercise their existing creation triggers and content locks through integration tests. Execute tasks in order; do not enable delegation on a live instance between partial tasks.

Test snippets below are assertion excerpts inside the named tests, using each package's existing fixtures. They do not introduce a new assertion library. For every red step, a missing behavior or symbol must cause failure; a skipped database/Docker test does not count.

### Task 1: Bounded private request and invocation handoff proof

**Files:** Create `internal/delegations/request.go`, `request_test.go`, `spool.go`, `spool_test.go`; create `internal/agenttools/delegation.go`, `delegation_test.go`. Modify `internal/agenttools/server.go`, `server_test.go`, `internal/codexworkload/workload.go`, `workload_test.go`, `subscription_test.go`, `internal/backends/backend.go`, `codex.go`, and `codex_test.go`.

**Interfaces:**

- `DelegationObjective` retains the accepted two string fields. `Request` has `AgentID uuid.UUID`, `RequestKey string`, and `Objective DelegationObjective`; `Target` has `AgentID uuid.UUID` and `Name string`.
- Produce `Decode([]byte) (Request, error)`, `Normalize(Request) (Request, error)`, `(Request).Fingerprint() string`, `NewSpool(directory string) (*Spool, error)`, `(*Spool).Request(Request) (Request, error)`, and `(*Spool).Publish(io.Writer) error` in `internal/delegations`.
- Add `agenttools.AddDelegation(server *mcp.Server, spool *delegations.Spool, targets []delegations.Target)`. Preserve `New`/`Run` callers: the workload writes a bounded target catalog in its private tool directory; `Run` reads it and registers the extra tool only when present.
- Add `backends.Input.DelegationTargets []delegations.Target`. The default is empty. Production workers do not populate it until Task 4.

- [x] **Step 1: Write failing tests.** `TestDelegationRequestStrictBounds` covers trimmed, nonempty title/description/key, 500/20,000/200 Unicode-character limits, nil UUID, invalid UTF-8/NUL, duplicate/unknown keys, trailing JSON, and a 128 KiB encoded request limit. `TestDelegationSpoolReplay` covers identical replay, changed payload under the same key, a second distinct request, symlinks, and atomic bounded publication. `TestDelegationToolPendingHandoff` requires status `pending_handoff`, no child ID, and no tool on ordinary unconfigured or PR-review invocations. Include assertions such as:

  ```go
  if len(encoded) > 128*1024 && err == nil { t.Fatal("accepted oversized request") }
  if first.Fingerprint() != replay.Fingerprint() { t.Fatal("replay changed identity") }
  if status != "pending_handoff" || childID != "" { t.Fatal("claimed child creation") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/delegations ./internal/agenttools ./internal/codexworkload ./internal/backends -run 'TestDelegation' -count=1`.
- [x] **Step 3: Implement the local contract.** Reuse proposal spooling patterns in a separate subdirectory so proposal enumeration cannot ingest delegation records. Expose `delegate_task` with the allowed target catalog in its description. Publish `circular.delegation.requested` only after clean CLI exit, through existing redaction. The backend decoder validates it even after `turn.completed`; it is a request, never admission authority. Instructions require a handoff summary and normal invocation exit after an accepted request. Reject tampering again in Go; private tool files are not trusted authorization.
- [x] **Step 4: Prove the process seam and verify.** Add `TestDelegationSequentialInvocationsReleaseAuth` using the existing fake executable and real `codexauth.WithLock` test seam: parent invocation, child invocation, and parent continuation acquire the same temporary login lock in that order, with release between each. Use channels/deadlines, no arbitrary sleeps. Assert three invocations and no overlap. Run the Task 1 packages with `-race -count=1`. This proves process sequencing, not durable orchestration.
- [x] **Step 5: Commit the tested slice.** `feat(delegation): add bounded private handoff requests`.

### Task 2: Durable policy, request identity, and invocation inputs

**Files:** Create the next migration and `internal/migrate/delegation_test.go`; modify `internal/migrate/migrate.go`, `migrate_test.go`. Create `internal/delegations/record.go`, `internal/postgres/delegations.go`, `delegations_test.go`, `invocations.go`, `invocations_test.go`, `internal/httpapi/delegations.go`, `delegations_test.go`, `internal/controlmcp/delegations.go`, and `delegations_test.go`. Modify HTTP/MCP route/tool registration, `contracts/openapi.json`, and `apps/web/src/generated/api.ts`.

**Interfaces and storage:**

- Add `agent_delegation_targets(project_id, parent_agent_id, target_agent_id)` with composite Agent/Project foreign keys and a unique parent/target pair. Empty policy disables delegation. An Agent cannot target itself; both Agents must be enabled and in the same Project. Revocation stops future admissions; cancelling the parent stops already admitted work.
- Extend `delegations` with nullable `request_key`, `input_fingerprint`, `objective_json`, `checkpoint`, `result`, and `error`. Keep legacy rows and `objective` text intact. New rows store the normalized description in `objective` and both fields in `objective_json`. Partial unique indexes enforce one new delegation per parent and one per `(parent_run_id, request_key)`; uniquely bind nonnull child Run IDs. Only new keyed rows participate in this lifecycle.
- Add `runs.invocation` default `1` and `run_invocations(run_id, number, input, input_sha256)` with primary key `(run_id, number)`. V1 permits numbers 1 and 2; a delegated child remains at 1. Snapshot resolved Task/Agent/Repository inputs and model settings, never credentials or credential-bearing workload stdin.
- `delegations.Record` exposes UUID fields `ID`, `ParentRunID`, `TargetAgentID`, `ChildTaskID`, `ChildRunID`, plus `RequestKey` and `Status`. Nullable child IDs represent rejected admission. New statuses are `waiting`, `continuing`, `completed`, `cancelled`, and `rejected`; child success/failure belongs in its recorded outcome.
- Produce `postgres.SetDelegationTargets(ctx context.Context, tx pgx.Tx, parentAgentID uuid.UUID, targetIDs []uuid.UUID) error` and `DelegationTargets(ctx context.Context, tx pgx.Tx, parentAgentID uuid.UUID) ([]delegations.Target, error)`.
- Produce `postgres.SaveInvocationInput(ctx context.Context, tx pgx.Tx, input ProvisioningContext) error` so child admission can freeze its inputs before it has a worker claim; produce `(*RunResources).InvocationInput() (ProvisioningContext, error)` for owned execution. Extend `ProvisioningContext` with `Invocation int`, `DelegationTargets []delegations.Target`, and `DelegationContextSHA256 string`.

- [x] **Step 1: Write failing tests.** `TestDelegationMigrationPreservesHistory`, `TestDelegationPolicyScopeAndReplacement`, and `TestDelegationInvocationSnapshot` assert preserved historical Tasks/Runs/delegations, empty default policy, idempotent policy replacement, same-Project validation, and immutable resolved inputs despite subsequent Agent edits:

  ```go
  if historicalCountAfter != historicalCountBefore { t.Fatal("history changed") }
  if len(defaultTargets) != 0 { t.Fatal("delegation enabled by migration") }
  if resumed.Instructions != original.Instructions { t.Fatal("mutable continuation input") }
  if !bytes.Equal(resumed.BackendConfig, original.BackendConfig) { t.Fatal("model changed") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/controlmcp -run 'TestDelegation' -count=1 -v` against the disposable test database.
- [x] **Step 3: Implement storage and owner policy transport.** Add private management `GET`/`PUT /api/v1/agents/{agent_id}/delegation-targets`, body/response `{ "target_ids": [...] }`; PUT replaces the complete set, and `[]` disables it. Use strict decoding at this new boundary without changing legacy `body` behavior. Register `get_delegation_targets` and `set_delegation_targets` on the owner control MCP, never on the Run's private MCP. Lock Project before sorted Agent rows for policy writes. Return 422 for malformed/out-of-scope IDs and 404 for a missing parent; roll back the whole replacement on error. Persist invocation inputs before allocating its first container, resolving backend defaults once. Identical snapshot replay succeeds; different content conflicts.
- [x] **Step 4: Verify.** Run the same focused tests and `corepack pnpm contracts:generate`, `corepack pnpm contracts:check`, and `corepack pnpm typecheck`. Update tool inventory/count assertions in `internal/controlmcp/server_test.go`, `http_test.go`, and `tests/browser/mcp.spec.ts`.
- [x] **Step 5: Commit.** `feat(delegation): persist scoped policy and invocation inputs`.

### Task 3: Verified checkpoint and child-relative source

**Files:** Create `internal/delegations/checkpoint.go`, `internal/execution/delegation_checkpoint.go`, `delegation_checkpoint_test.go`, `internal/artifacts/restore.go`, `restore_test.go`, `internal/git/delegation.go`, and `delegation_test.go`. Modify `internal/execution/cleanup.go`, `internal/git/base.go`, and adjacent cleanup/base tests only where shared ownership rules require it.

**Interfaces:**

- `delegations.Checkpoint` contains `Source` and `Progress` as `artifacts.Content`, and `BaseCommit`, `BaseRef`, `SourceCommit` strings. Its input identity refers to persisted invocation 1.
- Produce `(*Retention).Checkpoint(ctx context.Context, runID uuid.UUID) (delegations.Checkpoint, error)` and `artifacts.RestoreArchive(ctx context.Context, source io.Reader, destination string) error`.
- Produce `(*git.Local).SnapshotDelegation(ctx context.Context, parentRunID, repositoryID uuid.UUID, archive io.Reader) (string, error)`, returning the pinned synthetic source commit. Existing `Provision` can create the child's worktree at that commit.

- [x] **Step 1: Write failing tests.** `TestDelegationCheckpointRoundTrip`, `TestDelegationPatchBase`, and `TestDelegationRestoreRejectsEscapes` cover tracked deletions, ignored/untracked files, binaries, executable modes, safe links, absolute/traversal paths, duplicate archive entries, `.git` at every depth, escaping links, special devices, truncation, and checksum mismatch:

  ```go
  if !bytes.Equal(restoredBinary, originalBinary) { t.Fatal("binary changed") }
  if deletedFileExists { t.Fatal("restore resurrected deleted source") }
  if childDiffContainsParentEdit { t.Fatal("child patch includes parent work") }
  if escapedFileExists || protectedMetadataChanged { t.Fatal("archive escaped ownership") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/artifacts ./internal/git ./internal/execution -run 'TestDelegation' -count=1`.
- [x] **Step 3: Implement checkpoint/source capture.** Require a cleanly exited invocation and current ownership. Reuse `artifacts.Archive`, `LocalStore.WriteStream`, and `Verify`; use distinct immutable names `delegation-source.tar` and `delegation-progress.json`, not final `worktree.tar`/`git-diff.patch`. Record these references internally, outside ordinary publication artifacts. Build the synthetic source commit through a private Git index from the complete validated snapshot, including parent untracked/ignored content and deletions. Disable repository-controlled hooks/configuration during host Git operations. Pin source ownership to the parent; child provisioning uses the existing per-child base pin. Parent final diff still compares against the original base. Restore into a newly owned staging directory; never overlay an existing repository and accidentally retain deleted files. Stream files and enforce archive/content lengths without loading source into memory.
- [x] **Step 4: Verify capture and cleanup.** Run Task 3 package tests with `-race -count=1`, including corrupt archives and interrupted publication. A differing retry must fail without replacing the first checkpoint. Final parent cleanup removes only its owned temporary context/source references; child cleanup cannot remove the parent's original base. Preserve checkpoints as retained internal evidence. Do not silently truncate recorded progress to fit the 16 MiB workload input limit; fail continuation clearly if its verified input cannot fit.
- [x] **Step 5: Commit.** `feat(delegation): checkpoint parent source and isolate child patches`.

### Task 4: Atomic admission and parent suspension

**Files:** Modify `internal/postgres/delegations.go`, `delegations_test.go`, `resources.go`, `execution.go`, `workspaces.go`; create `internal/execution/delegation.go`, `delegation_test.go` and `internal/postgres/supervisor_delegation_test.go`. Modify `internal/execution/supervisor.go`, `protocol.go`, and their affected tests.

**Interfaces:** Consume Tasks 1–3. Produce `(*Resources).SuspendForDelegation(ctx context.Context, parentRunID uuid.UUID, request delegations.Request, checkpoint delegations.Checkpoint) (delegations.Record, error)` and `(*Supervisor).handoff(ctx context.Context, parentRunID uuid.UUID, request delegations.Request) error`. Store only a validated candidate during output ingestion; launch nothing before the decoder finishes and exit code is zero.

- [x] **Step 1: Write failing tests.** `TestDelegationAdmissionReplayAndConflict`, `TestDelegationAdmissionPolicyRace`, and `TestDelegationSuspensionKeepsWorkspace` exercise concurrent identical/different requests, revoked/disabled targets, forged identities, PR-review parents, nested delegation, absent Repository, and overlapping Task creation:

  ```go
  if childTasks != 1 || childRuns != 1 { t.Fatal("duplicate child work") }
  if parentStatus != runstate.WaitingForInput || parentWorker != nil { t.Fatal("not parked") }
  if workspaceStatus != "suspended" || parentWorktreeMissing { t.Fatal("lost parent source") }
  if runCompletedEvents != 0 { t.Fatal("handoff published completion") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/postgres ./internal/execution -run 'TestDelegation' -count=1 -v`.
- [x] **Step 3: Implement the transaction and supervisor branch.** Populate allowed targets only for eligible root coding invocations; fake execution is supported for tests. The worker checks current owner, invocation, target policy, Project, Repository, backend compatibility, and depth. After checkpoint verification, remove the exact exited parent container and confirm release. `SuspendForDelegation` owns its transaction: acquire the Project lock needed by Task insertion before the parent Task/Run locks; never call it from inside `WithRun`. Lock Agents in stable UUID order before snapshotting target settings. Create the child Task, child Run through `CreateRun`, frozen child inputs, delegation row, and Events atomically. Use the delegation UUID as the child Run's internal request key, avoiding the launch API's byte-length mismatch with Unicode delegation keys. Child external references are empty. Set parent `waiting_for_input`, workspace `suspended`, clear the removed container ID, and release its claim in this same transaction.
- [x] **Step 4: Verify nonterminal exit handling.** The supervisor returns from the confirmed handoff without finalization, `RecordFailure`, terminal cleanup, or normal `ReleaseClaim`. Worker `ReconcileExit` already accepts an absent claim; test that behavior explicitly. Replay lookup precedes admission mutation: an identical committed request returns its recorded child even after claim release; different payload conflicts. A lost commit acknowledgement is reconciled with the same identity, never a new Task. Rejected admission creates no child, records a safe rejection reason, and terminates the parent without automatic retry. Run Task 4 tests with `-race -count=1`, including the existing Task planning tests to catch lock-order regressions.
- [x] **Step 5: Commit.** `feat(delegation): admit child work and suspend parent atomically`.

### Task 5: Automatic continuation with invocation fencing

**Files:** Create `internal/postgres/delegation_continuation.go`, `delegation_continuation_test.go`, `internal/execution/delegation_context.go`, `delegation_context_test.go`, `internal/runtimes/delegation_test.go`; modify `internal/delegations/checkpoint.go`, `internal/postgres/queue.go`, `resources.go`, `execution.go`, `workspaces.go`, `internal/worker/worker.go`, `internal/execution/supervisor.go`, `retention.go`, `cleanup.go`, `internal/runtimes/policy.go`, `inspection.go`, `recovery.go`, `internal/backends/backend.go`, `codex.go`, and `internal/codexworkload/workload.go`. Update claim fixtures and affected package tests.

**Interfaces:**

- Extend `worker.Claim` with `Invocation int` and `Continuation bool`. Produce `(*Resources).ForInvocation(number int) (*Resources, error)`; its returned store rejects every `WithRun` operation when the durable invocation differs. Bind all production execution, retention, and heartbeat operations to a per-claim store, without mutating a shared supervisor instance. Reject invocation zero; ordinary claims explicitly use 1 and recovery claims retain the durable number. Update existing claim fixtures accordingly.
- `delegations.Result` has `ChildRunID uuid.UUID`, `Status runstate.Status`, `Error string`, and optional `Report`/`Patch *artifacts.Content`. Missing output from failed/cancelled work remains explicit.
- Produce `(*RunResources).ContinuationContext() (ProvisioningContext, delegations.Checkpoint, delegations.Result, error)`, `ResumeWorkspace() error`, and `MarkContinued() error`. `ResumeWorkspace` emits the existing `workspace.provisioning` event for its pending transition.
- Add `runtimes.DelegationMount{ContextSHA256 string}` and optional `Spec.Delegation`. Produce `(*Supervisor).prepareDelegationContext(ctx context.Context, parentRunID uuid.UUID) (string, error)` returning the verified context digest, and `(*runtimes.Docker).ReleaseDelegation(ctx context.Context, runID uuid.UUID, resourceID, contextSHA256 string) error` for exact cleanup after worker restart. Include the saved digest in `postgres.ResourceState` for cleanup validation.

- [x] **Step 1: Write failing tests.** `TestDelegationContinuationExactlyOnce`, `TestDelegationContinuationFailureResult`, and `TestDelegationContinuationFencesOldInvocation` cover two competing workers, each child terminal outcome, resource cleanup still pending, changed live Agent settings, stale invocation-1 output, and a stopped/replaced container:

  ```go
  if continuationClaims != 1 || claim.Invocation != 2 { t.Fatal("duplicate continuation") }
  if resumedRunID != parentRunID || attemptAfter != attemptBefore { t.Fatal("new Run attempt") }
  if childWorker != nil || childWorkspaceStatus != "released" { t.Fatal("resumed before cleanup") }
  if !errors.Is(staleWrite, postgres.ErrLeaseLost) { t.Fatal("accepted stale output") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/postgres ./internal/execution ./internal/runtimes ./internal/codexworkload -run 'TestDelegation' -count=1 -v`.
- [x] **Step 3: Implement durable result consumption and claiming.** Retain a verified child report artifact during child cleanup when a report exists; retain any available diff without claiming a failed child succeeded. Extend `Queue.Acquire` with a bounded eligible-parent selection alongside ordinary queued work. A parent is eligible only when its child is terminal, has no worker claim, and has either a released Workspace or no Workspace because allocation never happened. Under the parent lock, recheck eligibility, record the child outcome/references, change delegation to `continuing`, claim invocation 2, and preserve the Run's attempt/start time. Child cleanup never takes the parent lock while holding the child lock. It need not update a second workflow queue.
- [x] **Step 4: Implement and verify execution continuation.** Keep the parent in `waiting_for_input` while its new container is allocated; use dedicated resume methods to move its Workspace from `suspended` through `pending` to `ready`, then Run to `running` with `run.continued`. Verify checkpoint and retained workspace ownership/content before reusing them; missing/tampered state fails closed. Construct and persist exact invocation-2 input from invocation 1 plus the verified child result before model execution. Mount only that Run's context read-only at `/delegation-context`, derived from the trusted Worktree root's `.delegations/<run_id>` sibling directory in worker/daemon namespaces. Include the digest in runtime policy inspection and workload parsing. Never mount the whole artifact store. The parent can read the child patch but applies it deliberately; never auto-apply it. Omit delegation tools in invocation 2 and all children; enforce the limit again in storage. Recheck current credential availability and worker execution policy without changing recorded Agent inputs. Run affected package tests with `-race -count=1`.
- [x] **Step 5: Commit.** `feat(delegation): continue parent from durable child outcomes`.

### Task 6: Cancellation and restart recovery

**Files:** Modify `internal/postgres/run_cancel.go`, `run_cancel_test.go`, `queue.go`, `queue_test.go`, `delegation_continuation.go`, `internal/execution/delegation.go`, `cleanup.go`; create `internal/postgres/delegation_recovery_test.go` and extend `worker_process_test.go` and delegation fixtures.

**Interfaces:** Preserve `postgres.CancelRun` and `Queue.Acquire` public signatures. Add delegation-aware cases to their existing transactions. Use delegation relationships, not arbitrary `parent_run_id` links, for cancellation and cleanup.

- [x] **Step 1: Write failing tests.** `TestDelegationCancelRaces`, `TestDelegationRecoveryAtHandoffBoundaries`, and `TestDelegationOrphanedSuspensionCleanup` assert:

  ```go
  if parentCancelled && continuationsAfterCancellation != 0 { t.Fatal("continued cancelled parent") }
  if childCreates > 1 || activeInvocationReplays != 0 { t.Fatal("ambiguous work replayed") }
  if recoveryFinished && retainedUnownedWorkspaces != 0 { t.Fatal("cleanup stranded") }
  if unrelatedPRReviewCancelled { t.Fatal("used generic parent link for cascade") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/postgres -run 'TestDelegation(Cancel|Recovery|Orphaned)' -count=1 -v`.
- [x] **Step 3: Implement cancellation and bounded cleanup recovery.** Cancel parent before its unfinished delegated child using stable parent-then-child lock ordering; preserve completed history. Cancelling only the child delivers its cancelled outcome to the eligible parent. Parent cancellation closes its delegation as `cancelled`; other parent terminal outcomes close it as `completed`, preserving the separate child outcome. Add queue selection for terminal, ownerless parents with a suspended/pending unreleased Workspace; claim them for cleanup only with existing recovery limits. Preserve `ReleaseClaim`'s terminal/released checks. A safely parked parent survives worker restarts; an expired claim during an active/ambiguously started invocation fails and cleans up without invoking the model again. Shutdown cannot release a claim while leaving unverified resources.
- [x] **Step 4: Verify fault boundaries.** Inject failure before/after checkpoint byte publication, exact container removal, admission commit/acknowledgement, claim release, child terminal write, child cleanup, continuation claim/input persistence, and resumed container allocation. Verify no child before committed admission, one child afterward, safe terminal decisions, and retained original artifacts. Include policy mutation and Task creation concurrently to expose lock inversions. Run the Task 6 tests with `-race -count=1`; use real worker-process crash fixtures for the selected durable boundaries, not only mocked method returns.
- [x] **Step 5: Commit.** `fix(delegation): recover suspended work and serialize cancellation`.

### Task 7: Publication guards, usage, and observable Run state

**Files:** Extend the new, unreleased feature migration from Task 2 with publication guards; if that revision has already been applied outside disposable tests, add a forward revision instead. Never edit historical `0011.sql`, `0012.sql`, or `0015.sql`. Modify `internal/integrations/github_delivery.go`, `github_delivery_test.go`, `linear_activities.go`, `linear_activities_test.go`, `linear_updates.go`, `linear_updates_test.go`, `internal/httpapi/api.go`, `delegations.go`, `delegations_test.go`, `internal/postgres/execution.go`, `internal/controlmcp/server_test.go`, `contracts/openapi.json`, `apps/web/src/generated/api.ts`, `apps/web/src/use-run-events.ts`, `apps/web/src/run-events.test.ts`, and `playwright.console.config.ts`. Add `internal/integrations/delegation_test.go` and `tests/browser/delegation.spec.ts` using the existing console fixture/configuration.

**Interfaces:** Add optional `delegation` linkage and `invocation` to the existing Run execution response, consumed by control MCP `get_run`. Preserve the existing usage response shape. Server-generated execution Events include their invocation number; historical events without one belong to invocation 1. Add nonterminal `delegation.requested`, `delegation.created`, `delegation.rejected`, `run.suspended`, `run.continued`, and `workspace.suspended` to event contracts/subscriptions where applicable.

- [x] **Step 1: Write failing tests.** `TestDelegationPublicationRootOnly`, `TestDelegationUsageByInvocation`, `TestDelegationExecutionRead`, and the browser case `delegation stays live across parent suspension` pin these outcomes:

  ```go
  if childAutomaticDeliveries != 0 || finalsDuringHandoff != 0 { t.Fatal("premature publication") }
  // Invocation 1: cumulative samples 3/4 then 10/20; invocation 2: 7/8; child: 100/200.
  if usage.InputTokens != 17 || usage.OutputTokens != 28 { t.Fatal("usage counted incorrectly") }
  if resumedProviderRequestID != originalProviderRequestID { t.Fatal("lost provider identity") }
  ```

- [x] **Step 2: Observe red.** Run `go test ./internal/integrations ./internal/httpapi ./internal/controlmcp -run 'TestDelegation' -count=1 -v` and the new focused browser case.
- [x] **Step 3: Implement eligibility and projection.** Add explicit delegated-child exclusions to `queue_github_run_delivery`, `queue_linear_run_update`, and native session publication eligibility. Recheck automatic eligibility in provider delivery processing before external writes, including previously queued candidates; do not change explicitly authorized manual publication or PR-review behavior. Keep root native Linear request/session identity throughout waiting and continuation. `waiting_for_input` remains running progress for that integration; suspension is never a final response. Sum only the latest cumulative usage sample per parent invocation; exclude child usage. Persist invocation 2's usage under its own identity, deduplicating replay. Keep checkpoint/context artifacts outside normal final publication. Expose linkage through existing private Run reads and subscribe to added nonterminal Events without closing SSE or resetting its sequence cursor. Do not add a Task/Kanban feature or a separate delegation dashboard.
- [x] **Step 4: Verify.** Run the focused integration/HTTP/MCP tests, existing Linear/GitHub/PR-review tests, `corepack pnpm contracts:generate`, `corepack pnpm contracts:check`, `corepack pnpm typecheck`, `corepack pnpm test`, and `corepack pnpm exec playwright test --config playwright.console.config.ts tests/browser/delegation.spec.ts`. Set that config's `testMatch` to `["console.spec.ts", "delegation.spec.ts"]` so the added test actually runs. Browser checks must use fixtures, not live providers.
- [x] **Step 5: Commit.** `feat(delegation): expose continuation state and preserve publication boundaries`.

### Task 8: Complete fake execution acceptance and operator documentation

**Files:** Extend `internal/postgres/supervisor_delegation_test.go`, `worker_process_test.go`, `internal/codexworkload/subscription_test.go`, and `internal/runtimes/real_test.go`; create `internal/testsupport/delegation.go` for shared deterministic fixtures. Modify `internal/testsupport/docker.go` only for simulator behavior exercised by these tests. Extend `internal/fakeworkload/workload.go`, `workload_test.go`, `internal/backends/fake.go`, and `fake_test.go` with one bounded optional `delegation_request` in fake backend configuration, decoded using Task 1's contract; emit it only during invocation 1 of a permitted root. Fake configuration cannot bypass platform admission. Update `docs/development/codex-backend.md`, `control-mcp.md`, and `docs/architecture/foundation.md`.

**Interfaces:** Reuse the completed production handoff path. Test fixtures emit the same validated request/result protocol as real workloads; no separate test orchestrator or provider credentials.

- [x] **Step 1: Write failing acceptance tests.** `TestDelegationFullWorkerLifecycle` and `TestDelegationDockerIsolation` launch a fake parent that changes source, delegates, then consumes a child's report/patch in invocation 2. Assert exactly two Run records, three nonoverlapping invocations, one child Task, unchanged parent attempt, parent-only final completion/publication eligibility, child-relative diff, separate writable workspaces, read-only context, correct usage, released containers/worktrees/claims, and retained verified artifacts. Include a child failure and cancellation variant. The fake parent explicitly chooses whether to apply the patch; the platform never does so automatically.

  ```go
  if runs != 2 || invocations != 3 || invocationsOverlap { t.Fatal("wrong execution chain") }
  if sharedWritableWorkspace || contextWritable { t.Fatal("broken isolation") }
  if parentCompletionEvents != 1 || unreleasedResources != 0 { t.Fatal("incomplete lifecycle") }
  ```
- [x] **Step 2: Observe red.** Run the new acceptance tests with the disposable database and Docker fixture. Confirm they exercise the actual queue, supervisor, persistence, and cleanup path rather than directly calling continuation methods.
- [x] **Step 3: Complete fixtures and documentation.** Describe opt-in owner policy, `delegate_task`, `pending_handoff`, automatic continuation, cancellation, failed-child outcomes, immutable source, and the fresh-invocation context limitation. Document disabled-by-default rollout and existing recovery diagnostics. Add no production launch or deployment script. Reconcile the spec's review status only after the plan is accepted.
- [x] **Step 4: Run release checks once.** Match CI: `gofmt -l cmd internal contracts` produces no paths; `go vet ./...`, `go build ./...`, and `go test -race ./... -count=1 -timeout=300s` pass with `TEST_DATABASE_URL` and `CIRCULAR_RUN_DOCKER_TESTS=1`. Run `corepack pnpm contracts:check`, `corepack pnpm typecheck`, `corepack pnpm test`, `corepack pnpm build`, and focused console/delegation browser tests. Check the existing Task planning/content-lock, direct launch, Linear, PR-review, and crash-recovery suites actually ran. Record skips or unavailable infrastructure explicitly; do not claim real-model validation from fake tests.
- [x] **Step 5: Commit and review the integrated branch.** `test(delegation): verify complete parent child continuation`. Review against the spec and the five Review Focus cases before proposing a PR. Live enablement remains a separate operational action.

## Completion and handoff

The feature is complete when an opted-in fake parent/child/parent flow passes through the real worker, durable storage, isolated runtime, and cleanup; cancellation/restart cases preserve single admission; publication and usage projections remain correct; and all required regression checks pass. No task is complete merely because its happy-path unit test passes.

Review this plan as one proposed v1 scope. Native execution is recommended because the eight tasks share storage and resource-state interfaces; implement sequentially in one isolated checkout, then review the integrated change. Subagent-driven execution is an alternative if independent review after each task is preferred. Choose the execution method after plan review; no implementation has been started by writing this document.

## Verification record

Verified on 2026-10-14 in the isolated `feat/agent-delegation` checkout above
`7443e137ad26fe101997d677df6c891317ecf74c`. Formatting, vet, build and module
consistency checks passed. The final full Go race suite passed with disposable
PostgreSQL and real Docker enabled: 511 top-level tests, 1,227 passing test/subtest
records across 24 packages. Only the standalone subprocess helper skipped in the
parent harness, as intended. Contracts, TypeScript, 47 web tests, production build
and 21 console/delegation browser tests passed. The Codex runner build stage passed.

The independent review found chained symlinks could escape a restored checkpoint.
`TestDelegationRestoreValidatesSymlinkGraph` reproduced both ordinary and forward
reference escapes plus cycles before the fix, then passed with complete link-graph
validation. Safe internal chains and dangling internal links remain supported.
The full Go suite passed again after this fix.

The Minor fixture inconsistency was fixed on 2026-10-16: invocation sealing now
uses the fake backend's existing validation before JSON normalization. Regression
tests first reproduced acceptance of duplicate keys and fractional delays, then
passed with the fix. Valid requests, null configuration and ignored options remain
supported. The invocation snapshot and fake backend tests also passed.

No live-model or provider validation, publication, merge or deployment was
performed during implementation.

### GitHub integration on 2026-10-16

The draft PR branch starts from GitHub `main` at
`cf485db8ec0c296f01cba9c84dcb8f6c2a8375aa`. Only the delegation delta was ported
from the isolated implementation; the merged PR-review Git isolation changes
remain intact. HTTP/MCP tool counts and generated contracts reflect this base.

Task planning migrations `0016` and `0017` and their upgrade tests are included
byte-for-byte from the frozen `7443e137` baseline. This preserves the migration
identities used by the existing Task/Gantt release; delegation remains `0018`.
Task planning handlers, components and later changes stay in their separate
branch. Production rollout must first reconcile that application's release with
GitHub main; deploying this branch alone would omit its Task/Gantt features.

The reconciled branch passed formatting, module verification/tidy, vet and build.
Its full Go race suite passed with disposable PostgreSQL and real Docker: 488
top-level tests, 1,194 passing test/subtest records across 24 packages. Only the
standalone worktree subprocess helper skipped in its parent harness. This includes
the existing real Docker PR-review Git isolation regression, complete
parent/child/parent execution, cancellation/recovery, and migration upgrades.

Contracts, TypeScript, all 29 frontend tests and the production frontend build
passed. The real API/MCP browser test passed. Of 20 console/delegation browser
tests, 19 passed initially; the remaining Project-context test lost module requests
with Chromium `ERR_NETWORK_CHANGED` while Docker fixtures were active. It passed
on its sole executed retry after those fixtures finished, without code changes.
The retained trace identifies the network interruption. Live models/providers and
production rollout remain untested in this integration step.

A focused independent review found no issues in the validation fix, contract/MCP
reconciliation, preserved reviewer isolation, or migration ordering. It approved
publication as a draft PR with the production integration prerequisite above.
