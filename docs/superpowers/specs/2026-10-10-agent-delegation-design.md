# Agent delegation: accepted design

Status: accepted for implementation with the eight-task plan on 2026-10-14. The accepted scope includes the objective object (`title`, `description`), automatic parent continuation, lifecycle limits and owner-managed policy below. Deployment, live-model validation and live enablement remain separate operational actions.

## Intent and scope

Let an existing Agent request specialist work from another existing Agent and receive its result through Circular. This thread owns execution and integration logic; another agent owns Task planning and Kanban work.

The proposed first increment supports one child Run per parent Run and one delegation level. The child can perform ordinary coding or analysis work. It cannot delegate further. Existing dedicated PR-review Runs retain their separate launch and publication contracts.

The parent automatically continues with the child's outcome and artifacts, within its approved permissions. This handoff requires no additional human review and grants no additional permission to merge changes or publish to providers.

## Existing code to reuse

| Existing implementation | Consequence for this design |
| --- | --- |
| `internal/migrate/0001.sql` | `delegations` and `runs.parent_run_id` already exist; extend their contracts instead of creating another hierarchy. |
| `internal/postgres/run_launch.go` | Reuse transactional Task/Agent validation and Run creation. Delegation also needs idempotency before a child Task exists. |
| `internal/agenttools/server.go`, `internal/agentproposals/spool.go` | Agents already have a private MCP server and bounded local output. The worker validates output before it changes durable state. |
| `internal/worker/worker.go`, `internal/codexauth/auth.go` | Execution is serial per worker, and subscription authentication is locked across a Codex invocation. A parent cannot keep that invocation alive while waiting for its child. |
| `internal/codexworkload/workload.go` | Codex invocations are ephemeral. Continuation requires recorded context; an existing native session-resume facility is not assumed. |
| `internal/execution/cleanup.go`, `internal/postgres/execution.go` | Cleanup and claim release currently require a terminal Run. Suspending a parent needs a separate, explicit resource transition. |

## Accepted objective object

```go
type DelegationObjective struct {
    Title       string `json:"title"`
    Description string `json:"description"`
}
```

These fields become the child Task's title and description. The description states the requested work, constraints, and expected deliverable. It is instruction text, not an authorization mechanism.

The proposed tool arguments remain:

```json
{
  "agent_id": "<existing Agent UUID>",
  "request_key": "review-authentication",
  "objective": {
    "title": "Review authentication changes",
    "description": "Inspect the supplied diff for authentication bypasses and token-expiry errors. Return actionable findings with file locations, evidence, and suggested fixes."
  }
}
```

Proposed validation: a nonempty title of at most 500 Unicode characters, a nonempty description of at most 20,000 characters, and a nonblank request key of at most 200 characters. Reject invalid UTF-8, NUL, duplicate or unknown JSON fields, and oversized input. Normalize surrounding whitespace once, before fingerprinting and persistence. Keep the accepted description intact in the child Task.

The worker derives the parent Run, Project, Repository, source snapshot, and effective permissions. Tool arguments cannot choose those identities, credentials, publication permissions, model settings, or execution commands.

## Authorization and idempotency

Delegation is disabled unless the owner has allowed the parent Agent to use specific target Agents. That policy belongs to Circular, outside Agent instructions and backend model configuration. Policy management uses the authenticated control API/MCP; it does not require a new Task UI.

Before admitting work, Go verifies that the parent is still eligible, its worker claim is current, the target exists and is enabled in the same Project, the target supports the requested execution kind, and the owner policy permits it. The child uses the target Agent's approved configuration. A child cannot gain authority through objective text or by inheriting the owner's full control MCP connection.

Enforce a unique delegation request identity scoped to the parent Run, with a fingerprint of the normalized target and objective. Identical replay returns the recorded outcome; a changed payload under the same key is a conflict. The one-child limit is enforced under the same database lock as admission. Rejected requests do not create Tasks or Runs, and cannot trigger an unbounded automatic continuation loop.

Serialize admission with parent cancellation and relevant policy changes using the repository's established lock order. A database transaction creates the child Task, queued Run, delegation relationship, and ordered Events together. Failed or uncertain commits are reconciled by their original identity before any additional creation.

Use `delegations` to identify cancellation and result-delivery relationships. `parent_run_id` alone also describes existing PR reviews and must not accidentally change their behavior.

## Proposed handoff and continuation

