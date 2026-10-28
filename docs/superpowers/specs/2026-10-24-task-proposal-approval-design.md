# Task proposals and attributable user approval

Status: Task mapping and approval scope confirmed on 2026-10-25. Live testing
on 2026-10-27 rejected native Linear prompts as proof of user approval. The user
selected the private Circular review page with separate GitHub sign-in.
Date: 2026-10-24. Updated: 2026-10-27.

## Intent and settled requirements

External coding clients remain the primary executors. When documentation and the
roadmap define eligible work, they claim and complete it through Circular. An
unclear requirement becomes a question. A suggestion for new work becomes a
proposal; it does not become a Task until the user approves it.

Circular and coding-client chat must share the same proposal, version, decision,
and resulting Task. Approval creates work once. It does not, by itself, authorize
a runner, publication, or merge. The app and MCP remain private.

This work precedes the reusable Claude Code/Codex workflow skill. Its implementation
plan must stay on this feature branch and ship with the implementation PR. This
design and its implementation ship together. Deployment remains a separate step.

## Confirmed Task mapping and approval scope

One imported Linear issue maps to one Circular Task within a Project by default.
The Task represents the same work. It is not inherently smaller or more atomic.
Use linked subtasks when a piece needs its own owner, dependencies, or outcome.
Subtasks can remain local to Circular; creating one does not automatically create
another Linear issue. A parent link alone does not add a dependency or make the
parent complete when a child finishes.

An already-approved Linear issue must not require another approval merely to
create its Circular tracking record. Preserve its verified approval and the scope
it covers. Apply the proposal flow to new agent-proposed work or a change in scope.
An internal record, an execution attempt, and new product scope are different
things; do not use record creation alone to decide whether approval is missing.

The presence of a Linear issue is not evidence that its work was approved. Use
a durable decision receipt tied to the reviewed scope and an independently
verified user decision. A native Linear prompt cannot supply that proof: the
live test below reproduced it through an agent's API-written comment. Status,
labels, and agent-written approval fields are also insufficient. Missing evidence
means approval is unknown, not that the user rejected the work. Reusing approval
does not bypass readiness, dependency, ownership, or completion checks.

The user selected separate GitHub sign-in on 2026-10-27. This does not add
automatic creation of Linear issues or full status synchronization.

## Verified starting point before implementation

- `internal/httpapi/api.go` and `internal/httpapi/discovery.go` can create Tasks.
  `internal/controlmcp/mutations.go` exposes ordinary Task creation to agents.
- `internal/integrations/imports.go` and `request_launch.go` can import provider
  issues as Tasks. `internal/postgres/delegation_admission.go` creates a Task and
  child Run while admitting native delegation. All are relevant creation paths.
- Saved Agent proposals have their own scope and lifecycle. Reuse their UI and
  transaction patterns where useful, not their records or meaning.
- Console/API access currently trusts the private deployment boundary. MCP
  name/version are display labels. Neither establishes the user's identity.
- GitHub integration already has browser-bound OAuth state, PKCE, encrypted token
  storage, bounded provider requests, and `/user` identity lookup. Those are useful
  primitives, but an integration grant or app installation is not an approval.
- The bundled HTTP MCP server is stateless. It cannot currently complete a
  server-initiated elicitation conversation. A normal review URL works through
  both HTTP and stdio without making approval depend on that capability.

## Selected approach

**Selected: private Circular review page with separate user sign-in.** Chat
proposes work and returns the canonical review URL. The user reviews the exact
version, then approves or rejects through an authenticated browser session.
GitHub sign-in can reuse existing OAuth primitives, but must remain separate
from integration grants and credentials available to coding clients. The first
deployment also needs an explicit approver allowlist. This adds a browser step;
the user accepted that experience on 2026-10-27.

**Chat-only approval through a trusted client integration.** A managed client
component would need to authenticate the user and attest to the exact displayed
version and decision, with credentials unavailable to ordinary model tools. No
such trusted component has been verified in either supported client.

**Ordinary MCP acceptance is insufficient.** Claude Code hooks can answer an
elicitation automatically or replace its result. Codex exposes responses through
its app-server protocol. A client-supplied name or `approved: true` therefore
does not establish human consent. Tool annotations do not authorize decisions.

