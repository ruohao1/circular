# Task completion policies and evidence implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans for the recommended native execution, or superpowers:subagent-driven-development if the user chooses it. Implement and verify one task at a time. Steps use checkbox syntax for tracking.

**Goal:** Enforce each Task's planned completion contract across native/external work and every status-writing surface, with trustworthy PR readiness evidence.

**Architecture:** Add bounded Go completion types/evaluation and extend the existing PostgreSQL work lifecycle. Reuse the independent PR reviewer, GitHub integration workers, HTTP-backed MCP, and console; generalize review sources to include external Work sessions. Durable reconciliation separates Run-locked native hooks from planning-locked Task transitions.

**Tech Stack:** Existing Go 1.27.1, pgx v5/PostgreSQL, MCP Go SDK, React/TypeScript, OpenAPI, Vitest, and Playwright. No new dependencies.

**Spec:** [Task completion policy design](../specs/2026-10-20-task-completion-policy-design.md).

**State:** Approved 2026-10-21 for native implementation and one final independent whole-branch review, based on merged PR #8 (`ea18ddd`). Review-required Tasks reach Done only after a verified merge. This plan does not authorize deployment or merging the implementation PR.

## Global constraints

- External coding agents are the primary clients. Native execution uses the same rules.
- No new runtime dependency, daemon, public authentication system, automatic repair loop, or merge automation.
- The first tracked claim freezes the Task policy and required checks; reopening does not unlock it.
- No provider I/O inside a database transaction.
- Project → Task → Run → session lock order for planning/native administration; native hooks keep Run → session only. Preserve provider and review-settings lock ordering from the spec.
- Positive cached readiness expires after 60 seconds; provider errors make readiness unavailable.
- Only completed/success required checks pass. Critical/high/medium review findings block; low findings are advisory.
- No placeholder coding Run for external work; PR review Runs never acquire coding ownership.
- Private endpoints remain private. Client metadata and `actor` arguments never grant authority.
- Keep this plan, spec, and implementation on `feat/isq-473-completion-policy` in one implementation PR. No standalone plan PR.

## Review focus

1. A lost finish response followed by reopen/reassignment must replay history without promoting or releasing the newer attempt — Task 2.
2. A newer queued CI rerun or failed review must defeat an older green result, including across pagination and a base-only change — Tasks 4–5.
3. A native handoff, cleanup failure, or worker restart must not complete the parent early or invert locks — Tasks 2–3.
4. A retained v1 native review and a new external review must both execute/replay without fabricating ancestry or sharing credentials — Task 4.
5. Manual/provider terminal-stage updates and repository check edits must not weaken the frozen contract or release an active owner — Tasks 1, 2, and 6.

## File ownership and test environment

All paths are relative to this feature worktree. `internal/completion/` owns pure types/validation/verdicts; `internal/postgres/task_completion*.go` owns persisted completion and locking; `internal/integrations/task_completion*.go` owns provider orchestration. Extend adjacent existing files for callers, rather than adding generic wrappers.

Before implementation, verify migration numbering and rebase on the current main if needed. Planned new migrations are `0020.sql` (policy/work completion) and `0021.sql` (review source/readiness); renumber together if another feature has used them. Do not modify released migrations.

Use an explicitly disposable PostgreSQL database in `TEST_DATABASE_URL`. `testsupport.Database` isolates schemas; a skipped DB suite is not a pass. Docker tests require an available disposable runtime and `CIRCULAR_RUN_DOCKER_TESTS=1`. Use Go/pnpm versions from `go.mod`/`package.json`; do not reuse the prior issue's stopped test database. Local fixture setup is part of implementation, not this planning commit.

## Shared types and service seams

Define these once; downstream tasks consume them. Use existing UUID, timestamp, canonical JSON/fingerprint, and PR identity conventions.

