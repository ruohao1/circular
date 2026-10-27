# Temporal evaluation plan

**Goal:** Evaluate managed Run orchestration with a disposable Go SDK spike and reproducible failure evidence. Production adoption is a separate decision.

**Baseline:** `195eaf02dcbb74ec08b1a07f30e5d6732cd50137`.
**Scope:** A nested Go module in `experiments/temporal`, this plan, and an evidence/recommendation report. Preserve root dependencies, production scheduling, API/MCP contracts, task policies, Work sessions, and opt-ins.

The spike wraps the existing Supervisor as one non-retrying execution Activity. A separate, cancellation-shielded Activity reconciles interrupted execution through the existing lease-fenced cleanup path. PostgreSQL remains authoritative. This is the smallest adapter that can measure whether Temporal removes existing machinery. Splitting provisioning, retention, and finalization into separately retryable Activities would require new production seams and persistent host/runtime handles; assess that alternative in the report.

- [x] Add SDK workflow checks: one execution attempt, bounded cleanup retries, cancellation, and primary failure preservation. Observe the initial failing check.
- [x] Add a test-only transactional admission outbox, fixed Workflow identity, and a durable execution receipt. Repeat dispatch/delivery must not repeat invocation. Refuse non-test schemas and admit one Run per disposable schema.
- [x] Reuse real Go Supervisor, PostgreSQL, Git, retention, and fake workload. Exercise success, cancellation, partial allocation, uncertain start, retention/cleanup failures, duplicate delivery, and stale-owner fencing.
- [x] Run a local Temporal development server. Kill an activity worker process at each required boundary, restart recovery, and inspect durable Run/artifact/event state. Keep simulator and real-container evidence distinct.
- [x] Compare retained/removable code, durable admission, delegation, review/publication, operating overhead, versioning, observability, rollout/rollback, and remaining unknowns. Record commands and observed results.

No production migration, provider publication, external Work-session expiry, or real coding model invocation belongs to this spike. Keep the plan and spike on the same feature branch.

## Follow-up: separate cleanup recovery Activities

Compare a second Workflow with the original wrapper. Keep production files and dependencies unchanged. Reuse public `Queue`, `Resources`, `Retention` and Docker APIs. Initial execution still uses the Supervisor; the experiment splits only recovery after execution ends or its worker is lost.

- [x] Add failing Workflow tests for separate claim/retain/release steps, durable lease waits, bounded cleanup retries and primary-error preservation.
- [x] Implement short claim acquisition, fenced retention after stopping the container, and exact resource/claim release. Carry only Run and owner identities between Activities. Keep database heartbeats through each operation and reject stale owners.
- [x] Exercise all four worker-loss boundaries against both variants. Also kill the worker inside the new retention and release Activities. Verify output hashes/bytes, duplicate delivery, cancellation, cleanup exhaustion and primary errors.
- [x] Replay both Workflow types and record an exact source comparison. Distinguish the adapter polling/Supervisor recovery call removed from the alternative path from shared safety code that remains necessary.
- [x] Update the recommendation and evidence. Run the full isolated suite with race detection and the actual Docker case, then remove disposable services.
