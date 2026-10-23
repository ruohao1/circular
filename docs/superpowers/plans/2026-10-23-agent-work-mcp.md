# External coding-agent work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Let existing coding clients discover, claim, heartbeat, and finish Tasks through Circular's private HTTP API and MCP.

**Architecture:** Add transport adapters over the shared PostgreSQL lifecycle. Reuse planning pagination and completion validation. MCP never writes the database or launches a replacement Run.

**Tech Stack:** Existing Go, PostgreSQL, net/http, MCP Go SDK, OpenAPI and generated TypeScript; no new dependency.

**Spec:** `docs/superpowers/specs/2026-10-23-agent-work-mcp-design.md`

## Global Constraints

- Keep API/MCP private and read-only connections unable to mutate work.
- Caller retains a random session UUID and a 256-bit secret before claim; persist only its digest.
- Heartbeat every 30 seconds; attention after 120 seconds never releases ownership.
- Use the existing ownership, readiness, completion and transaction services.
- Plans and design travel with the implementation PR; no separate plan PR.
- No deployment, new UI screen, schema migration, client plugin or Linear sync.

## Review Focus

- Lost responses and restarts must replay the same identity without creating work or repeating stage changes (Task 1/2).
- A read-only client or stale secret must never mutate another session or recover its secret (Task 1/2).
- Partial/malformed completion evidence must leave ownership and stage unchanged (Task 1).
- Native ownership and cleanup must remain protected when external transports are used (Task 1).
- Task reads must keep planning, dependency and ownership facts in one snapshot; inspection is advisory until claim (Task 1/2).

### Task 1: HTTP lifecycle and inspection contracts

**Files:**
- Create: `internal/httpapi/task_work.go`, `internal/httpapi/task_work_test.go`
- Modify: `internal/httpapi/api.go`, `contracts/openapi.json`, `apps/web/src/generated/api.ts`
- Reuse: `internal/postgres/work_claims.go`, `work_reports.go`, `work_sessions.go`, `task_completion.go`

**Interfaces:**
- Consumes: `postgres.ClaimExternalWork`, `HeartbeatWork`, `FinishExternalWork`, `ReleaseTaskWork`, `CurrentTaskWork`, `InspectWorkSession`, `WorkSessionReceipts`, `InspectTaskCompletion`.
- Produces: the six HTTP routes in the spec, named OpenAPI request/read/error schemas, stable work error codes and reload paths. Snapshot/changes return current Task and board revisions.

- [x] Write HTTP tests for a Ready Task with guidance and dependency state, successful claim and exact replay, current ownership, heartbeat sequence replay, direct and review-required completion, and unchanged native Run count.
- [x] Run `go test ./internal/httpapi -run 'TestTaskWork' -count=1`; expect missing-route failures.
- [x] Implement strict request decoding and shared-service calls. Use repeatable-read inspection, safe session projections, no-store, and existing origin checks. Return credentials only from an accepted claim.
- [x] Add focused tests for wrong/malformed credentials, stale revisions, missing policy/dependencies/evidence, changed replay payloads, stale sessions after release/reopen, user release versus native cleanup, duplicate/unknown JSON keys, body limits and origin rejection. Verify no partial mutation or secret in errors/reads.
- [x] Update OpenAPI and regenerate its TypeScript client with `corepack pnpm contracts:generate`.
- [x] Run `go test ./internal/httpapi ./internal/postgres -run 'TestTaskWork|TestExternalWork|TestWork|TestCompletion' -count=1` and `corepack pnpm contracts:check`; expect PASS.
- [x] Commit the tested HTTP contract, implementation, tests, and these documents.

### Task 2: Private MCP tools, retry guidance and verification

**Files:**
- Create: `internal/controlmcp/task_work.go`, `internal/controlmcp/task_work_test.go`
- Modify: `internal/controlmcp/server.go`, `internal/controlmcp/client.go`, existing tool-surface tests/fixtures, `docs/development/control-mcp.md`
- Verify: `internal/httpapi/mcp*_test.go`, `tests/browser/mcp.spec.ts`, existing task/completion scenarios.

**Interfaces:**
- Consumes: Task 1's HTTP lifecycle, read models, error codes and reload actions; existing `list_task_planning`.
- Produces: six tools named in the spec. Both read tools exist in all access modes; four write tools exist only in full control. Network failures preserve caller-owned retry parameters.

- [ ] Write MCP integration tests over the real API/database covering candidate discovery, claim, reconnect/replay, heartbeat, completion and no replacement Run; write read-only and lost-claim cases.
- [ ] Run `go test ./internal/controlmcp -run 'TestTaskWork' -count=1`; expect missing-tool failures.
- [ ] Implement thin tools, bounded typed arguments, annotations, and descriptions. Reuse server-side validation. Error messages retain stable codes and reload guidance; include work retry identities in uncertain-response messages.
- [ ] Update tool inventories and private setup/recovery documentation with a complete client workflow and safe credential generation example.
- [ ] Run `go test ./internal/controlmcp ./internal/httpapi -count=1`; expect PASS.
- [ ] Run formatting, module checks, vet/build, full Go race suite with PostgreSQL and Docker, generated contracts, frontend typecheck/unit/build, and focused browser MCP/task regressions; expect PASS and record exact commands/results.
- [ ] Commit the MCP feature and validation record, request one fresh whole-branch review, fix substantive findings with regression tests, then create the implementation PR.

## Execution record

Base: merge commit `195eaf02dcbb74ec08b1a07f30e5d6732cd50137` (PR #9). Its tree matches tested head `9080606`; all three CI jobs passed before merge. Work uses a managed isolated worktree; the unrelated dirty root checkout is preserved.

HTTP verification: missing-route tests failed before implementation; lifecycle, preparation, replay, revocation, native cleanup and boundary cases pass. Shared PostgreSQL ownership/completion regressions pass (`go test ./internal/httpapi ./internal/postgres -run 'TestTaskWork|TestExternalWork|TestWork|TestCompletion' -count=1`). Generated contracts match.