| Type in `internal/completion/types.go` | Fields |
| --- | --- |
| `Requirement` | `Key, Description string` |
| `Policy` | `Mode string`, `Requirements []Requirement` |
| `Evidence` | `Key, Detail string`, `Links []string` |
| `Report` | `Result string`, `Evidence []Evidence`, `PullRequestNumber *int` |
| `RequiredCheck` | `WorkflowID, AppID, WorkflowPath, Name string`; IDs are canonical positive decimal strings. |
| `CheckConfig` | `Revision int64`, `Checks []RequiredCheck` |
| `Contract` | `Policy Policy`, `CheckConfig CheckConfig`, `Fingerprint string` |
| `Blocker` | `Code, Message, Action string` |
| `CheckResult` | Required-check identity; workflow run/job/check IDs and attempt; tested SHA; status/conclusion; provider URL and completion time. All provider IDs remain exact integer/decimal values. |
| `Observation` | Session/binding IDs, binding revision, contract fingerprint, provider identity generation; `PR prreviews.PRIdentity`; PR state/draft flag; tested commit/tree and ancestry proof; check results; checked time; availability/error code; merged flag/SHA and merge proof. |
| `Input` | Frozen contract and accepted report (nullable); current session identity/outcome/release/recovery state; observation (nullable); latest requested review identity/status and `prreviews.Review` (nullable); current Task stage. |
| `Verdict` | `NextStage, MergeReadiness string`, `CanComplete bool`, `Blockers []Blocker`, `Advisories []string` |
| `View` | Safe Task/session/policy/report/PR/evidence/review projections, `Verdict`, observed time; no session secret or raw provider payload. |

`MergeReadiness` values: `not_applicable`, `blocked`, `ready`. `NextStage` is a proposed stage, never permission for a caller to skip ownership/revision checks. Inspect/refresh does not itself move the Task to Done. The guarded explicit move does so after a confirmed merge. Native/external successful finish applies direct Done or In Review only.

Stable blocker codes include `policy_required`, `checks_unconfigured`, `report_required`, `evidence_missing`, `owner_active`, `cleanup_pending`, `recovery_required`, `attempt_superseded`, `pr_required`, `pr_invalid`, `pr_draft`, `provider_unavailable`, `permission_required`, `evidence_stale`, `ci_missing`, `ci_pending`, `ci_unsuccessful`, `ci_revision_unverified`, `review_required`, `review_incomplete`, `review_outdated`, `review_findings`, `merge_required`, and `merge_revision_unverified`. Every blocker includes a concrete next action; errors never project provider credentials.

---

### Task 1: Freeze explicit policy and required checks at admission

**Files:** Create `internal/completion/types.go`, `policy.go`, `report.go`, `policy_test.go`, `report_test.go`; `internal/postgres/task_completion_policy.go`, `task_completion_policy_test.go`; `internal/migrate/0020.sql`, `task_completion_test.go`. Modify `internal/postgres/work_claims.go`, `run_launch.go`, `task_planning.go`, `delegation_admission.go`; `internal/httpapi/discovery.go`; `internal/integrations/request_launch.go`; matching tests and `internal/testsupport/pr_review.go`.

**Interfaces:** Produce `completion.ValidatePolicy(Policy) error`, `completion.ValidateReport(Policy, Report) error`, `completion.SealContract(Policy, CheckConfig) (Contract, error)`. Produce `postgres.SetTaskCompletionPolicy(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, expectedRevision int64, policy completion.Policy) error`, `postgres.SetRepositoryChecks(ctx context.Context, tx pgx.Tx, repositoryID uuid.UUID, expectedRevision int64, config completion.CheckConfig) (completion.CheckConfig, error)`. Existing `CreateRun` and `ClaimExternalWork` signatures stay intact.

- [x] **1. Write failing policy, migration, and admission cases.** Use these assertions in the new table-driven tests:

  ```go
  // TestCompletionPolicyBounds
  // valid modes: review_required, direct_completion; 20 unique requirements pass.
  // 21 requirements, duplicate/invalid keys, empty descriptions, or unknown mode fail.
  // TestCompletionReportBounds
  // required keys all present => nil; missing/duplicate/unknown key => error.
  // empty result, >128 KiB, invalid UTF-8, non-HTTPS/credential-bearing URL => error.
  // TestCompletionAdmissionFreeze
  // unset policy or missing review checks => no Run/session inserted.
  // first claim => frozen contract; later settings change => identical contract digest.
  // reopen then policy weakening => conflict; metadata-only edit => allowed.
  // TestCompletionUpgrade
  // live owner/unsafe cleanup => upgrade rejected; legacy completed Task unchanged.
  ```

