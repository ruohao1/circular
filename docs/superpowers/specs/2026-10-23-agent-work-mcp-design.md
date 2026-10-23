# External coding-agent work through private MCP

Issue: [ISQ-474](https://linear.app/isqrd/issue/ISQ-474/expose-coding-agent-work-reporting-through-circular-mcp)

## Intent and existing decisions

An agent already running in Claude Code, Codex, or another client discovers a
prepared Task, claims it, reports contact, and finishes its work in Circular.
No replacement coding Run is created. Other clients can inspect ownership.
The approved ownership and completion designs remain authoritative:

- [Ownership and heartbeats](2026-10-19-task-ownership-heartbeats-design.md)
- [Completion policy](2026-10-20-task-completion-policy-design.md)

The user authorized this implementation after merging its prerequisites. This
document records the transport choices made for that work. It introduces no new
ownership policy, public access, automatic execution, or provider synchronization.

## Approach

Thin HTTP handlers call the existing PostgreSQL lifecycle operations; MCP calls
those HTTP handlers. Reusing Run launch would create an unwanted replacement
executor. A second lifecycle in MCP would diverge from native ownership and
completion guards. The existing Go services are the single useful seam.

Use the existing planning list, filters, and revision-bound pagination for
candidate discovery (`status=ready`). Add a coherent Task work snapshot containing
Project description/guidance, Task scope and revision, Repository, prerequisites
and their current stages, current owner, completion policy/evidence, and board
revision. Inspection does not reserve work or promise eligibility; claim admission
rechecks all preparation, dependency, ownership, revision, and completion settings.
A separate session read exposes safe contact/outcome facts and lifecycle receipts,
including a historical session after its Task is reopened.

## Transport contract

- `GET /api/v1/tasks/{task_id}/work`: Task work snapshot.
- `GET /api/v1/work-sessions/{session_id}`: safe session and receipts.
- `POST /api/v1/tasks/{task_id}/work/claim`: caller-generated session UUID,
  32-byte cryptographically random secret encoded as 64 lowercase hex characters,
  expected Task revision, and bounded client name/version. HTTP 201 on creation,
  200 on replay. Return the accepted session, credential, and current Task/board
  revisions. The caller saves the identity and secret before the first request.
- `POST /api/v1/work-sessions/{session_id}/heartbeat`: secret and monotonically
  increasing safe-integer sequence. Duplicate/older sequences do not renew contact.
- `POST /api/v1/work-sessions/{session_id}/finish`: secret, stable operation UUID,
  terminal outcome, nonblank summary, and a completion report for success. Report
  result must equal summary. Successful direct work becomes Done; successful
  review-required work becomes In Review. Failure/cancellation closes the attempt
  without claiming completion. Responses contain the original receipt, current
  session and Task/board revisions. Replays never reapply an old stage change.
- `POST /api/v1/tasks/{task_id}/work/release`: explicit administrator recovery with
  expected session UUID, operation UUID, and reason. Reuse `ReleaseTaskWork`; native
  cleanup retains ownership until safe. This is a user action, not an autonomous
  agent workaround for a conflict. Reassignment controls remain separate work.

MCP tools are `get_task_work`, `get_work_session`, `claim_task_work`,
`heartbeat_task_work`, `finish_task_work`, and `release_task_work`. All writes are
omitted from read-only connections and rejected by their HTTP adapter. Read tools
show no secret or digest. Only the successful claim response echoes the caller's
credential. Never place credentials in URLs, logs, receipts, or conflict payloads.
Client labels are display metadata, not identity or proof of user approval.

Heartbeat every 30 seconds; 120 seconds without accepted contact shows attention
but never releases ownership. No transport invents timestamps, automatic retries,
new operation IDs, or background heartbeats. A client process restart reuses its
saved claim and report parameters. After a lost claim the client stops reporting
and asks the user to resolve it; it never releases another owner automatically.

Requests use the existing strict JSON decoder, bounded bodies, origin checks,
no-store responses, private deployment boundary, and shared transaction locking.
Errors have stable codes, actionable details, safe owner/dependency/completion
blockers where relevant, and a reload path/tool. Generic Task edits and Run launch
continue to use their existing ownership/completion guards.

## Delivery and tests

Update OpenAPI and its generated TypeScript client, MCP descriptions/instructions,
and control documentation. No schema migration, new dependency, new UI screen,
Linear status sync, client plugin, or deployment is needed. Those follow-ons keep
their existing issue scope.

Test real HTTP/MCP against disposable PostgreSQL: claim/replay/restart, exclusive
ownership versus native launch, heartbeats, both completion policies, missing
required evidence, failure/cancellation, revoked sessions and replay after reopen,
read-only denial, strict input and origin boundaries, secret-free inspection and
errors, and existing planning pagination. The database's concurrency tests remain
the foundation for serialization. Finish with the full Go race suite, generated
contracts, frontend checks, and focused browser regression coverage.
