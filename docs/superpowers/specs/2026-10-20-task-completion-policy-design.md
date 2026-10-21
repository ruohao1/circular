# Task completion policies and evidence

Date: 2026-10-20
Issue: [ISQ-473](https://linear.app/isqrd/issue/ISQ-473/enforce-per-task-completion-policies-and-evidence)
Baseline: `ea18ddd00d503538622e50a080dc704f7df6ed1d` (merged ISQ-472, PR #8).
Status: approved 2026-10-21 for native implementation and one final independent whole-branch review; deployment remains separate.

## Intent and scope

External coding agents are the primary clients. A Task defines its completion contract before work starts. A final report records what the agent produced; Circular decides the Task transition. Native execution uses the same rules. Ownership, process outcome, planning stage, and readiness to merge remain distinct facts.

The agreed policies are `review_required` and `direct_completion`. Review-required work moves to In Review after a valid successful report, then needs authoritative GitHub CI and a fresh independent review for merge readiness. Direct work can reach Done once its required result/evidence is present and ownership is safely released.

**Approved product default:** review-required Tasks can reach Done only after GitHub confirms the PR was merged and Circular can match that merge to the verified evidence. Readiness alone leaves the Task In Review.

This issue delivers the policy/evidence services, native integration, external PR review support, API/MCP inspection, and console controls. ISQ-474 adds external claim/heartbeat/finish transport; ISQ-478 adds bidirectional Linear Task status synchronization. Both must call these services. ISQ-473 does not claim those transports already exist.

## Approach

Use a small Go package for bounded policy/report validation and a pure verdict calculation. PostgreSQL owns immutable contracts, reports, attempt identity, and guarded transitions. Existing integration workers collect GitHub observations and run the existing PR reviewer. Existing HTTP handlers remain the shared transport boundary for console and control MCP.

Alternatives considered:

| Approach | Consequence |
| --- | --- |
| Shared evaluator plus existing storage/reviewer, selected | One rule set, durable evidence, existing isolation and retries. |
| Independent rules in each transport | Smaller first endpoint, but native work and provider updates can disagree. |
| General workflow/rules engine | Adds an interpreter and configuration surface this issue does not need. |

No new runtime dependency, daemon, public authentication system, automatic repair loop, or merge automation.

## Completion contract

`Policy` contains `mode` and `requirements`. Each requirement has a stable `key` and a human-readable `description`. A result summary is always required for successful completion. Requirements specify additional evidence the reporter must supply; they are not executable predicates.

Limits: at most 20 requirements; keys match `[a-z][a-z0-9_]{0,63}`; descriptions contain 1–1000 Unicode characters. `Report` contains `result` (1–4000 characters), `evidence` entries keyed by requirement, and an optional PR number in the Task's Repository. Each evidence entry has `key`, `detail` (1–4000 characters), and at most five HTTPS links of at most 2048 characters each. Reject duplicate/unknown keys, malformed URLs, control characters, invalid UTF-8, and reports larger than 128 KiB. Links are displayed safely and never fetched as evidence. The PR input is a positive number, not an arbitrary fetch URL or client-supplied SHA.

Required evidence must be present; Circular does not pretend that nonempty prose proves correctness. Agent test claims remain supporting evidence. Retained native artifacts remain available through their existing verified artifact paths. Independent CI and review supply the additional guarantees for `review_required`.

Policy starts unset. Planning can set/change it while no post-upgrade contract has been frozen and no owner exists. The first tracked claim freezes the Task policy and, for review-required work, the Repository's explicitly selected required checks. Retries inherit that contract. Every caller, including the console, is subject to the freeze; changing a Task's completion contract after work starts requires a newly planned Task. Reopening, releasing, cancelling, or reassigning work does not unlock it.

This intentionally avoids claiming a user/agent privilege distinction the private control API does not have. MCP client names and `actor` arguments are not authorization. Repository settings can change for future Tasks without weakening frozen contracts. Policy-changing control MCP tools are not added.

Review-required admission needs a nonempty explicit check configuration. Missing reviewer configuration blocks review/readiness and has an actionable setup link. Native admission also prevents selecting the configured reviewer as the coding Agent. Readiness uses the trusted frozen reviewer fingerprint from the latest requested review attempt; changing Agent defaults does not rewrite an existing review. Requesting a new review invalidates the prior verdict immediately.

Planning stage Ready can still describe an incomplete plan. Admission explains missing policy/check setup instead of starting work with a guessed default.

## Attempt identity and transitions

Keep ISQ-472 Work sessions and receipts. Add a Task pointer to its current completion session, separate from its current owner. Claiming new work sets it. Explicitly reopening a released Task into Open or Ready clears it, invalidating old promotion candidates while retaining history. Explicit Task cancellation also clears it so delayed reconciliation cannot undo cancellation. A later claim gets a new session ID. Terminal reports never reacquire ownership.

| Input | Required conditions | Task result |
| --- | --- | --- |
| Successful direct report | Frozen policy, all evidence, current session, safe release | Done (`completed`). |
| Successful review-required report | Frozen policy, all report evidence, current session, safe release | In Review, even if the PR is not yet published or CI is pending. |
| Failed/cancelled work | Authenticated current session | Record outcome and release when safe; do not promote the Task. |
| Native process success without a valid report | Cleanup completes normally | Preserve process success and report a completion blocker; no Task promotion. |
| Request Done for review-required work | Valid report, independent review/CI for the verified revision, confirmed matching merge, no owner | Done. |
| Generic move to In Review/Done | Same conditions as the corresponding authoritative transition | Apply or return actionable blockers. |
| Move an actively owned Task to another stage | No implicit release/cancellation | Reject; use the explicit work administration path. |
| Same-stage board reorder or unrelated planning metadata | Existing revision/order checks | Preserve existing behavior. |

Creation/import cannot manufacture a completed Task or policy/evidence by setting `status`. Historical completed records are preserved. A completed Task's accepted completion receipt remains historical truth; later base-branch movement does not reopen it. Reopening is explicit and makes fresh tracked work necessary.

External successful finish extends `WorkFinishInput` with the structured report. Validate before changing ownership; incomplete reports leave the session active so the client can correct them. Exact operation replay returns its original receipt; changed content with the same operation ID conflicts. Preserve the legacy nil-report digest shape and resolve historical replay before applying new report requirements. For a new successful report, derive the receipt summary from its result; reject a conflicting supplied summary. A new operation after release cannot rewrite that report. Failed/cancelled finish may omit a successful report.

Native code submits the same report through a local `submit_task_result` tool, using the established private spool/output pattern. The report is bound by trusted worker context to Run, invocation, Task, and session; these identities are never agent arguments. Persist a final candidate only from the final coding invocation. A delegation handoff report is not parent completion. Report validation does not turn a cancelled/failed process into success.

Native hooks already hold Run then session locks. They must only record outcome/report and durable pending reconciliation. A background transaction later takes Project → Task → Run → session locks, verifies current attempt and cleanup, and applies the same decision as external finish. Crashes after cleanup but before promotion must be recoverable by scanning persisted pending records. Recovery holds, live workers, or unreleased Workspaces block promotion and ownership release.

External finish must enter through Project → Task → session order; it cannot lock the session and then acquire a planning lock. Provider identity/binding/route/request locks retain their established ordering before planning locks. Review settings locks retain their existing settings → Task/Run ordering; completion code never locks those settings after locking a Task. No provider I/O inside a database transaction.

## PR binding and independent review

A current completion session can bind one PR in the Task's configured Repository. Resolve the canonical PR via the configured GitHub installation, numeric repository identity, and PR number. Validate returned URL, repository IDs, refs, and SHAs using the existing inspector. Retain the current same-repository restriction; forks are explicitly unsupported. Reject cross-Task/Project/Repository references and arbitrary URL hosts before network access.

A native publication receipt supplies the native source Run. An external report or explicit Task PR binding supplies the external Work session. Binding/replacing a PR uses expected Task/session/revision preconditions and invalidates readiness. It neither changes policy nor releases an owner. Native work can finish into In Review before its PR is manually published.

Extend the existing review source to exactly one of native coding Run or external Work session. Make `pr_reviews.source_run_id` nullable and add the external source FK, with deferred scope/kind constraints. The review is still a real isolated `runs.kind='pr_review'` Run. Its parent is the native source Run or null for external work. Never create a placeholder coding Run, coding Work session, or Linear agent session for external work.

Generalize source loading, launch replay, automatic launch intent identity, prepared fingerprints, context validation, retained evidence, API history, and publication together. Preserve existing native snapshots and their fingerprints. Use an explicitly versioned new source shape for external snapshots; do not silently rewrite persisted v1 context bundles. Preserve native stop/revocation fences and provider identity handling on publication. External reviews obtain authority through their real Task/Repository binding.

Only the existing trusted reviewer pipeline can create authoritative review evidence. An external client cannot submit a clean review verdict. Native coder and reviewer Agent IDs differ; external coding has a separate session and a fresh Circular reviewer execution with no reused coding conversation. Review Runs never acquire coding ownership or complete a coding Task themselves.

For the current completion source and PR, assess the latest requested review attempt. Every new review intent for policy-managed work takes the Task lock, invalidates readiness, and rejects a completed/superseded source; it cannot race the final Done transaction. Pending, failed, cancelled, invalid, or incomplete newer attempts block; never fall back to an older clean review. Require a succeeded Run, retained validated report, complete coverage, matching base/head, and the sealed attempt's reviewer fingerprint. Critical/high/medium findings block. Low findings are advisory. Clearing a finding requires a fresh independent report; coder self-dismissal is not supported.

## GitHub CI and freshness

The initial supported required-check source is GitHub Actions, selected per Repository by numeric workflow ID and exact job name. Allow 1–50 unique workflow/job selections; names contain 1–200 characters. Serialize provider IDs as canonical positive decimal strings to avoid JavaScript rounding. Persist the selected workflow identity and GitHub Actions producer identity obtained from GitHub, not client-supplied producer claims. Circular's initial user-confirmed selection is the workflow `.github/workflows/ci.yml` and these jobs:

- `Go / PostgreSQL / Docker`
- `Frontend / contracts`
- `Browser / local and Compose`

Offer that selection in setup for this Repository; do not silently install these names for unrelated repositories or seed runtime repository IDs in a migration. Non-Actions status contexts are not silently accepted in this first version.

Add a read-only GitHub token purpose with the necessary metadata, contents, pull requests, checks, and actions read permissions. Preserve installation/Repository scoping, identity-generation fencing, token-cache purpose separation, and safe errors. Missing permissions produce a reconnect/setup blocker, never a fallback to a human token.

Collect all pages needed for the selected workflow's runs/jobs and associated check runs, including pending attempts. Choose the newest applicable workflow run and its latest attempt before testing success; a newer pending/failed attempt overrides an older green one. Reject ambiguous duplicate job identities or an incomplete/truncated scan. Require every selected job/check to be completed with conclusion `success`. Missing, pending, neutral, skipped, failed, timed-out, cancelled, or unavailable results block readiness. An agent's test log cannot substitute for these results.

Bind CI to an immutable tested commit obtained from GitHub:

1. Prefer an observed test-merge commit whose parents are the exact current base and head.
2. A head-SHA check is eligible only when GitHub's commit graph proves the current base is an ancestor of that head.
3. If neither proof is available, return `ci_revision_unverified` with the action to update the branch/rerun CI. Never infer the tested base from a check name, a current PR label, or an agent statement.

The conservative fallback may require updating a branch even when GitHub would permit merging it. Do not claim to validate the commands a user-selected workflow runs; configuration chooses the CI authority.

Read PR state before collecting evidence and again afterward. Persist only if numeric repository/installation identity, base/head, selected check snapshot, latest review attempt, and current completion session still match. A changed observation or lost lease cannot overwrite a newer one. Reuse the existing 30-second refresh lease/25-second provider deadline and one-minute refresh interval. Positive cached readiness expires after 60 seconds; provider errors mark it unavailable rather than retaining a green verdict. Reject timestamps in the future. Bound each paginated collection to 20 pages of 100 records and each response to 4 MiB; if a remaining page, provider search cap, or incomplete scan prevents proof, block rather than approve a partial result. Inspect returns observation time and exact revision.

An explicit Done request forces refresh outside the planning transaction, then conditionally applies the result under planning locks. This cannot make GitHub and PostgreSQL atomic; Circular displays a bounded observation, and the human still controls GitHub merging.

For a merged PR, verify the accepted pre-merge revision instead of comparing its evidence to today's advanced base branch. Require GitHub's merged flag, merge SHA, matching source head, and the exact expected result tree for every merge method. A normal merge also needs the exact base/head parents; squash/rebase need ancestry from the accepted base. The expected tree comes from the previously verified test-merge commit, or from the tested head when that head contains the base. Missing lineage/tree proof returns `merge_revision_unverified`; it never silently bypasses CI/review. Retain the accepted receipt and provider evidence. Circular does not merge PRs or mark Done merely because a GitHub webhook says “closed.”

## Data and public surfaces

Add only feature data:

- Nullable Task policy, immutable frozen contract, and current completion-session pointer.
- Versioned Repository check configuration; required-check snapshot in the frozen Task contract.
- One immutable accepted completion report per Work session, with pending/applied reconciliation state and decision receipt.
- Session-scoped current PR binding and bounded latest readiness observation, including evidence IDs, exact revisions, timestamps, identity generation, and refresh lease.
- External source support in existing PR review records/intents; retain existing native data.

No secret digest, raw credential, provider token, private worker path, or unrestricted provider response appears in projections. Store bounded safe evidence fields. Do not add a generic event bus or parallel ownership model.

HTTP exposes policy editing before freeze, check configuration, completion inspection/refresh, Task PR binding, and Task-scoped review preparation/launch/history. Existing Run-scoped reviewer endpoints keep working. All stage changes still use the guarded planning service; no public `force_done` input. Direct work needs no GitHub connection.

Control MCP adds `get_task_completion`, `refresh_task_completion`, `bind_task_pull_request`, and Task-scoped review tools. Inspection is available on the read-only MCP surface; provider refresh, binding, and launching are not. External work finish/heartbeat tools remain ISQ-474. Future Linear status application calls the same guarded `MoveTask` service and preserves an active owner on rejection.

The Task detail shows the policy/requirements, frozen state, report, PR, check evidence, review verdict/findings, observation age, and specific next actions. Use “Ready to merge” for the evidence verdict; never replace planning “Ready” with it. Reuse PR review report components and task stage controls. Explain stage rejection inline and refresh stale UI state. Setup exposes explicit check selection and permission errors. No new dashboard/Gantt ownership UI in this issue.

PR publication remains manual for policy-managed work. Existing automatic publication triggers must skip these new sessions; do not turn on publishing as a side effect of a report. Manual draft publication remains possible while readiness is blocked. Existing explicitly enabled independent-review scheduling may be reused; this does not authorize publication or merging of the PR itself.

## Existing flows and rollout

Generic Task creation/import leaves policy unset unless the planning caller explicitly supplies one. Linear automatic requests with an unset policy wait with a configuration reason; they do not guess direct completion or fail permanently. Once the user plans that Task, existing request retry/start behavior can continue.

Discovery is an explicit planning preset: show and save direct completion with a required discovery report before launch. Delegated children have a server-defined direct-completion contract for their internal handoff: objective result and a `handoff` evidence entry describing changes/checks or why no changes were needed. Show that contract in delegation guidance and persist it before child admission. The coding agent cannot choose a weaker contract for the parent. Child Run completion still resumes the parent regardless of the child's Task stage. A child's successful report never completes its parent or clears the parent's review/CI requirements; the parent must report and review the integrated result normally.

Stop old writers and drain native execution/cleanup and external ownership before migration, matching ISQ-472's upgrade discipline. Abort migration when an active owner or unsafe native cleanup remains; do not release it or invent a policy. Preserve historical Tasks, Runs, reports, and old operation receipts. Previously completed Tasks remain completed. Every new post-upgrade claim needs an explicit contract. Preserve old review bundle decoding; test upgrades with retained native reviews.

No deployment or live configuration change is part of this planning change. Ship spec, implementation plan, and code together on `feat/isq-473-completion-policy`; no standalone plan PR.

## Verification and limits

Use table-driven evaluator tests, PostgreSQL migration/concurrency tests, provider fixture tests, HTTP/MCP parity tests, and focused browser scenarios. Exercise lost finish acknowledgements, reassignment/reopen races, native cleanup/recovery, base-only changes, pending CI reruns, pagination/permissions failure, newer failed reviews, old snapshot replay, cross-Repository PR input, and every generic terminal-stage bypass. Run the full existing CI gates after implementation because admission changes affect all fixtures/launchers.

Initial limits are deliberate: GitHub Actions checks, same-repository PRs, immutable contracts once work starts, and fail-closed revision proof. Missing proof can block a legitimate completion and must say how to resolve it. Direct completion checks supplied evidence, not its semantic truth. Manual GitHub actions remain outside Circular's enforcement boundary.

## Source notes

GitHub documents check-run producer/commit fields, pagination, and Checks read permission in [Check runs](https://docs.github.com/en/rest/checks/runs). Workflow/run identity, rerun attempts, and Actions read permission are documented in [Workflow runs](https://docs.github.com/en/rest/actions/workflow-runs) and [Workflow jobs](https://docs.github.com/en/rest/actions/workflow-jobs). The design above deliberately treats incomplete discovery as blocked.

GitHub distinguishes head and test-merge checks and accepts some conclusions that this issue explicitly rejects; see [Troubleshooting required status checks](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks). Commit ancestry and merge metadata come from [Commits](https://docs.github.com/en/rest/commits/commits) and [Pull requests](https://docs.github.com/en/rest/pulls/pulls). The stricter freshness, producer selection, and merge-proof rules here are Circular design decisions.