- [x] **2. Verify RED.** Run `go test ./internal/completion ./internal/migrate ./internal/postgres -run 'Completion(Policy|Report|Admission|Upgrade)' -count=1`. Expect missing implementation or failing assertions, with DB tests actually executing.
- [x] **3. Implement the contract and migration.** Add nullable policy/frozen contract/current-completion-session fields, versioned Repository check settings, and completion report/reconciliation storage with scoped FKs. Freeze in the existing atomic admission transaction, before inserting a session; preserve exact legacy command replay before new eligibility checks. Enforce frozen-contract immutability in Go and DB constraints/triggers. Do not backfill invented policies/evidence.
- [x] **4. Preserve launch callers.** Discovery explicitly saves its displayed direct/report preset. Delegation saves the spec's direct/handoff child preset before admission, preserving parent locks and continuation. Linear requests needing policy stay in an actionable waiting state instead of silently choosing one or retrying forever as generic failures. Add explicit policies/check fixtures to existing launch tests; tests about unset policy remain unset.
- [x] **5. Verify GREEN and commit.** Run the RED command plus `go test ./internal/postgres ./internal/httpapi ./internal/integrations -run 'Launch|Claim|Delegation|Discovery|Request' -count=1`; all pass with no new skips. Commit `feat: freeze task completion contracts`.

### Task 2: Apply reports through one guarded transition path

**Files:** Create `internal/completion/evaluate.go`, `evaluate_test.go`; `internal/postgres/task_completion.go`, `task_completion_test.go`; `internal/integrations/task_completion.go`, `task_completion_test.go`. Modify `internal/postgres/work_reports.go`, `work_reports_test.go`, `work_run_hooks.go`, `work_run_hooks_test.go`, `task_planning.go`, `task_planning_test.go`; `internal/httpapi/background.go`.

**Interfaces:** Consume Task 1 contracts. Produce `completion.Evaluate(Input, time.Time) Verdict`; `postgres.InspectTaskCompletion(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, now time.Time) (completion.View, error)`; `postgres.CheckTaskCompletionMove(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, target string, now time.Time) error`; `(*integrations.Service).ProcessTaskCompletion(context.Context) (bool, error)`. Extend `WorkFinishInput` with `Report *completion.Report` using `omitempty` so nil-report legacy digests remain unchanged. Resolve historical replay before new report requirements; new success derives receipt summary from `Report.Result` and rejects a conflicting supplied summary. Native report retention is `(*postgres.RunResources).SaveCompletionReport(invocation int, report completion.Report) error`.

- [x] **1. Write failing state/replay/concurrency tests.** Cover valid direct finish → completed; valid review finish → in_review without a PR; missing required external evidence → error with owner unchanged; native success without report → blocker; failed/cancelled → no promotion. Race finish with reassignment, reopen, and policy edit. Replay a lost success response after a new claim: same receipt, newer owner/stage untouched. Attempt generic creation/move to In Review/Done and cancellation with an active owner: rejected. Same-stage reorder still passes.
- [x] **2. Verify RED.** Run `go test -race ./internal/completion ./internal/postgres ./internal/integrations -run 'Completion|WorkFinish|WorkReassign|TaskMove' -count=1` and confirm the new assertions fail.
- [x] **3. Implement finish and stage guard.** Route external finish through Project → Task → session locks, canonical report validation, replay check, terminal outcome/release, report persistence, and shared transition in one transaction. Only the current completion pointer may promote. Keep the existing `MoveTask` API, calling the guard under its planning locks; generic stage creation must use the same eligibility rule. Reopen invalidates the pointer. Typed conflict errors carry blockers and leave ownership unchanged.
- [x] **4. Implement durable native reconciliation.** Run-locked hooks save candidate/outcome/pending state only. The background process selects a candidate without taking a session lock first, then locks Project → Task → Run → session and rechecks it. Apply once after cleanup, or persist the blocker. Crash/restart retries, recovery holds, two background processes, superseded attempts, and final invocation selection must be covered. Do not call review-settings mutation or provider APIs while holding these locks.
- [x] **5. Verify GREEN and commit.** Repeat the RED command plus the existing run cancellation/recovery/delegation tests. Check the ownership rows, completion receipt, and Task revision in each concurrency outcome. Commit `feat: enforce task completion transitions`.

### Task 3: Submit native completion reports without changing process semantics