**Native Linear prompts are rejected as an approval channel.** The live test
below reproduced a user-attributed response through an API-written comment.
Keep Linear as a source of work, links, context, and recorded outcomes. A future
provider mechanism would need a separate, verified provenance guarantee before
it could become a decision adapter.

## Recognizing prior approval for Linear-linked work

A receipt records the proposal revision, immutable reviewed scope, decision,
verified approver, verification method, and decision time. Link it to the Circular
Project and the provider workspace/issue. Retain evidence from the authenticated
decision channel; a Linear activity or comment is contextual evidence only.

During import, match that identity and compare the current mapped scope with the
receipt. Include the reviewed title, description, acceptance criteria, Repository,
dependencies, and completion policy. Use deterministic comparison. Status,
assignee, priority, and scheduling changes alone do not invalidate scope approval.
Reuse the receipt and corresponding Task when scope matches. New or changed scope
requires a new reviewed revision. Provider failure leaves verification pending.

Do not backfill historical consent from issue existence or workflow metadata.
Work approved outside the verified flow needs trustworthy evidence or an explicit
user confirmation. This limitation does not mean the user rejected that work.

## Live verification result, 2026-10-27

**Result: the proposed Linear confirmation flow cannot establish human consent.**
The existing route stayed in manual-approval mode throughout the controlled test.

| Check | Observed result |
| --- | --- |
| App token calls `agentActivityCreate` with `type: prompt` | Rejected with `INPUT_ERROR`: prompt is not an allowed activity type. |
| App token calls `agentActivityCreatePrompt` | Rejected with `FORBIDDEN`: unexpected authentication method `oauthAccessToken`. |
| User selects Approve test | A prompt carried the configured option value and the user's ID. |
| Agent replies through the official Linear plugin | A fresh test choice produced a prompt attributed to that same user, without a user click. |
| Activity and comment provenance | Both responses had null `signal`, `signalMetadata`, `sourceMetadata`, `botActor`, and `onBehalfOf`. Their source comments identified the same user. |
| Provider delivery | Both responses reached Circular as processed `prompted` webhooks with matching activity and user IDs. |

Linear's documented restriction on direct prompt creation does not cover this
indirect comment path. A version-bound choice value identifies the proposal; it
does not establish who initiated the response. Checking the signature, session,
user ID, choice value, and available provenance fields would accept the automated
test response. Session creation also generated an initial prompt attributed to
the app, so activity type alone was never sufficient.

The expired legacy user OAuth credential returned 401 during the earlier
read-only check. Personal API-key mutations, Reject selection, stale decisions,
and duplicate decisions were not tested. The confirmed counterexample already
invalidates this candidate; more tests cannot make these observed fields proof
of a manual decision.

The test request was stopped through the existing private API with no Run
attached. No Circular Task or coding Run was created. Deployment and route
configuration stayed unchanged. No approval implementation was enabled.

The conclusion applies to the tested prompt-based mechanism, not every possible
future Linear integration. The private review flow below is implemented on this
feature branch; it has not been deployed.