1. **Request locally.** `delegate_task` records a bounded request in the current invocation's private spool. Its reply says `pending_handoff`; it does not claim that a child Run already exists. Only one distinct request is accepted. The parent finishes its current model invocation normally.
2. **Validate and checkpoint.** After a clean invocation exit, the worker validates the request and captures the parent's immutable execution inputs, recorded progress, repository base, and current workspace contents. Reuse artifact integrity and path-validation patterns. Incomplete or unverifiable capture prevents child launch.
3. **Suspend safely.** Stop and remove the exact parent container, preserving a verified checkpoint and the parent-owned workspace. Only then atomically admit the child and place the parent in `waiting_for_input` with an explicit delegation reason. A dedicated suspension operation releases the claim; normal terminal cleanup rules remain strict. The Workspace needs an explicit suspended state and a verified path back to execution.
4. **Execute the child.** The existing queue and supervisor run the child in its own Workspace, initialized from the captured parent source. The child cannot write the parent's workspace. Source restoration must preserve binary changes and untracked files while rejecting archive path escapes, protected Git metadata, and unsafe links.
5. **Record the result.** Associate the child outcome and verified report/diff artifacts with the delegation. Wait for child resource release before resuming the parent so the shared login lock is free. Failed or cancelled child work is returned as that outcome, without pretending the requested result exists.
6. **Continue the parent.** Once the child outcome is durable and its resources are released, automatically claim the eligible waiting parent exactly once and start a fresh invocation from its original inputs, verified checkpoint, and child result. Parent cancellation prevents continuation. No human-review decision is required for the handoff. The first version has no remaining delegation allowance after this handoff.

A continuation belongs to the same logical parent Run; it is not a fresh execution attempt or a silent retry of failed work. Record invocation boundaries and enforce ownership of the current invocation so stale output cannot affect a resumed Run. The exact continuation input is persisted before execution. Model output and child artifacts remain untrusted data when building that input.

The parent receives the child's patch as an artifact. Applying it is a deliberate parent action against the recorded base, with conflicts surfaced rather than overwritten. Checkpoint artifacts are internal execution inputs; they are not final successful output eligible for publication.

## Failure, cancellation, and publication rules

- Cancelling a parent closes admission and cancels its unfinished delegated child in the same coordinated state change. Completed child history remains intact.
- A suspended cancelled parent must become eligible for cleanup even though it has no active worker claim. Do not leave its workspace outside the recovery queue.
- A restart while the parent is safely suspended can continue from durable state. A crash during active model execution retains the existing failure-and-cleanup behavior; it does not silently re-execute an ambiguous attempt.
- A crash during handoff must leave either the original owned execution or a verified suspended parent and one child. A lost commit acknowledgement must not create a second child.
- Child Runs retain independent Events, artifacts, and usage. Parent usage must account for both parent invocations; the current API reads only the latest usage event, so that projection needs deliberate updating without counting child usage twice.
- Delegated children do not automatically publish PRs or Linear updates. Update automatic publication eligibility explicitly; empty inherited provider references alone are insufficient protection against Project-wide publishing settings.
- The original parent keeps its existing provider request identity. A planned handoff does not publish success, failure, or a final result. Final delivery happens only after its actual terminal outcome and existing publication checks.

## Alternatives considered

**Recommended: durable sequential handoff.** Reuses the existing queue, isolated execution, and shared subscription login. Its main implementation cost is safe suspension and continuation.

**Concurrent child execution.** Allows the parent to remain active, but requires additional worker capacity and a separately validated authentication design. It does not remove the need for durable delegation records and result ownership.

**Delegation with manual follow-up only.** Creates child work and leaves result consumption to the user. This option was not selected: the user wants the parent to continue automatically with the child's outcome.

## Implementation scope and verification

The detailed implementation plan should cover storage and policy, MCP/output validation, checkpoint and source capture, suspension and continuation claims, cancellation/cleanup, publication eligibility, HTTP/MCP contracts, and fixtures. Reuse the existing Go packages and dependencies. Coordinate migration ordering and any shared schema/generated-client changes with the Task planning agent; do not reserve a migration number in this draft.

First prove one fake parent requesting one fake child, releasing the worker and login lock, then continuing with the child's result. Add failure cases at every durable handoff before enabling real-model execution.

Acceptance requires:

- One child for concurrent identical requests; a clear conflict for changed replay parameters.
- Rejection of forged parent identities, cross-Project targets, disallowed or disabled Agents, nested delegation, and excess children.
- No active parent invocation holding the shared login lock while its child waits to execute.
- An eligible parent continues automatically exactly once after a succeeded, failed, or cancelled child has a durable outcome and has released its resources. There is no additional review step or automatic retry of child work.
- Parent cancellation racing with admission, child completion, and continuation cannot launch additional work or overwrite a terminal outcome.
- Restart tests before and after checkpoint publication, admission commit, claim release, and continuation claim preserve data and resource ownership.
- Checkpoint capture/restore handles binary and untracked files and rejects unsafe paths; missing or corrupt artifacts stop continuation.
- No premature Run success, unintended child publication, duplicate usage accounting, or changes to existing PR-review behavior.
- Existing direct launches, Linear requests, Run recovery, and Task content locks continue to work.

## Review status and limits

The objective shape and automatic parent continuation are accepted. One child per parent, policy administration, and lifecycle details still require design review. This is a design proposal, not an implementation plan or an approval to run real models.

The proposed [implementation plan](../plans/2026-10-11-agent-delegation.md) turns this design into eight sequential, testable tasks. Its remaining design choices are presented together for plan review; implementation has not started.

Fresh invocation from recorded context is not exact restoration of the model's hidden reasoning or an ephemeral Codex session. The checkpoint and continuation proof must establish useful behavior before release. A child-count limit bounds the number of delegated Runs; it is not a monetary spending cap.
