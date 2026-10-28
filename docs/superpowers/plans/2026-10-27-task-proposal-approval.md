# Task proposal approval implementation plan

**Goal:** Let agents propose work and let an independently signed-in user decide
its exact scope once, through the private Circular console.

**Architecture:** PostgreSQL owns immutable proposal revisions and decision
receipts. HTTP and MCP share that lifecycle. A separate GitHub browser session
supplies the decision identity. All creation, semantic editing, and execution
admission paths require the same scope receipt. The worker resumes pending
native delegations through their existing checkpoint boundary.

**Tech stack:** Go, pgx/PostgreSQL, existing GitHub OAuth helpers, React/TanStack,
OpenAPI, MCP Go SDK. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-24-task-proposal-approval-design.md`

## Global constraints

- Work only in the isolated feature worktree. Keep this plan with implementation.
- Private API/MCP and review page. No deployment, public ingress, or provider writes.
- A model tool argument, Linear comment, issue status, or MCP identity is not consent.
- Immutable decisions bind exact proposal revision and expected existing Task revision.
- No duplicate Tasks or lost history. Reuse verified unchanged Linear scope approval.
- Read-only MCP omits mutations. Approval does not launch work or publish anything.
- Project lock precedes Task/proposal locks. Provider requests run outside planning locks.
- Keep completion contracts, source locks, active ownership, and dependency checks.
- Missing login setup fails closed. Never print or persist provider login tokens.

## Review focus

Forgery/replay/CSRF, stale revisions and concurrent decisions, every Task creation
and semantic-edit path, historical approval unknown, import reuse, native parent
continuation after refusal, and integration credentials unchanged by login.

## Task 1: Durable proposal and approval domain

- [x] Add failing PostgreSQL tests for revision races, duplicate decisions, create,
  confirm, amendment, dependency readiness, scope reuse, and rejection.
- [x] Add migration and shared proposal types, validation, lifecycle, and receipts.
- [x] Verify focused tests with a disposable PostgreSQL database; expect PASS.

Interfaces: proposal draft/revision/decision/view; canonical Task scope comparison;
shared transaction functions with Project-first locking. Existing Tasks stay intact.

## Task 2: Separate browser identity

- [x] Add failing tests for OAuth state/browser/PKCE/app binding, expiry, replay,
  allowlist, cookie/CSRF/origin boundaries, and unchanged integration grants.
- [x] Implement separate state/session tables and HTTP login/session/logout routes.
- [x] Reuse lower-level provider helpers with a dedicated callback; never reuse
  integration Begin/Complete. Verify focused security tests; expect PASS.

Interfaces: server-derived approver identity, opaque session, CSRF token; no model
or ordinary API argument can supply a decision identity.

## Task 3: Private proposal HTTP, MCP, and console surfaces

- [x] Add failing API/MCP and browser tests for propose/revise/read/review/decide.
- [x] Add strict contracts, handlers, MCP tools, and a private review page.
- [x] Show revisions, sources, unknown approval, readiness blockers, decision
  receipt, and resulting Task. Conflicts require reading the new revision.
- [x] Verify generated contracts, frontend checks, and focused tests; expect PASS.

Interfaces: shared proposal views, revision-bound review URL, browser-only decision
POST. Read-only MCP never advertises mutations or decision tools.

## Task 4: Enforce approval at existing boundaries

- [x] Add failing tests for direct create/discovery/import/native launch/semantic
  edit bypasses and admissions without a receipt.
- [x] Route new scope through proposals. Preserve safe scheduling changes and
  unchanged imports. Bind provider identity and reviewed source scope explicitly.
- [x] Adapt console callers, fixtures, and existing tests to the new behavior.
- [x] Verify creation/planning/import/work/completion regressions; expect PASS.

Interfaces: existing routes return a proposal when consent is needed; already
approved unchanged scope retains its Task. Existing history is never backfilled.

## Task 5: Native delegation approval and continuation

- [x] Add failing tests for pending checkpoint, approved child admission exactly
  once, stale policy, refusal continuation, and replay without re-proposal.
- [x] Save execution intent before waiting; have the worker revalidate admission.
- [x] Resume the parent after refusal without creating a fake child Run.
- [x] Verify delegation/recovery/execution regressions; expect PASS.

Interfaces: proposal-linked delegation, nullable child Run, explicit refusal result,
existing checkpoint and continuation. Decision handler never launches a Run.

## Task 6: Delivery and verification

- [x] Document allowlist and additional GitHub callback setup. Keep ingress private.
- [x] Add changelog entry and update the design with observed implementation limits.
- [x] Run full Go tests with PostgreSQL, relevant race tests, contract check,
  frontend typecheck/tests/build, and focused browser tests. Record actual results.
- [x] Request one independent whole-change review, fix important findings, and rerun
  affected checks. Prepare the implementation for review without deploying it.

## Implementation decisions

- Confirmation and amendment bind the existing Task ID and revision. History and
  manual stages stay intact. Approval of incomplete requirements is refused.
- Scope edits reuse the planning validators under a savepoint. The attempted edit
  is rolled back before its complete proposal is persisted.
- Purpose and acceptance criteria are versioned scope and are frozen into native
  execution input. Scheduling, priority, and preferred Agent do not grant scope.
- Approval records child work. Worker admission independently rechecks its saved
  execution intent. A refusal resumes the parent without a child Run.
- Rejected revisions retain their decision receipt and can be revised. Approved
  proposals are immutable. Source-managed repository changes conflict early.
- The review covered tracked and untracked work before committing. All four
  important findings were fixed: rejected revision editing, MCP planning fields,
  launcher handoff, and PR-review admission. Follow-up integration checks also
  fixed GitHub callback registration and the Task detail navigation blocker.
- Proposals use the existing Project collection pattern without pagination.
  External claims retain their existing Ready-stage requirement.
- This branch started from `feat/agent-work-mcp`. After PR #10 merged on
  2026-10-28, the approval PR was retargeted to `main`. No deployment is included.

## Verification results

- The full Go race suite passed with disposable PostgreSQL and real Docker:
  25 packages and 1,570 test cases, using `go test -race -p 2 ./... -timeout=10m`.
  The subprocess helper is not run as a standalone test. Vet and build passed.
- Generated contracts, TypeScript, all 53 frontend unit tests, and the production
  build passed. The existing large-bundle warning remains.
- The full local browser run passed 173 of 177 scenarios. Four old fixtures still
  assumed immediate Task creation, earlier MCP counts, or one provider identity.
  After correcting those fixtures, all eight scenarios in the affected files
  passed on a fresh stack, including the four failures. The focused four-case
  proposal handoff suite also passed.
- Approval and PR-review Go regressions passed again after the provider fixture
  correction. The production approver allowlist was not relaxed.
- Hosted frontend checks and both complete browser runs (local and Compose)
  passed. Hosted Go jobs exposed 150–200 ms CLI deadlines in existing timeout
  fixtures. Controlled startup and cleanup latency reproduced the failures.
  These fixtures now use the standard two-second CLI budget and five-second
  create delays. The late-commit fixture still commits after the CLI timeout.
  Timeout, cancellation, ownership, cleanup, and exactly-one-retry assertions
  remain. The delayed startup case passed three repetitions; all delayed cleanup
  cases passed twice. The full runtime package also passed with race detection
  and real Docker enabled after the complete fixture correction.
- No live GitHub sign-in or deployment was performed.