**Files:** Create `internal/completion/spool.go`, `spool_test.go`; `internal/agenttools/completion.go`, `completion_test.go`; `internal/execution/task_completion_test.go`. Modify `internal/agenttools/server.go`; `internal/backends/backend.go`, `codex.go`, `codex_test.go`; `internal/codexworkload/workload.go`, `workload_test.go`; `internal/execution/protocol.go`, `supervisor.go`; `internal/postgres/execution.go`, `invocations.go`; relevant fake workload/backend fixtures.

**Interfaces:** Consume `Report`, `Contract`, and `SaveCompletionReport`. Add a frozen completion contract to coding `backends.Input`/`postgres.ProvisioningContext`; preserve old invocation serialization/hashes with omitted optional fields or an explicit versioned decoder. Produce `completion.NewSpool(directory string, contract Contract) (*Spool, error)`, `(*Spool).Submit(Report) error`, and `(*Spool).Read() (Report, error)`. Register local `submit_task_result(report)` with `agenttools.AddCompletion(server *mcp.Server, spool *completion.Spool)`, following existing spool registration. Produce normalized event `task.completion.reported` bound to the trusted invocation; public agent arguments contain no Task/session identity, stage, or verdict.

- [x] **1. Write failing local-tool/output tests.** Valid report persists; malformed/missing evidence returns a correctable tool error. Identical report replay is safe, conflicting final report is rejected. The coding container cannot choose another Task or policy. A plain final text message/process exit does not manufacture a report. Failed/cancelled process after report cannot promote. A delegation handoff report cannot finish the parent; the continuation's final report can. PR review workloads cannot call this coding completion tool.
- [x] **2. Verify RED.** Run `go test ./internal/completion ./internal/agenttools ./internal/backends ./internal/codexworkload ./internal/execution -run 'Completion|Delegation|PRReview' -count=1` and verify new cases fail for the missing behavior.
- [x] **3. Implement using the existing private spool and output framing patterns.** Register the local tool only for coding workloads with a frozen contract, pass the contract in trusted input, and validate bounded regular files/immutable submission. Reuse `reviewOutput` serialization for trusted tool records instead of mixing writes into partial CLI JSON. Bind persisted report to the final invocation in the worker; handoff candidates stay out of parent completion. Retain report and actual process outcome independently. Update fake fixtures to emit explicit valid reports only where the scenario expects completion.
- [x] **4. Verify GREEN and commit.** Repeat the RED command and run relevant Docker-backed native/delegation execution tests with the disposable runtime. Assert cleanup and parent continuation still occur when a report is missing. Commit `feat: report native task results through local tools`.

### Task 4: Reuse the independent reviewer for external Work sessions

**Files:** Create `internal/migrate/0021.sql`; extend `internal/migrate/pr_review_test.go`. Modify `internal/prreviews/types.go`, `snapshot.go`, `context.go`, `workflow.go` and tests; `internal/postgres/pr_reviews.go`, `pr_review_artifacts.go`, `pr_review_linear.go` and tests; `internal/integrations/pr_review_launch.go`, `pr_review_publish.go`, `pr_review_linear.go` and tests; `internal/execution/pr_review_context.go`, `pr_review_context_test.go`; `internal/testsupport/pr_review.go`, `pr_review_completion.go`.

**Interfaces:** Introduce `prreviews.Source{RunID *uuid.UUID, WorkSessionID *uuid.UUID}` with exactly-one validation and a canonical key. Existing native source endpoints adapt to `Source{RunID: ...}`. Store `Source` on external snapshot v2; keep native v1 JSON/hash/context compatibility. Add `(*postgres.PRReviewStore).SourceForWork(ctx context.Context, sessionID uuid.UUID, reviewerID uuid.UUID) (prreviews.LaunchSnapshot, error)` and Task-scoped history; generalize `Replay`/launch identity to the typed source while preserving native replay keys.

