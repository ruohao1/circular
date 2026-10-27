# Temporal for managed Run orchestration

Evaluated on 2026-10-26 against Circular `195eaf02dcbb74ec08b1a07f30e5d6732cd50137`.

## Decision

**Defer production adoption for managed Runs.** Temporal can coordinate the existing execution boundary and recover its Workflow after worker loss. Both tested variants remove **zero production coordination lines**. The follow-up replaces in-Activity lease polling with a durable Workflow timer, but increases the compared adapter functions from 70 to 210 source lines. It still needs PostgreSQL claims, leases, fencing, the Supervisor, output ingestion, terminal decisions, retention, and cleanup recovery. The wrapper adds admission reconciliation, an execution receipt, Workflow history, SDK dependencies and another service to operate.

The authorized follow-up split cleanup recovery into separate claim, retention and release Activities. It preserved the failure matrix and freed the Activity during lease waits, but did not meet the code-deletion target. Long-lived delegation or approval waits could provide a stronger reason to adopt Temporal. They were mapped below, not implemented.

The [runnable spike](../../../experiments/temporal/README.md), its [plan](../plans/2026-10-26-temporal-evaluation.md), and this decision belong to the same feature branch. The root Go module, production scheduler, migrations, API/MCP contracts and application behavior are unchanged.

## What was evaluated

