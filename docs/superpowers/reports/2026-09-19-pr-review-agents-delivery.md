# PR review workflow delivery

Implemented and deployed locally on 2026-09-19; tracked in [ISQ-255](https://linear.app/isqrd/issue/ISQ-255/add-dedicated-pr-reviewer-agents-and-a-review-workflow).

Each project has a customizable reviewer with Astra/low defaults. The console, HTTP API and MCP support preparing and launching reviews, reading retained findings/history, refreshing PR status and recovering publication. Automatic reviews are off by default. Reviews inspect pinned source in a read-only workspace; GitHub receives COMMENT reviews and the linked Linear issue receives its independently opted-in summary.

## Verification

- 22 Go test packages passed with disposable PostgreSQL and actual Docker execution; 10 packages have no tests.
- 26 frontend unit tests and all 48 browser tests passed.
- Contracts, typecheck, production build and whitespace checks passed.
- The deterministic full-stack browser flow verified source task → reviewer run → retained report → GitHub review → Linear summary.
- Mobile report/settings screenshots inspected at 390×844.
- Independent review found no Critical issue. Both Important findings were reproduced and fixed: acknowledged reports now reach the worker stream before the private tool returns success; completed report/history views refresh later PR and independent publication changes. The report/output/private-tool suites also pass under the Go race detector.

## Local upgrade

Applied additive schema 0012 and rebuilt API, worker, web, MCP and Codex workload images. Preserved 13 projects, 3 repositories, 17 tasks and runs, all 26 existing agents, artifacts, workspaces, and integration metadata. Added one reviewer per project (13), with every automatic-review setting off. GitHub and Linear remain connected. No model review or external feedback was triggered by deployment.

The live UI prepared PR reviewer `4dffc1c4-8a1b-415a-9bc5-49daecd80ab4`, model `gpt-6-astra`, reasoning `low`, for circular-test PR #1 at `6d997fc17e7f02cfe6b2794013c3e5b70ee647db`. Live-model acceptance is pending explicit authorization; this is the remaining issue acceptance step.

## Deferred minors

- Retained review artifacts use the existing generic Workspace output/TAR labels; primary structured findings and links work, but the artifact list needs distinct labels/formats.
- Oversized PR capture shows a generic verification failure; user docs explain size limits/splitting, but the execution error should name the exceeded limit.

## Rulings made during implementation

- Ruling: Work in the approved shared checkout and preserve existing changes; no commits or .git mutations — the approved plan explicitly requires this and HEAD lacks prerequisite features — cost if wrong: isolation is provided by the captured baseline, not a separate branch.
- Ruling: Focused tests per task and full suite at integration checkpoints — follows repository test cost and developer guidance — risk: cross-module regressions detected at next integration checkpoint.
- Task 2: Ruling: Older tests assumed one built-in preset; update cardinalities and select discovery by preset, asserting both presets remain present — required by the approved second preset — cost if wrong: false failures in legacy tests avoided without weakening preservation assertions.
- Task 3: Ruling: Implement the pure freshness projection ahead of Task 7 because inspection already returns the shared DTO — no provider work added early — cost if wrong: minor ordering change only.
- Task 4: Ruling: Shared context I/O lives in prreviews (ReadBundle/WriteContext), used by worker preparation and private tools — one bounded immutable implementation avoids divergent file validation — cost if wrong: broader value-module interface.
- Task 4: Ruling: An unborn managed cache HEAD is assigned the verified captured head only after exact objects are fetched — existing worktree validation requires a valid local HEAD, never a live default-branch substitution — cost if wrong: the cache's local default ref needs a normal coding refresh later.
- Task 6: Ruling: Retain canonical JSON bytes so stored context/report checksums equal their semantic fingerprints — avoids JSONB key order changing retention identity — cost if wrong: only the serialization order changes for these new artifact types.
- Task 6: Ruling: Add ReleaseReview with the database's context digest instead of trusting recovery labels alone — existing Release has no expected review identity argument — cost if wrong: a changed/missing recorded digest intentionally blocks cleanup adoption.
- Task 6: Ruling: Fence context directory allocation and persistence together using the existing withAllocation lock — prevents a stale worker creating files after recovery released the run — cost if wrong: context fsync temporarily holds the run lock.
- Task 6: Ruling: Review cleanup retains context, diff and available structured report, without a redundant source archive — the exact commits remain pinned and review source is read-only — cost if wrong: no tar download for a review run.
- Task 7: Ruling: Queue the review's Linear summary through a shared postgres transaction helper so provider fixtures exercise the same frozen-issue and current opt-in checks — avoids duplicating completion policy — cost if wrong: one narrow exported database helper.
- Task 7: Ruling: For review summaries, persist the Linear body in a separate committed step before calling the provider — the existing coding delivery transaction did not provide a durable frozen-body marker for late GitHub URLs — cost if wrong: one additional background iteration before a review comment.
- Task 8: Ruling: Permit only the exact UUID-scoped POST /pr-reviews/{id}/refresh through the read-only MCP HTTP client — this route performs provider reads only, as required by the approved tool table — cost if wrong: future changes to that endpoint must preserve its read-only semantics.
- Task 9: Ruling: The disposable browser harness uses a separate deterministic review image and test-only seeding endpoint, while preserving production Codex validation, GitHub identity/authentication, Git capture, worker execution and publication code — no paid model is needed to verify the coherent flow — cost if wrong: real-model behavior remains a separately authorized acceptance step.
- Task 9: Ruling: Legacy browser tests selected discovery by array position and asserted pre-reviewer counts; select the repository-discovery preset explicitly, assert the new reviewer exists, and extend MCP count/read-only assertions for eight review tools — preserves all original behavior checks — cost if wrong: only fixture expectation changes. Initial full browser run 43/46; all 5 affected MCP/setup cases now pass. Final independent reviewer dispatched with feature baseline, complete rulings and verbatim Review Focus; full browser rerun and image builds underway.
- Final: Ruling: Real paid-model review quality and live account compatibility remain the separately authorized acceptance step specified in the approved plan — fixture execution and read-only live preparation establish implementation behavior without authorizing model work — cost if wrong: account-specific model failure can still surface in the first authorized live review.
- Final: Ruling: Local upgrade outcome is verified by the implementer after independent review, using pre/post metadata and live read-only checks — deployment had not occurred at review time — cost if wrong: local operational issues must be corrected before delivery.
- Final: Ruling: Keep the shared checkout and verification ledger instead of presenting git integration choices or deleting the execution record — the approved plan already specifies no commits/pushes and preservation of prior uncommitted features — cost if wrong: integration remains an explicit later decision; the recorded source baseline and tests remain available.

Detailed execution evidence remains in `.superpowers/sdd/2026-09-19-pr-review-agents/progress.md` and its adjacent logs. The shared checkout is preserved without commits, pushes or branch changes, per the approved plan.