- [x] **1. Write failing external/compatibility tests.** A valid external completion PR creates one real PR review Run with null parent and no coding Run/Work session. Cross-Task/Repository/session input fails. A coding Agent cannot review its own native Run. A client-uploaded report is never independent review evidence. Keep a serialized v1 snapshot/context fixture and assert its fingerprint and artifact checks stay identical. Test latest-review-pending/failed/cancelled/incomplete blocking despite an older clean review. Native stop/revocation and publication identity fences continue to work.
- [x] **2. Verify RED.** Run `go test ./internal/prreviews ./internal/migrate ./internal/postgres ./internal/integrations ./internal/execution -run 'Review(Source|External|Legacy|Replay|Publication|Context)|CompletionReview' -count=1`; verify new cases fail.
- [x] **3. Generalize the source end to end.** Add nullable native/external FKs and deferred exactly-one/scope/kind constraints, external intent/replay indexes, and versioned snapshot decoding. Load external evidence from its accepted report with an explicit agent-claim limitation. Keep the same isolated reviewer/runtime/artifact validation. Do not call native external-effect reservations with a zero Run ID; fence external publication through its real Task/Repository identity. Adapt the Linear inheritance trigger so external work never invents a native Linear session. Scope latest review selection to the current source and PR; use that attempt's sealed reviewer fingerprint. All new policy-managed review intents take the Task lock, invalidate old readiness, and reject completed/superseded sources. Test an intent racing the final Done transaction.
- [x] **4. Verify GREEN and commit.** Repeat the RED command, existing complete PR review suites, and a real fixture reviewer-container test. Inspect DB rows to prove external work created no coding Run. Commit `feat: review external task pull requests independently`.

### Task 5: Collect authoritative CI and calculate revision-bound readiness

**Files:** Create `internal/integrations/task_completion_github.go`, `task_completion_github_test.go`; `internal/postgres/task_completion_evidence.go`, `task_completion_evidence_test.go`. Extend `0021.sql` with session-scoped binding/readiness tables; modify `internal/integrations/github_identity.go`, `github_identity_test.go`, `pr_review_freshness.go`, `task_completion.go`; `internal/httpapi/background.go`; `internal/completion/evaluate.go`, `evaluate_test.go`; `internal/testsupport/github_review.go`.

**Interfaces:** Produce `(*integrations.Service).BindTaskPullRequest(ctx context.Context, taskID, sessionID uuid.UUID, expectedRevision int64, number int) (completion.View, error)`, `InspectTaskCompletion(ctx context.Context, taskID uuid.UUID) (completion.View, error)`, `RefreshTaskCompletion(ctx context.Context, taskID uuid.UUID) (completion.View, error)`, and `ProcessTaskCompletionFreshness(context.Context) (bool, error)`. Introduce token purpose `GitHubCompletionRead`. Persistence stores `Observation` only under matching session/binding/contract/provider-generation/lease preconditions.

- [x] **1. Write failing provider/evaluator fixtures.** Pin these exact results:

  ```go
  // TestCompletionReadiness
  // current PR + all 3 configured completed/success jobs + complete clean review => ready.
  // any missing/pending/skipped/neutral/failed/cancelled/timed_out required job => blocked.
  // age 60 seconds or more, future checked_at, unknown base/head, provider error => blocked.
  // base-only/head-only change; latest review failed; medium finding => blocked.
  // low-only findings => ready with advisory; agent-reported green tests alone => blocked.
  // TestCompletionGitHubEvidence
  // latest queued workflow rerun beats older success; paginate before selecting.
  // wrong app/workflow, duplicate ambiguous job, page cap/error => blocked.
  // verified current merge parents, or head containing current base => eligible CI SHA.
  // unprovable tested base => ci_revision_unverified; changed PR during fetch => discard.
  // TestCompletionMergeProof
  // ready but unmerged => CanComplete false; matching proven merge => true.
  // closed-unmerged, unrelated merge tree, missing lineage => false.
  ```