The nested Go module pins `go.temporal.io/sdk v1.50.0`, uses Go 1.27.1, and imports Circular through a local module replacement. The SDK release requires at least Go 1.26.0. The local service is Temporal CLI 1.9.1 / Server 1.32.0. The Compose file pins its container digest and stores the development database on a disposable volume. This server is a development tool, not a production deployment. [SDK release and requirement](https://github.com/temporalio/sdk-go/releases/tag/v1.50.0), [CLI setup](https://docs.temporal.io/cli/setup-cli).

The spike uses actual PostgreSQL migrations, `postgres.CreateRun`, native Work sessions, `postgres.Queue`, `execution.Supervisor`, Git worktrees, the fake backend, artifact storage, and the HTTP execution/event reads. Fault cases use the existing external Docker CLI simulator. A separate successful lifecycle uses an actual Docker container built from this checkout's fake backend. Each test owns one random schema and one Run. The initializer refuses ordinary schemas, and the execution receipt accepts only the fake backend.

The original Workflow has two Activities. The follow-up uses a distinct Workflow type for separate cleanup recovery, described below:

| Boundary | Evaluated behavior | Ownership and retry rule |
| --- | --- | --- |
| Admission and dispatch | Commit a Run, native Work session and outbox row; dispatch a fixed Workflow ID | PostgreSQL transaction owns admission. Dispatch retries the same identity. |
| `execute` | Claim, provision, execute, finalize, retain, clean up and release through the existing Supervisor | PostgreSQL owns execution. One Activity attempt; a durable receipt also rejects repeated delivery. |
| `reconcile` | Observe a settled Run, or wait for the current lease and perform cleanup-only recovery | Three Activity attempts; existing database recovery counter also caps takeovers at three. Never execute the agent. |
| Workflow | Sequence those Activities and preserve the execution/cancellation error when cleanup also fails | No Workflow retry policy. Cleanup uses a disconnected context. |

Initial execution deliberately keeps all phase boundaries inside the Supervisor in both variants. The follow-up splits recovery after execution ends or its worker is lost. It reuses public retention and cleanup APIs, so it needs no production seam changes. It does not test a split of initial provisioning, execution or finalization.

## Evidence

Run the commands in the [experiment README](../../../experiments/temporal/README.md). The [result manifest](../../../experiments/temporal/evidence/results.json) records the final test outcomes, versions, history hashes and limitations. `TEMPORAL_EVIDENCE_DIR` exports the actual service histories; the tests replay each completed history with `worker.NewWorkflowReplayer`.

| Case | Fault or check | Observed outcome |
| --- | --- | --- |
| Complete fake Run | Actual Temporal service, PostgreSQL, Git and real Docker container | One Run started and completed. Diff/archive hashes and expected output bytes verified. Container and worktree removed. |
| Admission acknowledgement loss | Reject the outbox acknowledgement after Temporal accepted start; dispatch again | Same Workflow identity, one backend start, one Run. |
| Concurrent/repeated Activity delivery | Two execution deliveries race, then delivery repeats after completion | One receipt wins. One backend start. Repeated execution/reconciliation adds no events. |
| Receipt committed before claim | Reconcile a queued Run with an execution receipt | Attempt fails and releases its claim. Backend never starts. |
| Missing Temporal history | Receipt exists but service has no Workflow for that identity | Dispatch refuses to start another Workflow. The receipt still prevents invocation replay. |
| Loss during partial provisioning | SIGKILL after container creation while identity handoff is incomplete | Interrupted Run fails once. Zero backend starts. Resources reconciled; two artifacts retained. |
| Loss during uncertain start | Withhold state-inspection acknowledgement; wait for the actual fake result file; confirm `provisioning` and no committed container ID; SIGKILL | Backend demonstrably executed once. Recovery finds the allocation, preserves output, fails the interrupted attempt, and never starts it again. |
| Loss during retention | Pause artifact-row insertion after immutable patch bytes exist; SIGKILL | Recovery retains patch/archive and their hashes, releases resources, and does not execute again. |
| Loss during cleanup | SIGKILL at container removal after successful finalization | Run stays `succeeded`. Two artifacts and output survive. Recovery releases resources without another invocation. |
| Retention failure | Reject artifact events, then restore storage and expire the disposable lease | Worktree survives the outage. Recovery verifies retained bytes, preserves the original Run failure and releases resources. |
| Backend plus cleanup failure | Inject backend failure after its first event and reject container removal | Cleanup records separate Workspace failure events. Later recovery keeps the original backend failure and retained output. |
| Cancellation | Wait for known backend output, persist cancellation through existing API policy, then cancel the Workflow | Run and Workflow remain cancelled; cleanup verifies retained bytes and releases resources. |
| Fencing | Reconcile only after lease expiry; try a stale owner's write; separately retain a live lease | Stale write rejected. A live lease is not stolen. |
| Workflow retry policy | SDK test environment: execution and cleanup both fail | One execution call; three cleanup calls; original execution failure returned. |

The crash tests kill a real Activity worker process group and start a replacement worker against the same service. They advance only the test Run's database lease timestamp to avoid waiting 60 seconds. They do not simulate process loss by returning an error or calling graceful `Stop`. The accepted-start case uses the real fake-workload output file as evidence; counting a CLI start call alone would not establish agent execution.

The interrupted cleanup case also demonstrates an important distinction: the Workflow can fail from a lost Activity acknowledgement while the authoritative Run remains successful. A user retry must consult Circular's Run state. It must not repeat the coding invocation to make the Workflow green.

The initial Workflow check failed with zero Activity calls, then passed after implementation. Live cancellation exposed a successful Activity result after API cancellation; the adapter now preserves the Workflow context's cancellation state. Independent review found two evidence gaps in the early start/cancellation markers. The final tests wait for actual backend output and assert its retained bytes.

## Verification summary

The final race-enabled experiment run passed **37 tests/subtests in 162.09 seconds**. Its two skips are the intentionally subprocess-only worker helpers. PostgreSQL, real Temporal, all four SIGKILL boundaries under each variant, crashes inside the new retention/release Activities, and the original real Docker success case were enabled. All 15 exported service histories replayed successfully. `go vet` and module verification passed. The earlier wrapper-only run passed 19 tests/subtests in 61.38 seconds; its summary remains in the result manifest.

A pre-publication rerun on 2026-10-27 passed the same 37 tests/subtests in 131.99 seconds, including 10 worker-loss cases and 15 successful history replays. Static checks and module verification passed again. The result manifest records this rerun separately.

The full existing Go suite passed 1,489 tests/subtests and skipped 25, but failed the unchanged `internal/codexauth/TestRejectsUnsafeCredentialPaths` fixture under the shell's `umask 077`. It requests a 0644 file that becomes 0600 under that mask, so the intended unsafe credential file is never created. The unchanged credential package passes a separate race-enabled run under `umask 022`. The full root suite was not repeated with the different mask. Existing real-Docker suites and browser tests were not enabled; the experiment's own real-container case was enabled.

The final raw service histories are preserved at `/tmp/circular-temporal-evidence-20261026-split-final/`; the manifest records their event counts and SHA-256 hashes. Reruns produce new random Run identities. The manifest contains the original observed results; the tests and README provide the reproducible procedure.

## Follow-up: separate cleanup recovery

`SplitWorkflow` runs the same non-retrying `execute` Activity as the original. It then calls `CleanupWorkflow` under a disconnected context. That Workflow coordinates three Activities:

1. `claim-cleanup` returns immediately with a settled result, a fresh PostgreSQL cleanup owner, or the remaining lease duration. The Workflow uses `workflow.Sleep` for that duration. A live owner is never replaced early.
2. `retain` stops/removes the exact container under the Run lock, then retains and verifies immutable output through the existing `Retention.Retain` API. It records a separate cleanup error if retention fails.
3. `release` calls the existing `Retention.Cleanup` and `ReleaseClaim` boundaries. The shared cleanup code verifies retained output again before deleting the worktree. The experiment preserves that repeated verification.

A failed retention/release Activity restarts recovery with a fresh owner after the previous lease expires. Both the Workflow and PostgreSQL bound recovery to three attempts/takeovers. The Workflow also bounds lease waiting to six minutes. Short claim Activities can retry delivery three times. Retention and release Activities each have one attempt per acquired owner; they never silently reuse an expired owner. Database heartbeats remain active through each bounded external operation, including cancellation-shielded cleanup.

The alternative no longer calls `Activities.Reconcile`, or the Supervisor's cleanup-only `Execute` path. It still calls `Queue.Acquire`, including its expired-owner reconciliation, and retains all resource and Work-session fences. Initial execution still calls the full Supervisor. No production function is deleted or changed.

### Measured code cost

The [source comparison](../../../experiments/temporal/evidence/source-comparison.json) measures physical function spans with Go's parser after formatting. It excludes tests, DTO declarations, comments before functions and existing effect implementations reused by both paths.

| Compared adapter functions | Source lines |
| --- | ---: |
| Original `RunWorkflow` plus `Activities.Reconcile` | 70 |
| `SplitWorkflow`, `CleanupWorkflow`, and split Activity helpers | 210 |
| Increase | **140** |
| Production coordination lines removed | **0** |

The split removes the Activity's polling loop, but its lease checks, ownership transfer, lease heartbeats, retention-before-delete and exact cleanup still need code. This is a function-source comparison, not a throughput, operating-cost or total repository-size benchmark.

### Follow-up observations

- Both variants pass the same partial-provisioning, accepted-but-unacknowledged start, retention and cleanup worker-loss cases. Backend execution is never repeated; interrupted terminal decisions and retained bytes remain intact.
- SIGKILL during the new retention Activity recovers uncommitted artifact metadata and verifies both artifacts. SIGKILL during the new release Activity preserves the already released Workspace and completes claim release without a duplicate release event.
- A live three-second lease produces **one persisted timer and two short claim Activities**. The waiting period does not keep a cleanup Activity running. This verifies the scheduling change; production worker-capacity savings were not benchmarked.
- A modeled lost cleanup-claim result causes another call to wait for its live owner. After expiry, a fresh owner wins and the lost permit cannot mutate resources. This test discards a committed return value; it is not a transport-failure injection.
- Repeated successful retention/release adds no events or artifacts. Expired owners cannot retain or release another owner's resources.
- Exhausted container-removal recovery keeps the worktree output, Run result, and native Work-session attention flag. Cancellation still retains known backend output and preserves the existing API decision.

Independent review found no blocking defect. The reviewer ran race checks without external services; the parent ran the complete live suite. The full failure matrix uses the existing CLI simulator, while the separate original successful lifecycle uses actual Docker. The split variant's admission is driven by the test harness; the original outbox test remains the admission evidence. Production scheduling, a full initial-phase split and a workflow-version migration remain unevaluated.

## Identity and admission consistency

Use `circular-run/<Run UUID>` as Workflow ID. The Go client explicitly sets open-ID conflict to fail and closed-ID reuse to reject duplicates. On a duplicate, it verifies the Workflow type and its Run-ID memo before accepting the handle. A lost database acknowledgement therefore causes redispatch of the same identity, not creation of another Run.

`Admit` uses the same transaction as `postgres.CreateRun`. A failed outbox insert rolls back the Run and native Work session together. The outbox remains pending after an uncertain start response. The receipt is committed before claiming or invoking the agent. If that commit is uncertain, execution stops. Recovery may fail an attempt that never actually started; this conservative outcome is preferable to repeating an invocation whose effects are unknown.

Temporal provides one open Workflow per ID in a namespace. Closed-history deduplication lasts only within namespace retention. The PostgreSQL receipt remains necessary after that history disappears. Neither Workflow-ID uniqueness nor Activity heartbeats fences an external process or database mutation. [Workflow identity and retention](https://docs.temporal.io/workflow-execution/workflowid-runid), [Go start options](https://github.com/temporalio/sdk-go/blob/v1.50.0/internal/client.go).

The prototype has no production scheduling entry point. Its one-Run-per-schema constraint makes queue-wide acquisition unambiguous. Production adoption would require a durable scheduler selector and changes to **all** PostgreSQL acquisition paths: queued Runs, expired leases, orphaned delegations and continuations. A Temporal-owned Run must never also be eligible for the ordinary worker. That routing change is not demonstrated here.

## External effects and reconciliation

| Effect | Durable identity or fence | Rule after uncertain completion |
| --- | --- | --- |
| Run and native task ownership | Native admission request key, Run UUID and Work-session rows in one transaction | Recover the same admission. Never mint a replacement to hide uncertainty. |
| Repository cache and worktree | Repository UUID, Run path/branch and existing Git ownership receipts | Reuse existing cache locking and partial-allocation reconciliation. Temporal does not own filesystem paths. |
| Container allocation | Run labels, policy digest, allocation nonce and immutable container receipt | Inspect ownership and reconcile the exact allocation. A missing database ID is not proof no container exists. |
| Agent invocation | Pre-invocation receipt plus existing runtime/lease fences | Do not retry execution. Stop/reconcile the allocation. A fresh Run is the explicit execution retry boundary. |
| Events and usage | Run row lock and per-Run event sequence | PostgreSQL remains authoritative for replay/SSE. Backend payloads do not become Workflow history. |
| Diff and archive | Deterministic Artifact identities, immutable URI, size and SHA-256 | Verify existing content and complete its database receipt. Preserve the worktree if retention fails. |
| Final outcome | Run transition policy and lease/invocation fence | Preserve an existing terminal result and its primary error. |
| Cleanup | Exact runtime identity; retain-before-delete; terminal-before-release | Repeat cleanup safely. Bound takeovers and leave actionable resources after exhaustion. |

These guarantees come from the retained Circular implementation: [Supervisor](../../../internal/execution/supervisor.go), [queue/recovery](../../../internal/postgres/queue.go), [resource transactions](../../../internal/postgres/resources.go), [retention](../../../internal/execution/retention.go), [cleanup](../../../internal/execution/cleanup.go), and [runtime reconciliation](../../../internal/runtimes/recovery.go).

Ordinary Temporal Activities can retry by default. An external side effect can occur before its completion is recorded. The spike therefore sets `MaximumAttempts: 1` for execution and uses a separate recovery Activity. It does not claim to restore an external CLI's process memory. Its two-second heartbeat timeout and ten-minute execution timeout are test settings, not proposed production limits. [Activity execution model](https://docs.temporal.io/activity-execution), [retry policies](https://docs.temporal.io/encyclopedia/retry-policies).

## Delegation, review, publication and Work sessions

These are proposed mappings, based on current code. They are not implemented or exercised by the spike.

- **Delegation:** Persist the parent checkpoint and child admission before a durable wait. Give the child its own Run/Workflow and ownership. Resume only after the child's terminal state, released resources and retained result are verified. A signal can wake the parent, but a persisted database predicate must resolve lost or duplicated signals. Preserve the existing explicit invocation-2 continuation and its fence; it is not an automatic retry of invocation 1. The current per-Run experiment receipt does not support this continuation. See [handoff](../../../internal/execution/delegation.go) and [continuation admission](../../../internal/postgres/delegation_continuation.go).
- **PR review:** Launch a separate review Run pinned to its captured head/base and reviewer configuration. Preserve read-only execution and exclusion from competing coding claims. Keep manual approval and automatic-review opt-in. Review failure stays in the review boundary and never retries the coding Run. See [review launch](../../../internal/integrations/pr_review_launch.go).
- **Publication:** Put provider work after successful coding in separate Activities or a separate Workflow. Keep the existing publication intents, freshness checks, actor identity, opt-ins and provider receipts. After a lost provider response, reconcile the original PR/comment/review before posting again. Never wrap coding plus publication in one retry policy. See [GitHub delivery](../../../internal/integrations/github_delivery.go) and [review publication](../../../internal/integrations/pr_review_publish.go).
- **Work sessions:** Keep external sessions and task claims separate from a managed Run's worker lease. `needs_attention` after missed external heartbeats does not release or reassign ownership. Temporal timeouts must not change that rule. Keep claim credentials, explicit user release/reassignment, completion evidence and API/MCP behavior in PostgreSQL. The spike calls native admission and retains the existing Work-session guards. See [session contact state](../../../internal/postgres/work_sessions.go), [native hooks](../../../internal/postgres/work_run_hooks.go) and [completion policy](../../../internal/postgres/task_completion_policy.go).

## Coordination code and operating cost

| Responsibility | This spike | What a later replacement could remove |
| --- | --- | --- |
| Queue polling, claim arbitration and expired-lease discovery | Retained through `postgres.Queue` | Some polling after durable routing/admission and independent safety recovery exist. |
| Lease heartbeats, owner/invocation fences and allocation locks | Retained | Fences remain necessary; generic heartbeat scheduling might move after proof. |
| Supervisor sequencing, cancellation observation and cleanup | Retained | Phase sequencing and wait/retry timers only after narrower, durable Activities exist. |
| Backend ingestion, domain events and outcome policy | Retained | No deletion demonstrated. |
| Artifact retention and exact resource release | Retained | Effect code and validation remain; scheduling may move. |
| Workflow history, SDK, admission outbox and execution receipt | Added | No current equivalent deleted. |

Self-hosting adds Temporal service persistence/visibility, schema upgrades, access controls, metrics, capacity planning, backups and availability work. Temporal Cloud transfers service operation, while Circular still owns worker deployment, credentials, storage and recovery policy. No production cost or throughput benchmark was run. [Production deployment](https://docs.temporal.io/production-deployment), [self-host operating checklist](https://docs.temporal.io/self-hosted-guide/production-checklist).

Workflow history adds pending-Activity, timeout and retry visibility. Correlate Circular Run UUID, Workflow ID and Temporal execution Run ID. Keep streamed events, artifact bytes/hashes and domain state in Circular. The development Compose setup is headless; production UI, metrics and access controls remain unevaluated. [Go observability](https://docs.temporal.io/develop/go/platform/observability).

Workflow code must replay deterministically. A future adoption should pin a Workflow type/version and keep compatible workers for existing histories. Gate changes with replay tests over recorded normal, cancellation and failure histories. Use `workflow.GetVersion`, a new Workflow type, or Worker Versioning for incompatible command changes. This evaluation replays the same code; it does not test a version migration. [Go versioning](https://docs.temporal.io/develop/go/workflows/versioning), [replay testing](https://docs.temporal.io/develop/go/best-practices/testing-suite).

## Rollout, rollback and remaining unknowns

If a later experiment justifies adoption, first add scheduler ownership and a transactional admission/cancellation outbox. Admit only new opted-in Runs to Temporal. Let existing queued/active Runs finish under their original PostgreSQL scheduler. Do not import active CLI attempts as fresh Activities. Keep the original API/MCP contracts and Work-session/completion rules.

For rollback, stop new Temporal admissions and drain existing Workflows on compatible workers. Do not make their Runs eligible for the old scheduler while any Activity can still own resources. An emergency handoff requires fencing, an explicit interrupted terminal result, and cleanup reconciliation before any fresh Run. Do not erase receipts or artifact records during rollback.

Temporal cancellation is cooperative, and termination bypasses Workflow cleanup. Workflow timeout, termination, exhausted cleanup, or inaccessible host storage still needs an independent operator/recovery path. The tested wrapper retains PostgreSQL recovery rather than claiming Temporal eliminates it. [Cancellation and termination](https://docs.temporal.io/encyclopedia/workflow/cancellation-and-termination).

Not established: production deployment/operating cost, prolonged network partitions, stale external processes that continue to modify files after fencing, recovery on a different host or lost disk, server/database outage recovery, version upgrades, history-retention expiry on a real aged execution, delegation, approvals, or provider publication. Crash fault injection uses the CLI simulator; only the separate successful lifecycle uses actual Docker. Browser checks and real coding-model/provider calls are outside this change.

This comparison met the recovery checks but missed its code-deletion target. Keep PostgreSQL orchestration for managed Runs; a production migration is not justified by this cleanup split.
