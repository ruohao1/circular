# Circular Linear Requests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an explicitly enabled Linear mention/delegation start one isolated Circular run, show progress and results, and stop that work from Linear or Circular.

**Architecture:** Normalize verified agent events into durable requests; resolve explicit routes; launch through a shared transactional run operation. Snapshot inputs and use a separate idempotent activity outbox for native Linear sessions. Keep one execution per session in this release.

**Tech Stack:** Existing Go/PostgreSQL execution and integrations, Linear GraphQL adapter, React/shadcn, generated OpenAPI, MCP, Fumadocs, provider fixtures and Docker execution tests.

**Spec:** [Circular identity design](../specs/2026-09-19-circular-identity-design.md), sections Linear routing, Progress/stop/follow-ups, Console experience, and Testing.
**Dependencies:** [Stage 1 identity](2026-09-19-circular-identity-auth.md), [stage 2 reception](2026-09-19-circular-identity-webhooks.md).
**Tracking:** [ISQ-261](https://linear.app/isqrd/issue/ISQ-261/run-circular-agents-from-linear-mentions-and-issue-delegation).

**Execution status (2026-09-20):** All 6 implementation tasks complete in the shared checkout, verified by the [final record](2026-09-19-circular-identity.md#plan-completion-record). The original steps below remain the execution recipe; actual checks and rulings are recorded in `.superpowers/sdd/2026-09-19-circular-identity-linear/progress.md`. Live acceptance is separately prepared in [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test).

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

1. Two API processes receive/replay one session: task 2 creates exactly one run even across a crash boundary.
2. Stop arrives before created or during publication: task 4 preserves a tombstone and cancels unstarted effects.
3. A team fallback overlaps a project route or an existing imported task uses another repository: tasks 1/2 refuse ambiguity and repository reassignment.
4. Agent/task/route settings change after approval: task 2 uses immutable verified inputs or returns a conflict before starting.
5. Linear accepted an activity but its response was lost: task 3 recovers the exact activity UUID and never produces duplicate session/ordinary comments.

## File and interface map

| New file                                             | Responsibility                                                         |
| ---------------------------------------------------- | ---------------------------------------------------------------------- |
| `internal/integrations/linear_requests.go`           | Agent webhook normalization, workspace/app/issue verification, routing |
| `internal/integrations/request_routes.go`            | Explicit project/team route validation and precedence                  |
| `internal/integrations/request_launch.go`            | Transactional request-to-task/run transition and immutable inputs      |
| `internal/integrations/request_stop.go`              | Stop tombstones, cancellation, and publication fences                  |
| `internal/integrations/linear_activities.go`         | Bounded native session updates, receipt recovery, retries              |
| `internal/postgres/run_launch.go`                    | Shared keyed run creation and typed conflicts                          |
| `internal/postgres/run_cancel.go`                    | Shared cancellation transition/event                                   |
| `internal/httpapi/external_requests.go`              | Private routes for request/routing UI and MCP                          |
| `internal/controlmcp/external_requests.go`           | Read request/list tools and capability-gated start/stop                |
| `apps/web/src/components/linear-request-routing.tsx` | Project/repository/agent selection and explicit enablement             |
| `apps/web/src/components/external-request-list.tsx`  | Project and unrouted request lists                                     |
| `apps/web/src/pages/request.tsx`                     | Request detail, source/context, approval/stop/result                   |

Introduce these public types in `request_routes.go` / `linear_requests.go` and keep token/raw-webhook fields private:

```go
type RequestRoute struct {
    ID string `json:"id"`
    IdentityID string `json:"identity_id"`
    ScopeType string `json:"scope_type"` // project or team
    ScopeID string `json:"scope_id"`
    ProjectID string `json:"project_id"`
    RepositoryID string `json:"repository_id"`
    AgentID string `json:"agent_id"`
    Mode string `json:"mode"` // automatic or approval
    Enabled bool `json:"enabled"`
    Generation int64 `json:"generation"`
}
type ExternalRequest struct {
    ID string `json:"id"`
    IdentityID string `json:"identity_id"`
    SessionID string `json:"session_id"`
    IssueID string `json:"issue_id"`
    SourceURL string `json:"source_url"`
    RequesterID string `json:"requester_id"`
    RequesterName string `json:"requester_name"`
    ProjectID string `json:"project_id"`
    RepositoryID string `json:"repository_id"`
    AgentID string `json:"agent_id"`
    RunID string `json:"run_id"`
    Status string `json:"status"`
    Reason string `json:"reason"`
    InputFingerprint string `json:"input_fingerprint"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
type RequestMessage struct {
    ID string `json:"id"`
    Body string `json:"body"`
    CreatedAt time.Time `json:"created_at"`
}
type ExternalRequestDetail struct {
    ExternalRequest
    Title string `json:"title"`
    Prompt string `json:"prompt"`
    AgentName string `json:"agent_name"`
    Model string `json:"model"`
    ReasoningEffort string `json:"reasoning_effort"`
    RunURL string `json:"run_url"`
    PullRequestURL string `json:"pull_request_url"`
    DeliveryStatus string `json:"delivery_status"`
    Messages []RequestMessage `json:"messages"`
}
type RequestListQuery struct {
    ProjectID string
    Unrouted bool
    Cursor string
    Limit int
}
type RequestPage struct {
    Items []ExternalRequest `json:"items"`
    NextCursor string `json:"next_cursor"`
}
```

Service contracts:

```go
SaveRequestRoute(context.Context, RequestRoute) (RequestRoute, error)
RequestRoutes(context.Context, string) ([]RequestRoute, error) // project ID
ExternalRequest(context.Context, string) (ExternalRequestDetail, error) // request ID
ExternalRequests(context.Context, RequestListQuery) (RequestPage, error)
StartExternalRequest(context.Context, string, string) (ExternalRequest, error) // request, fingerprint
StopExternalRequest(context.Context, string) (ExternalRequest, error)
ProcessExternalRequest(context.Context) (bool, error)
ProcessLinearActivity(context.Context) (bool, error)
```

Unrouted list requests use an explicit `unrouted=true` HTTP filter; do not interpret an absent project filter as permission to display another project's filtered results. Current deployment is a trusted single console; these endpoints do not introduce internet-facing account authorization.

### Task 1: App capability upgrade, routes, and normalized requests

**Files:** Create `internal/migrate/0015.sql`, `internal/migrate/external_requests_test.go`, `internal/integrations/request_routes.go`, `request_routes_test.go`, `linear_requests.go`, `linear_requests_test.go`, `internal/testsupport/linear_agent.go`. Modify `internal/migrate/migrate.go`, `linear_identity.go`, `webhook_process.go`.

**Consumes:** Shared Linear identity/scopes, verified webhook inbox, enabled Circular agents and repositories.
**Produces:** Validated route records and one normalized request per `(identity, session)`; typed input and routing conflict errors; supported `purpose=agent` app authorization.

- [ ] Extend the owned provider fixture with agent-session events and activity/session GraphQL endpoints. Store minimal sanitized provider schema fixtures for `AgentSessionEventWebhookPayload`, `AgentActivityCreateInput`, and `AgentSessionUpdateInput`; exclude fields marked internal. Confirm the current contract against [Linear's published schema](https://raw.githubusercontent.com/linear/linear/refs/heads/master/packages/sdk/src/schema.graphql).
- [ ] Test app OAuth scopes and target actor/workspace verification. Start with `read,comments:create,app:mentionable,app:assignable`. Before live enablement, verify agent mutations under that grant in a designated acceptance workspace; if general `write` is required, make it a documented, explicit capability upgrade rather than a silent fallback. No admin scope.
- [ ] Add route tests for project-over-team precedence, duplicate scopes, wrong-project agent/repository, disabled agent, unknown workspace, and an issue already imported to another repository. Add normalization tests for wrong `oauthClientId`, `organizationId`, or `appUserId`, missing issue, oversized prompt, and application-generated sessions without a responsible human.
- [ ] Run `go test ./internal/integrations ./internal/migrate -run 'RequestRoute|LinearSession|ExternalRequest' -count=1`; confirm new failures before implementing.
- [ ] Add routes with unique `(identity_id,scope_type,scope_id)`, enabled=false by default, selected project/repository/agent, mode, and generation. A save validates current provider scope access; disabled routes cannot start work. Updates require the existing generation and increment it atomically. Add request/message records with unique `(identity_id,session_id)` and `(request_id,activity_id)` respectively. Allow missing issue/project/run metadata on a stop-before-create tombstone; identity/session and the stop fence remain mandatory.

```sql
CREATE UNIQUE INDEX external_requests_session
    ON external_requests(identity_id,session_id);
CREATE UNIQUE INDEX external_request_messages_activity
    ON external_request_messages(request_id,activity_id);
CREATE UNIQUE INDEX external_requests_active_issue
    ON external_requests(identity_id,issue_id)
    WHERE status IN ('queued','running');
```

- [ ] Preserve creator ID/name and immutable signed prompt/context. Validate prompt size <=128 KiB; report oversize rather than silently dropping instructions. A missing human creator requires console approval even on an automatic route, preventing app-to-app loops. A missing issue gets a supported-entry-point explanation without a run.
- [ ] Route exact Linear project first, then team fallback. Missing/disabled/conflicting routes create `needs_routing`; approval routes create `awaiting_approval`; healthy automatic routes can become `ready`. A second session for an already-active issue becomes `waiting_for_active_run` with its active run link and requires explicit later start; do not silently queue competing coding attempts.
- [ ] Pass migration/normalization/routing tests with a real disposable database. Verify enabling a route neither replays previously ignored events nor launches old `needs_routing` requests. Record evidence.

### Task 2: Shared atomic run launch and immutable execution inputs

**Files:** Create `internal/postgres/run_launch.go`, `run_launch_test.go`, `internal/integrations/request_launch.go`, `request_launch_test.go`, `internal/postgres/external_inputs_test.go`. Modify `internal/httpapi/api.go`, `run_requests_test.go`, `internal/integrations/imports.go`, `internal/postgres/execution.go`, `internal/migrate/0015.sql` before application, and `internal/httpapi/background.go`.

**Consumes:** Verified request/route, existing task import identity and keyed launch semantics.
**Produces:** Reusable `postgres.CreateRun` plus `StartExternalRequest`/`ProcessExternalRequest`; a run ID committed with the request and its execution snapshot.

Define the shared operation in `internal/postgres/run_launch.go`:

```go
type RunLaunchInput struct {
    TaskID uuid.UUID
    AgentID uuid.UUID
    RequestKey string
    ExternalRefs json.RawMessage
}
type RunLaunchResult struct {
    RunID uuid.UUID
    Created bool
}
// CreateRun(ctx context.Context, tx pgx.Tx, input RunLaunchInput) (RunLaunchResult, error)
```

- [ ] Move existing keyed-create behavioral tests onto this shared operation before changing behavior: same key/same payload returns the existing attempt, changed parameters conflict, agent/task project mismatch fails, and unkeyed calls retain current behavior. HTTP/MCP calls continue using the same operation and current response schemas.
- [ ] Add a concurrent-session launch test using two independent services against the same fixture database. Deliver the same signed session with two delivery headers, process both, restart one service, and assert one task/run/request link. Crash after run insert but before request link must roll back the whole transaction.
- [ ] Add a snapshot test: approve a request, change the agent model/instructions and task description, then provision; the workload sees the approved snapshot. Changing repository binding/clone URL causes a visible conflict. A normal user-supplied `external_refs` key cannot manufacture a trusted session link or suppress normal publications.
- [ ] Run `go test ./internal/postgres ./internal/httpapi ./internal/integrations -run 'RunRequest|RunLaunch|ExternalInput|ExternalRequestLaunch' -count=1` and confirm new failures.
- [ ] Extract `createRun` internals with typed errors that the HTTP layer maps to its existing 404/409/422 responses. Preserve task locking, key lookup before mutable-agent checks, attempt allocation, and backend capture. Resolve provider access before the launch transaction; recheck identity/route generation under lock without provider I/O in the transaction.
- [ ] Prepare the immutable normalized input and SHA-256 fingerprint from canonical JSON, including prompt, agent/backend settings, repository identity, and route generation. A manual start must match its displayed fingerprint against freshly prepared inputs; changes before approval return 409. Automatic start captures the same preparation and generation check without a browser. In one transaction lock request and route, then task; check stop/enable/fingerprint/access generation, create/reuse the issue task with matching repository, snapshot the inputs, and create the run. Use this deterministic key:

```go
launch := postgres.RunLaunchInput{
    TaskID: taskID,
    AgentID: agentID,
    RequestKey: "linear-session:" + sessionID,
    ExternalRefs: refs,
}
result, err := postgres.CreateRun(ctx, tx, launch)
```

- [ ] Store `external_run_inputs(run_id PRIMARY KEY, request_id UNIQUE, snapshot JSONB, fingerprint TEXT)` before commit, then link the run to the request. The snapshot includes project/repository IDs, exact clone URL/default branch, task title/prompt, agent identity/instructions/backend configuration, provider source IDs, and route generation. A task already imported to another repository is a conflict, not an update.
- [ ] In `ProvisioningContext`, use the trusted snapshot only when its relational request/run link exists. Verify digest, selected backend, and current repository identity before allocation; missing/corrupt snapshots fail safely. Existing manual coding and PR review paths retain their behavior. Reuse existing backend normalization/model defaults rather than introducing provider-event model overrides.
- [ ] Pass targeted tests and `go test -race ./internal/postgres ./internal/integrations`. Verify the source prompt and model actually reach the fake workload, not just a database row. Record evidence.

### Task 3: Native session activity delivery and receipt recovery

**Files:** Create `internal/integrations/linear_activities.go`, `linear_activities_test.go`, `linear_activities_internal_test.go`. Modify `internal/integrations/linear_updates.go`, `pr_review_linear.go`, `github_publish.go`, `internal/httpapi/background.go`, and outbox/trigger additions in `internal/migrate/0015.sql` before application.

**Consumes:** Request/run linkage and shared Linear app grant.
**Produces:** `ProcessLinearActivity`; short progress/result/status messages, verified activity receipts, and session external URLs.

- [ ] Add an activity outbox with durable UUID, request/session/actor IDs, semantic key, bounded content, status, attempt/lease/retry fields, started flag, and receipt ID. Use unique `(request_id, semantic_key)`; a stored UUID is reused for every retry of that activity. Initial acknowledgement/stop receive priority over routine updates.
- [ ] Write a lost-response test: create activity is accepted, response disappears, consumer restarts, query by durable UUID succeeds, and the fixture counts one creation. Wrong session/actor/body must never be accepted as its receipt. Test terminal-before-start coalescing, periodic status, scope loss, and provider 429/backoff.
- [ ] Run `go test ./internal/integrations -run 'LinearActivit|SessionDelivery' -count=1` and confirm failure.
- [ ] Use the provider-supported activity UUID field and retrieve by ID/filter before retrying a started mutation. Do not assume an HTTP timeout proves failure. On an uncertain result whose receipt cannot be established, preserve `uncertain` and offer reconciliation rather than a new activity UUID.

```graphql
mutation CircularAgentActivity($input: AgentActivityCreateInput!) {
  agentActivityCreate(input: $input) {
    success
    agentActivity {
      id
      agentSession {
        id
      }
      user {
        id
      }
    }
  }
}
query CircularAgentActivityReceipt($id: ID!) {
  agentActivities(first: 1, filter: { id: { eq: $id } }) {
    nodes {
      id
      agentSession {
        id
      }
      user {
        id
      }
      content {
        __typename
        ... on AgentActivityThoughtContent {
          body
        }
        ... on AgentActivityElicitationContent {
          body
        }
        ... on AgentActivityResponseContent {
          body
        }
        ... on AgentActivityErrorContent {
          body
        }
        ... on AgentActivityActionContent {
          action
          parameter
          result
        }
      }
    }
  }
}
mutation CircularSessionLinks($id: String!, $input: AgentSessionUpdateInput!) {
  agentSessionUpdate(id: $id, input: $input) {
    success
  }
}
```

- [ ] Publish received/queued/running/waiting/result/error activity content from trusted run states and bounded final summaries. Use `externalUrls` for Circular and PR links. Add one coalesced operational heartbeat per minute for long-running sessions; do not mirror raw thoughts/tool logs. Healthy fixture tests must meet initial activity/link target within 10 seconds, while webhook responses remain independent of provider latency.
- [ ] Suppress the existing ordinary Linear start/result/PR/review comments only for relationally linked session runs. Route their PR/review links through the session outbox, including descendant review runs. Leave manual runs' current comment outbox unchanged. Persist the source session relation through review creation so no heuristic based on text URLs is needed.
- [ ] Check application actor/workspace on every delivery and freeze actor in the outbox row. Token refresh is shared with stage 1. Failure to post progress is visible on the request and never rewrites a successful run to failed.
- [ ] Pass tests for duplicate suppression, publication retries, provider outages, long runs, and descendant review linkage. Verify session links remain the configured console origin and never include secrets. Record evidence.

### Task 4: Stop, ordering, revocation, and bounded follow-up behavior

**Files:** Create `internal/postgres/run_cancel.go`, `run_cancel_test.go`, `internal/integrations/request_stop.go`, `request_stop_test.go`. Modify `internal/httpapi/api.go`, `internal/integrations/linear_requests.go`, `webhook_process.go`, `github_publish.go`, `pr_review_launch.go`, `pr_review_publish.go`, `linear_activities.go`, and related tests.

**Consumes:** Request/run state, queued publication/review intents, provider activity signals.
**Produces:** Shared run cancellation, `StopExternalRequest`, a stop fence honored by all subsequent request-related effects.

```go
// CancelRun(ctx context.Context, tx pgx.Tx, id uuid.UUID, source string) error
// Existing HTTP cancellation preserves its responses and lifecycle validation.
// Linear callers use source="linear" and do not erase a terminal run result.
```

- [ ] Add ordering tests: stop arrives before created; two stop deliveries; stop races queued launch; stop races terminal event; app revoke races refresh; route disables before reservation. Run tests with two database consumers to exercise actual locks/leases.
- [ ] Add publication boundary tests: before a durable write reservation, stop prevents POST; after a reserved/in-flight write, record/reconcile its receipt without a new mutation. Stop cancels pending automatic review intents and active descendant reviews started for that request. Existing finished PRs/reviews remain linked.
- [ ] Run `go test ./internal/integrations ./internal/postgres ./internal/httpapi -run 'Stop|Cancel|Revocation|PublicationFence' -count=1`; confirm failures before implementing.
- [ ] Extract current HTTP cancel logic into the shared transaction function, preserving the single run.cancelled event and worker-observed cancellation. When the run is already terminal, retain its outcome while fencing further request effects.
- [ ] Upsert a request/session stop tombstone even if the create event has not arrived. Later create/prompt deliveries may fill in safe metadata but cannot clear the fence or launch. Persist stop and cancellation in the same transaction; prioritize the stop consumer path.
- [ ] Fence outgoing effects at their durable start/reservation point. A stop blocks new reservations; an operation already reserved is in flight and may finish. Document this exact boundary in diagnostics, then reconcile only. Avoid holding a database transaction across an external request. Definitive app revocation stops linked active external work and disables future routes; temporary provider errors only pause unavailable operations.
- [ ] Persist non-stop prompted messages once by activity ID. Send concise status/help explaining that additional instructions are saved but do not change this run, with a console link for a new run. Do not start another run for the same session or pretend to inject text into the current CLI process. Preserve the final stopped state when a late status/result arrives.
- [ ] Pass targeted and race tests, then a fake Docker cancellation test proving the workload exits and workspace is released. Assert no pending publication starts after the stop fence. Record evidence.

### Task 5: Routing/request UI, API, and restricted MCP controls

**Files:** Create `internal/httpapi/external_requests.go`, `external_requests_test.go`, `internal/controlmcp/external_requests.go`, `external_requests_test.go`, `apps/web/src/components/linear-request-routing.tsx`, `external-request-list.tsx`, `apps/web/src/pages/request.tsx`, `tests/browser/linear-agent.spec.ts`. Modify `internal/httpapi/integrations.go`, `schema.go`, `internal/controlmcp/server.go`, `apps/web/src/main.tsx`, `pages/integrations.tsx`, `api.ts`, generated contracts.

**Consumes:** Route/request/status/start/stop service interfaces.
**Produces:** A setup flow ending in explicit enablement, visible request details and recovery, read-only/full-control MCP parity for request operations.

```text
GET  /api/v1/projects/{project_id}/integrations/linear/request-routes
POST /api/v1/projects/{project_id}/integrations/linear/request-routes
     { identity_id, scope_type, scope_id, repository_id, agent_id, mode, enabled }
POST /api/v1/projects/{project_id}/integrations/linear/request-routes/{route_id}
     { expected_generation, repository_id, agent_id, mode, enabled }
GET  /api/v1/external-requests?project_id=UUID&cursor=CURSOR&limit=20
GET  /api/v1/external-requests?unrouted=true&limit=20
GET  /api/v1/external-requests/{request_id}
POST /api/v1/external-requests/{request_id}/route
     { route_id, expected_input_fingerprint }
POST /api/v1/external-requests/{request_id}/start
     { expected_input_fingerprint }
POST /api/v1/external-requests/{request_id}/stop
     {}
```

- [ ] Define `RouteExternalRequest(ctx, requestID, routeID, fingerprint string) (ExternalRequest, error)` in `request_routes.go`. It validates issue/scope and updates a still-unstarted request into approval state; it never starts a run. Unrouted requests expose a fingerprint of their normalized source input; routing replaces it with the full prepared-input fingerprint. Omitted/unknown JSON fields follow existing strict schemas and typed errors. Route updates call `SaveRequestRoute` with its ID and expected generation; a stale edit returns 409.
- [ ] Add browser/HTTP tests for: no routes, setup incomplete, automatic versus approval, wrong-project agent, duplicate route, stale fingerprint, unrouted request, already-active issue, failed delivery, and stop. Add a read-only MCP test denying start/stop while allowing request status.
- [ ] Use the existing styled selects/model summary and Markdown component. Selecting an agent displays its current model/reasoning; the approval preview uses a frozen fingerprint. Initial enablement describes which issues may trigger runs and honors existing separate draft-PR and reviewer settings. A failed check never flips the switch optimistically.

```ts
await page.getByRole("combobox", { name: "Run mode" }).click();
await page.getByRole("option", { name: "Ask in Circular first" }).click();
await page.getByRole("button", { name: "Enable Linear requests" }).click();
await expect(
  page.getByText("Approval required", { exact: true }),
).toBeVisible();
```

- [ ] Show source/requester, repository/agent/model, received text, status/reason, linked run/PR, and last provider delivery on `/requests/{id}`. Unmapped requests are reachable from the installation inbox even when the current project filter has no match. Auto-refresh active requests and unsettled delivery states; terminal settled records stop polling.
- [ ] Register `circular_list_requests`, `circular_get_request`, `circular_start_request`, and `circular_stop_request` using the existing MCP access-mode annotations/guards. Start requires the displayed request fingerprint. Do not add MCP key upload, app installation, route enablement, or webhook-secret access in this release.
- [ ] Regenerate/check OpenAPI and client types; run the new HTTP/MCP/browser tests, frontend tests/typecheck/build, and inspect 390x844/desktop screenshots. Verify keyboard focus and actionable provider repair links. Record evidence.

### Task 6: Full workflow acceptance and user documentation

**Files:** Create `tests/browser/linear-agent-execution.spec.ts`, `docs/user-guide/circular-identity.md`, `docs/user-guide/linear-agent.md`. Modify `cmd/circular-e2e-stack/main.go`, `internal/testsupport/linear_agent.go`, `docs/user-guide/meta.json`, `connections.md`, `agents-and-runs.md`, `pull-request-reviews.md`, `troubleshooting.md`, and `docs/development/integrations.md`.

**Consumes:** All stages and existing fixture GitHub publication/review paths.
**Produces:** Reproducible acceptance evidence and concise user-facing setup/use/recovery documentation.

- [ ] Extend the fixture stack to run the real receiver, API background consumers, PostgreSQL, and fake Docker workload. A signed Linear mention in a mapped automatic route must create one run, deliver app-authored progress/result, create a draft fixture PR if opted in, and attach PR/review results to the same native session.
- [ ] Repeat the same payload across listener/API restarts and different delivery IDs; count one run, one activity per semantic phase, and no duplicate ordinary comments. Change the agent settings while queued to prove the actual workload consumes its snapshot. Record execution artifacts as evidence.
- [ ] Exercise approval, unmapped and non-issue requests, humanless session, second active issue session, provider outage, revoked identity, and stop. Stop a running fake workload and a completed-but-unpublished request; assert the defined publication reservation boundary and no newly started automatic review.
- [ ] Write user docs around three steps: enable Circular's identity, verify request reception, choose where work goes and enable it. Explain mention versus delegation, human ownership, automatic/approval mode, model choice, stop, errors, local console link reachability, and one-run-per-session limitations. Keep encryption/JWT/database details in development docs.
- [ ] Run all checks from the umbrella plan once after the final changes, with owned database/Docker resources and no skipped required integration cases. Complete one independent final code review under the selected execution workflow, address findings, and record the evidence in Linear.
- [ ] Prepare a concrete live test using the existing `circular-test` project: selected test issue, app actor previews, chosen agent/model, explicit route mode, and expected provider messages. Run it only when that live action is selected by the user; do not treat planning approval as authorization to start a paid run or publish test content.

## Exit criteria

- [x] One supported mention/delegation maps to one verified isolated execution.
- [x] Native Linear session updates show Circular's actor, internal agent, and run/PR links.
- [x] Stop, duplicates, restarts, route ambiguity, and provider uncertainty have tested visible outcomes.
- [x] Console and MCP controls reuse the same guarded service operations.
- [ ] User documentation, fixture acceptance, independent review, and selected live acceptance are recorded.