- [x] **2. Verify RED.** Run `go test -race ./internal/completion ./internal/postgres ./internal/integrations -run 'Completion|GitHubIdentity' -count=1` and confirm the new evidence cases fail.
- [x] **3. Implement bounded collection and conditional persistence.** Reuse the canonical PR inspector, installation identity/token path, and refresh lease pattern. Discover numeric workflow/app identities from GitHub when saving explicit configuration. Fetch all applicable run/job/check pages without filtering away pending attempts; select newest run/latest attempt. Validate tested-SHA ancestry/merge-parent proof. Read PR identity twice and fail closed on changed/unavailable evidence. Use 30-second lease, 25-second provider deadline, one-minute scheduling and 60-second positive TTL. Limit each collection to 20 pages of 100 records and each body to 4 MiB; provider truncation or remaining pages at the cap block readiness. Store only safe evidence IDs/status/revisions/links/times, preserving IDs as decimal strings.
- [x] **4. Implement verdict and verified-merge projection.** Require the current session/contract, latest independent retained report and fresh provider observation. Keep review/CI and policy blockers separate from `merge_required`. For merged PRs use the accepted pre-merge base/head and lineage/tree proof from the spec, not the current target branch tip. No automatic Done in refresh. Direct-completion evaluation never requires GitHub. A missing PR after native finish remains actionable In Review.
- [x] **5. Verify GREEN and commit.** Repeat the RED command plus existing freshness, token-purpose/identity-generation, and publication tests. Include two refresh workers, lease expiry, reconnect during fetch, and a slow stale response overwriting a newer one. Commit `feat: verify task completion with current GitHub evidence`.

### Task 6: Expose policy and verdict through shared HTTP/MCP contracts

**Files:** Create `internal/httpapi/task_completion.go`, `task_completion_test.go`; `internal/controlmcp/task_completion.go`, `task_completion_test.go`. Modify `internal/httpapi/api.go`, `task_planning.go`, `task_planning_read.go`, `pr_reviews.go`, `schema.go` as required; `internal/controlmcp/server.go`, `task_planning.go`, `mutations.go`, `pr_reviews.go`; `contracts/openapi.json`; regenerate `apps/web/src/generated/api.ts`; update `apps/web/src/api.ts`.

**Interfaces:** Add these routes using exact IDs/preconditions from Tasks 1–5:

| Route | Purpose |
| --- | --- |
| `PUT /api/v1/tasks/{task_id}/completion-policy` | Policy + expected Task revision; unavailable after freeze. |
| `GET/PUT /api/v1/repositories/{repository_id}/completion-checks` | Explicit check configuration with config revision. |
| `GET /api/v1/tasks/{task_id}/completion` | Safe completion `View`. |
| `POST /api/v1/tasks/{task_id}/completion/refresh` | Refresh authoritative evidence, return `View`. |
| `PUT /api/v1/tasks/{task_id}/pull-request` | PR number + expected Task revision/current session. |
| `GET /api/v1/tasks/{task_id}/pr-reviews/prepare` | Existing prepared review shape for current completion source. |
| `GET/POST /api/v1/tasks/{task_id}/pr-reviews` | Task review history / launch with existing fingerprint/replay request. |

MCP tools: `get_task_completion`, `refresh_task_completion`, `bind_task_pull_request`, `prepare_task_pr_review`, `list_task_pr_reviews`, `launch_task_pr_review`. No policy weakening or external lifecycle tools. Existing `move_task` delegates to the same guarded HTTP move. Existing Run-scoped PR review routes/tools remain compatible.

- [x] **1. Write failing boundary/parity tests.** HTTP/MCP/console-backed moves return the same blocker codes and keep the owner/session unchanged. A provider-origin caller using shared `MoveTask` cannot bypass it. Creation with terminal status, frozen policy rewrite, arbitrary PR host/project identity, unknown fields/oversized reports, and forged actor/client metadata fail. Read-only MCP cannot refresh/bind/launch. Inspect never exposes credentials. Done move refreshes outside the transaction then checks revision/freshness again under locks; an intervening new claim blocks it.
- [x] **2. Verify RED.** Run `go test ./internal/httpapi ./internal/controlmcp -run 'Completion|TaskMove|TaskCreate|PRReview' -count=1`; confirm route/behavior failures, not environment skips.
- [x] **3. Implement thin handlers and strict typed contracts.** Use the existing transport/service patterns, existing project/Repository scope checks, and bounded validators. Keep non-stage metadata updates independent. Expose the policy/frozen state on task planning reads; expose only authoritative verdict fields for presentation. Return validation errors for malformed input and typed conflict/blocker responses for valid but prohibited transitions. Do not implement Linear status ingestion or external claim/heartbeat/finish endpoints here; document the shared service seam those issues must consume.
- [x] **4. Regenerate and verify GREEN.** Run `corepack pnpm contracts:generate`, `corepack pnpm contracts:check`, `corepack pnpm typecheck`, and the RED command. Verify legacy Run review endpoints and MCP read-only inventories explicitly. Commit `feat: expose task completion policy and evidence`.

