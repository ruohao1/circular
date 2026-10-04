# Circular Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Circular its own GitHub/Linear actor and let explicitly enabled Linear mentions/delegation produce visible, recoverable Circular runs.

**Architecture:** Extend existing encrypted integrations and publication ledgers. Add a dedicated webhook receiver and durable inbox, then route Linear sessions through the existing isolated run system. Keep the three stages independently deployable.

**Tech Stack:** Go 1.27.1, PostgreSQL 17, React/shadcn, TanStack Query, generated OpenAPI, Fumadocs, Go tests, Vitest, Playwright, owned Docker/provider fixtures.

**Spec:** [Circular app identity and Linear requests](../specs/2026-09-19-circular-identity-design.md)

**Status:** All 14 implementation tasks are complete in the shared checkout; independent review findings are fixed and final fixture checks pass (2026-09-20). Live acceptance is prepared as [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test). [ISQ-258](https://linear.app/isqrd/issue/ISQ-258/give-circular-its-own-github-and-linear-identity).

## Global Constraints

- Use Go 1.27.1, PostgreSQL 17, the existing React/shadcn UI, generated OpenAPI client, and Fumadocs user documentation; no new framework or queue service.
- Use the existing encrypted integration vault; credentials never appear in public API responses, MCP results, logs, run inputs, or workload containers.
- Keep the control API and MCP private; public ingress exposes only the dedicated webhook receiver.
- Existing connections and automation settings remain unchanged until an explicit setup or enable action; migrations do not backfill publications or start runs.
- Preserve numeric provider identities, publication receipts, original authors, and idempotency across retries, restarts, renames, and reconnections.
- Use existing agent model/reasoning controls and the repository's Astra default; inbound text cannot change credential access, routing, or execution policy.
- Test against owned provider fixtures and disposable PostgreSQL/Docker resources; use live providers only for separately selected acceptance actions.
- Preserve the shared checkout's existing changes; this planning work does not authorize staging, committing, pushing, deployment, or live automation.

## Review Focus

1. A delivery lost its response just before the project changed identity: recover the original author's receipt. Stage 1 task 4.
2. Two projects share the same Linear app grant: disconnect/refresh in one must not corrupt the other. Stage 1 task 3.
3. A webhook signature is valid but belongs to another workspace/repository: no cross-project launch. Stage 2 task 1 and stage 3 task 1.
4. A stop arrives before its create event or while publication is queued: cancellation wins and no later write is initiated. Stage 3 task 4.
5. The user changes an agent model or a task repository after approval: the request executes its verified snapshot or reports conflict. Stage 3 task 2.

## Delivery sequence

| Stage | Plan                                                                                                                                                                                     | Working result                                                                       |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| 1     | [App identity and publication](2026-09-19-circular-identity-auth.md) · [ISQ-259](https://linear.app/isqrd/issue/ISQ-259/enable-circular-app-identity-and-app-authored-publications)      | Existing runs publish as Circular after guided setup, with legacy connections intact |
| 2     | [Webhook reception](2026-09-19-circular-identity-webhooks.md) · [ISQ-260](https://linear.app/isqrd/issue/ISQ-260/receive-provider-webhooks-through-a-dedicated-circular-listener)        | Verified deliveries and revoked access are visible; no automatic runs yet            |
| 3     | [Linear requests and sessions](2026-09-19-circular-identity-linear.md) · [ISQ-261](https://linear.app/isqrd/issue/ISQ-261/run-circular-agents-from-linear-mentions-and-issue-delegation) | A mapped mention/delegation starts one run and reports native progress; stop works   |

Stages depend on the prior stage's interfaces. Within stage 1 the GitHub and Linear provider adapters are separate review units, but both depend on the identity storage contract. Keep implementation inline unless an execution method is selected explicitly; use one independent final review before delivery. These shared credential/receipt boundaries favor sequential work over concurrent edits.

## Accepted product defaults

- One app identity per provider account/workspace; internal agent names appear in activity.
- Existing apps are upgraded in place.
- External work remains disabled until the project has a verified route and the user enables it.
- Draft route mode is automatic; an approval mode is also available.
- First release supports GitHub authorship and Linear issue invocation. It does not promise GitHub chat commands or live conversation with a running job.
- Public HTTPS webhook hosting/tunneling is configured once by the operator. The console handles provider URLs and checks; automatic tunnel provisioning is a separate feature.

## Implementation checklist

- [x] Review this design and the three stage plans; accept automatic mode after explicit enablement.
- [x] Stage 1: implement shared identity records and capabilities.
- [x] Stage 1: implement GitHub App keys and scoped installation tokens.
- [x] Stage 1: implement Linear workspace app grants and safe reconnection.
- [x] Stage 1: bind all publication paths to their recorded actor.
- [x] Stage 1: ship guided identity UI, API contracts, upgrade coverage, and user docs.
- [x] Stage 2: implement verified webhook acceptance and durable inbox.
- [x] Stage 2: package the separate receiver and process access changes.
- [x] Stage 2: ship reception setup, diagnostics, API contracts, and user docs.
- [x] Stage 3: implement explicit routing and request records.
- [x] Stage 3: share transactional run launch and snapshot external inputs.
- [x] Stage 3: deliver idempotent Linear session activities.
- [x] Stage 3: implement stop, out-of-order events, and honest follow-up behavior.
- [x] Stage 3: ship request UI and restricted MCP request controls.
- [x] Stage 3: complete fixture end-to-end coverage, user docs, and final review.
- [ ] Perform selected live acceptance in `circular-test`; record provider actor IDs and run/PR links in Linear.

## Verification environment

Verification used `testsupport.Database(t)` with `TEST_DATABASE_URL` pointing to an owned disposable PostgreSQL server. Database-dependent tests that skip are not acceptance evidence. Docker execution must use owned fixture resources, not the live Circular stack.

Use the Go version in `go.mod` and Corepack/pnpm version in `package.json`. Run targeted tests while implementing, then the following once the relevant stage is complete:

```sh
go test ./...
corepack pnpm contracts:generate
corepack pnpm contracts:check
corepack pnpm test
corepack pnpm typecheck
corepack pnpm build
corepack pnpm exec playwright test
git diff --check
```

Run credential/lease/concurrency packages with `-race` after their tests pass normally. Inspect mobile/desktop screenshots for the new forms and request details. Confirm old setup, repository creation, run publishing, and PR review browser flows still pass. Do not repeat full suites without a new change or unresolved failure.

## Plan completion record

Implementation completed sequentially in the existing shared checkout. Review used
`/tmp/circular-identity-baseline` to exclude prior uncommitted features. One
independent reviewer checked the three stages and five review-focus cases. Both
Important findings were fixed with regressions observed failing and then passing:
accepted webhook retry exhaustion and cross-project automatic-request starvation.
No Critical findings or deferred Minor findings remain.

Final verification:

- Full Go suite with disposable PostgreSQL and real Docker: 984 cases/subtests
  passed across 23 tested packages. Only the subprocess-only Git helper skips its
  direct harness invocation; its parent process test passes.
- Race detection passed for integrations, PostgreSQL, HTTP, MCP, webhook and
  migration packages.
- All 53 browser flows and 26 frontend unit tests passed.
- Generated contracts, TypeScript checking, production build and whitespace checks
  passed. The existing large-bundle build warning remains.
- Desktop/mobile request screenshots and keyboard focus/selection were checked.

Source changes, baseline and evidence are retained; no staging, commit, push or
live deployment occurred. The detailed stage task sequences remain the original
execution recipe; their ledgers record actual verification and decisions.
[Prepared live acceptance](2026-09-19-circular-identity-live-acceptance.md) uses the
existing `circular-test` project, Coding agent and Astra/low. ISQ-262 is prepared;
no live invocation or route enablement has been performed.