References: [Linear interaction](https://linear.app/developers/agent-interaction#activity-content-payload),
[selection signals](https://linear.app/developers/agent-signals#select),
[OAuth actor behavior](https://linear.app/developers/oauth-2-0-authentication).

## Browser approval boundary

Use an explicit instance-level allowlist of immutable GitHub user IDs for the
first private deployment. Derive the acting user from a fresh browser sign-in,
not from a model argument, client metadata, existing provider token, or app bot.
Use the configured GitHub application's OAuth primitives with a distinct purpose
and callback/state storage; signing in must not overwrite project integrations.
Validate state, browser binding, PKCE, app identity, expiry, and replay before
accepting the result. Do not retain provider access tokens once identity is read
unless required by the existing grant semantics; do not revoke a grant shared by
an integration just to discard a sign-in token.

Issue a short-lived, server-side browser session using an opaque random HttpOnly
cookie. Secure cookies outside loopback development, exact allowed origins,
CSRF protection, no-store responses, session expiry/logout, and framing protection
are part of the decision boundary. The allowlist is deployment configuration,
not mutable through unauthenticated project or MCP tools. Missing setup fails
closed for operations that need a human decision.

A review URL contains only proposal identity and requested revision. It grants no
authority. Opening a link or accepting a URL elicitation never approves work.
Approval/rejection requires an explicit authenticated POST for the version the
page displayed. The server derives the actor and records the decision. Never
expose browser cookies, provider tokens, or reusable approval credentials through
MCP. The threat boundary excludes a compromised browser/host or an administrator
with direct database access; a skill cannot protect those environments.

## Shared proposal and decision lifecycle

Persist a proposal ID, Project, monotonically increasing revision, immutable
revision content, author/display provenance, server timestamps, and decisions.
Each revision includes title, purpose, scope, acceptance criteria, Repository,
dependencies, intended completion policy, and bounded supporting sources/findings.
Record missing decisions explicitly; an agent cannot turn an unresolved product
question into Ready work by omitting it from a prose summary.

Edits require the expected current revision. They create a new revision and
invalidate pending decisions on the old one. Rejection is durable. Revising a
rejected proposal returns it to review. An approved proposal and its Task link
remain immutable; further work is a new proposal or an explicit Task edit, not
a second approval that silently creates another Task. Approved Task content remains
bound to the reviewed objective, Repository, dependencies, and completion policy.
An agent cannot use ordinary `update_task` or policy mutation to replace that scope
without a new reviewed decision. Scheduling and priority changes retain their
existing planning semantics. Preserve the frozen completion contract after work
starts; approval cannot weaken it.

The decision transaction uses the existing Project-first planning lock order,
then locks the proposal and verifies revision and actor authorization. A unique
proposal-to-Task association and an idempotent decision identity ensure that
concurrent approvals and retries create at most one Task. Exact retries return
the existing receipt. Changed payloads under an accepted identity conflict.
Stale approval/rejection or a decision against a replaced revision fails clearly.
No provider network call occurs while these database locks are held.

Task creation, dependencies, completion policy, decision receipt, and board
revision change atomically. Fully specified approved work with satisfied
prerequisites and a Repository becomes Ready. Unresolved product questions or a
missing completion policy prevent approval. Approved work without a Repository
or with incomplete prerequisites remains Open. Admission rechecks dependencies,
repository scope, policy, and ownership. Dependency completion does not move the
Task stage automatically. External clients must move eligible Open work to Ready
before claiming it; native admission retains its existing Open/Ready policy.

MCP can list, inspect, create, and revise proposals and return the configured
review location. It can inspect decisions but has no ordinary
`approve_task_proposal` mutation that accepts model-written consent. Read-only
MCP omits all proposal mutations. Console review and any verified client
integration use the same durable records and server-side decision operation.
Linear may display the resulting receipt but cannot authorize it through a
prompt. Approval through one channel makes competing pending requests stale;
retries cannot create a second Task.

## Existing creation paths must respect the rule

The implementation must inventory every production Task insert and its caller.
Do not protect only a new proposal endpoint or only the MCP tool catalogue.

| Path | Required treatment |
| --- | --- |
| Direct Task API / `create_task` | Reuse verified approval when representing the same approved work. New work or changed scope requires a proposal decision. The console hands off creation to the exact-revision review page. |
| Existing Task edits | Prevent semantic repurposing through `update_task`, dependency changes, or completion-policy changes; bind scope changes to a reviewed decision rather than treating an existing Task ID as blanket approval. |
| Discovery Task | Apply the same human decision requirement to the displayed fixed objective; preparing it still does not launch a Run. |
| Manual provider import | Import an already-approved Linear issue into its existing or new corresponding Task without another approval. Retain verified approval and scope. An import without a matching receipt opens the review page; an issue's existence alone does not supply consent. |
| Linear request launch | Preserve verified existing-Task launches. When Circular has no Task yet for approved Linear work, reuse verified upstream approval for the same scope. Do not prompt merely because the local Task is new. An app token or signed delivery alone does not prove that approval. |
| Native delegation | Existing target allowlists remain execution policy, not approval of an arbitrary new objective. Check whether recorded approval covers the proposed child work. New scope requires a proposal decision. Do not infer approval from the parent link or an agent's assertion. Previously admitted children and parent continuation keep their existing recovery behavior. |
| Research recommendations | Save proposals and sources only; reports do not create Tasks. |

The exact handoff for previously automatic new-Task imports/delegations must be
visible and recoverable, with tests for waiting, rejection, and resumption. Never
present denial as success, lose an active parent's checkpoint, or claim approval
alone starts execution. Existing execution authorization must be checked separately.

Pending child approval preserves the parent checkpoint and waits. If the user
rejects the child proposal, resume the parent with that recorded refusal under
its original approved objective, consistent with the existing requirement that
parents continue after child failure or cancellation. Do not fabricate a child
Run, fail the parent merely for the refusal, repeatedly re-propose equivalent
work, or substitute another unapproved objective. The continuation path supports a refusal result without an actual child Run.

Existing Tasks retain history; there is no retroactive deletion or status reset.

## Verification and delivery

Before enabling a client, exercise its actual installed runtime: create a
proposal, follow the selected review flow, record a human decision, and observe
the shared receipt from both clients. Preserve the API-comment counterexample
as a regression case: a Linear prompt must not supply human consent. Verify
that review interactions never launch a Run. For browser review, test forged
origins/cookies and expired/replayed login state. For every enabled channel, test
forged actors, disallowed users, stale revisions, changed retries,
rejection/revision, parallel decisions, dependency changes, and ordinary creation
paths. Verify that unchanged approved Linear work needs no second approval,
repeated import returns the same Task, and unapproved or changed scope cannot
reuse approval. Local subtasks must not create Linear issues. Test keyboard
access, stale-review display, applicable login return/logout, and clear separation
between approval and execution.

Reuse Go services, existing frontend components, OpenAPI/generated contracts, and
current test fixtures. Include focused real-PostgreSQL concurrency tests and browser
coverage, followed by existing regression checks. Do not claim a live sign-in or
client acceptance test from mocked HTTP providers or generated protocol schemas.
No deployment or new public ingress is part of this change.

## Client research evidence, 2026-10-24

- Installed `codex-cli 0.164.0`; `codex app-server generate-json-schema
  --experimental` exposes `McpServerElicitationRequestParams`, its response type,
  and `mcpServer/tool/call`. It also exposes `openai/userVerification`, described
  as a device-authenticated approval containing a proof. Its trust configuration,
  server verification contract, and availability have not been verified; an
  undocumented schema shape is not a supported cross-client solution. Schema
  generation used no model call. Runtime UI behavior has not yet been exercised.
- Claude Code is not installed on this host. Its official docs describe both
  form and URL elicitation, plus automated/replaced responses. This is documented
  support, not a completed real-client acceptance test.
- Heartbeat research is separate: existing `circular-mcp` uses stdio lifetime;
  a client-owned helper may be sufficient, but exit/crash/reconnect and long tool
  calls still require actual verification. The HTTP server must never keep a
  dead client alive merely because the server process is running.

Sources:

- [Claude Code MCP and elicitation](https://code.claude.com/docs/en/mcp#respond-to-mcp-elicitation-requests)
- [Claude Code elicitation hooks](https://code.claude.com/docs/en/hooks#elicitationresult)
- [Codex app-server elicitation](https://learn.chatgpt.com/docs/app-server)
- [MCP elicitation security and URL flows](https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation)

## Implementation rulings (2026-10-27)

Approval supports creation, confirmation of existing scope, and amendments.
Existing Tasks keep their IDs, history, and status. An amendment binds the expected
Task revision and retains source, ownership, execution, and completion locks.
Historical Tasks remain readable; unknown consent cannot authorize a new admission.

Native delegation saves its execution intent and checkpoint before waiting for
approval. A separate worker admission rechecks that intent after approval.
Refusal resumes the original parent with an explicit refusal result and no child
Run. Approval never invokes the worker from the decision handler.

Browser OAuth state expires after ten minutes. Approval sessions expire after
thirty minutes. The allowlist contains immutable GitHub numeric user IDs. Login
uses a dedicated callback and discards the fetched user token without revoking
or changing an existing integration grant.