### Task 7: Show the contract, evidence, and actionable blockers in the console

**Files:** Create `apps/web/src/components/tasks/task-completion.tsx`, `apps/web/src/components/repository-completion-checks.tsx`; `apps/web/src/lib/task-completion.ts`, `task-completion.test.ts`; `tests/browser/task-completion.spec.ts`. Modify `apps/web/src/components/tasks/task-detail.tsx`, `use-task-planning.ts`, `task-board.tsx`; `apps/web/src/components/console/task-launcher.tsx`; `apps/web/src/pages/setup.tsx`, `components/run-pr-reviews.tsx`, `pr-review-report.tsx`, `github-run-publishing.tsx`, `run-github-delivery.tsx` where reused; `cmd/circular-e2e-stack/pr_review.go`, `main.go`, relevant browser fixtures.

**Interfaces:** Consume generated `View`, policy/config updates, and Task review API methods. `TaskCompletion` takes a Task ID and queries authoritative state. `RepositoryCompletionChecks` takes a Repository ID. UI helpers only format evidence/actions; they do not independently calculate readiness or authorize transitions.

- [x] **1. Write failing UI/browser scenarios.** Plan an unset Task, choose direct/report requirements, launch/finish it, and see Done. Review-required report shows In Review, a pending CI blocker, then Ready to merge after current checks/review; it stays In Review until a verified merge and explicit completion. Change base/head and see readiness clear. Display review severity, missing permissions, expired evidence, malformed external PR, frozen policy, and blocked drag/drop with a recoverable inline error. Keyboard/focus access works for all controls.
- [x] **2. Verify RED.** Run `corepack pnpm test` for the new formatting tests and `corepack pnpm test:e2e tests/browser/task-completion.spec.ts --workers=1` against the disposable stack. Confirm the missing UI/behavior failures.
- [x] **3. Implement using existing Task detail, fields, and PR review components.** Keep planning Ready and Ready to merge visibly separate. Link each blocker to setup, evidence refresh, PR binding, or review as appropriate. Display observation age and exact revision. Prefill the three Circular CI jobs only for the matching Repository and require the user to save the selection. Preserve explicit manual draft publishing even while readiness is blocked. Explain that policy-managed work uses manual publication; do not leave legacy automatic-publishing copy implying these new Tasks auto-publish.
- [x] **4. Verify GREEN and commit.** Repeat the focused tests plus `corepack pnpm typecheck` and related planning/review/setup browser scenarios. Inspect desktop and narrow Task detail for overflow, focus order, and error recovery. Commit `feat: show task completion requirements and readiness`.

### Task 8: Close migration/publication regressions and validate the whole feature

**Files:** Extend feature migrations/tests and affected fixtures where assertions reveal omissions; modify `internal/integrations/github_delivery_test.go`, `github_publish.go` only if required by the publication guard; `internal/controlmcp/github_delivery.go` descriptions; `docs/user-guide/pull-request-reviews.md`, `coding-agent.md`, `agents-and-runs.md`; `docs/development/integrations.md`, `control-mcp.md`, `vps-deployment.md`; `CONTEXT.md` for concise domain terms only. Keep this plan/spec updated with actual implementation decisions/results.

**Interfaces:** No new product service. Preserve existing manual `PublishRunPullRequest` behavior and native replay/retention contracts. Future ISQ-474 uses `WorkFinishInput.Report`; ISQ-478 uses guarded `MoveTask`. ISQ-476 documents clients only after ISQ-474 exists.

- [x] **1. Add targeted regression assertions before any final fixes.** Upgrade retained historical Tasks/reviews/receipts without rewriting fingerprints; reject mixed old-writer/active-owner upgrade. Confirm new policy-managed sessions are skipped by automatic GitHub publication triggers, while manual draft publication and historical pending-delivery reconciliation still work. Old native review/Linear publication fences remain intact. Verify discovery, delegation, request reactivation after policy setup, and native execution browser fixtures supply explicit policies/reports.
- [x] **2. Verify RED for each newly found gap, then fix its owner.** Run the specific test before the smallest production change. Do not add a parallel completion path to patch a failing caller. Update fixture semantics explicitly instead of disabling tests or globally defaulting policy to direct completion.
- [x] **3. Document operator/client behavior and rollout.** Explain the contract freeze, supported GitHub permissions/check source, conservative revision-proof blocker, manual publication, preserved historical records, and drain/stop/migrate/restart steps. Include an initial explicit check-selection example for Circular, without credentials or environment-specific IDs. Document native direct/handoff preset semantics and the remaining external lifecycle/status-sync issues.
- [x] **4. Run the full required gates once after the final change.** With the correct disposable DB/runtime, run:

  ```sh
  gofmt -l cmd internal contracts
  go mod verify
  go mod tidy -diff
  go vet ./...
  go build ./...
  go test -race ./... -count=1 -timeout=300s
  corepack pnpm contracts:check
  corepack pnpm typecheck
  corepack pnpm test
  corepack pnpm build
  corepack pnpm test:e2e --forbid-only
  ```

  Formatting/tidy emit no changes; all checks pass with DB/Docker suites executed. Run the existing Compose browser profile from `.github/workflows/ci.yml` with an owned project/volume, then clean up only that disposable stack. Do not rerun broad passing suites without a new change or unresolved concern.
- [x] **5. Review, commit, and open the implementation PR.** Use the selected execution workflow's independent whole-branch review. Fix material findings and rerun affected checks. Include spec/plan/code together, record exact-head test evidence, and link ISQ-473. Do not merge or deploy merely because the PR exists. Commit remaining documentation/fixtures as `test: verify completion policy across existing workflows`.

## Planning self-review and execution handoff

Coverage: policies/evidence/freeze/migration → Task 1; ownership/status bypass/native crash safety → Tasks 2–3; external independent review → Task 4; authoritative checks/freshness/merge proof → Task 5; API/MCP/provider guard seam → Task 6; console and explicit configuration → Task 7; publication/regressions/rollout → Task 8. All five Review Focus cases have an owning test task.

Native execution approved and implemented in this feature branch; the plan and spec ship with the implementation PR. One fresh whole-branch review completed after all task gates. No merge, deployment or live configuration change is included.

Implementation decisions: cancellation clears the current completion pointer while retaining evidence; coding reports reuse the existing report forwarder; new Tasks reuse Save Task → completion policy → Start Run instead of duplicating unsaved requirements in the launcher. See the implementation PR for verification results and review decisions.

The independent review found three material issues: oversized PR numbers could stall native reconciliation, merged-PR collection could ignore newer head CI, and unavailable saved checks could not be removed in setup. Each regression failed before its fix and passed afterward. Report validation now shares the existing PR-number bound; merged refresh retains both head and tested-SHA candidates; setup exposes unavailable saved checks for explicit removal, including moved workflow paths. No minor findings were deferred.

Final verification on implementation commit `6e77bae`: formatting, module verification/tidy, vet, build and the full Go race suite passed with PostgreSQL and real Docker enabled. Contracts, typecheck, 53 frontend unit tests and production build passed. All 172 local browser scenarios passed (8.0m), followed by all 156 Compose-applicable scenarios (5.7m; 16 existing local-provider-only exclusions). Neither final browser run required a retry. Only this validation record and the plan's completion markers changed afterward.

## Review corrections — 2026-10-23

The subsequent requested review identified four gaps. GitHub reconnect now locks identity before binding and retains the transactional account-conflict check. Managed review launch only reuses the current latest attempt; restoring old reviewer settings starts a fresh execution while exact request replay remains unchanged. Merged CI collection keeps both the source head and recorded test-merge candidates, including when accepted checks ran on the head. Completed Tasks project their accepted historical result and retain evidence/review history without recovery controls.

The focused correction review also identified Done → In Progress retaining the old session. The shared move path now clears the completion pointer whenever Done is reopened; both policies require fresh work. Existing Run-detail review controls were left in their prior scope; completed sources remain rejected by the backend.

All defects were reproduced before their fixes. Verification: formatting, module verification/tidy, vet, build and the full Go race suite passed with PostgreSQL and real Docker enabled (PostgreSQL package: 254.934s). After the final shared reopening guard change, vet/build and affected completion, PostgreSQL, integration, HTTP and MCP race tests passed again. Contracts, typecheck, all 53 frontend unit tests, production build and all 7 completion browser scenarios passed. Reconnect concurrency also passed 10 consecutive targeted runs. No schema, dependency, deployment or live configuration changes were added.
