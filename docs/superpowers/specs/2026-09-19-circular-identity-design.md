# Circular app identity and Linear requests

**Status:** Implemented and fixture-verified in the shared checkout on 2026-09-20. Independent review findings are fixed. Live deployment/setup and acceptance remain separately selected.
**Date:** 2026-09-19
**Tracking:** [ISQ-258](https://linear.app/isqrd/issue/ISQ-258/give-circular-its-own-github-and-linear-identity)

## Intent and success

The user wants people to recognize Circular as the actor on GitHub and Linear, see its agent's work in both services, and invoke it through a Linear mention or issue delegation. Setup belongs in the console and documentation must speak to users. Existing applications, connections, repositories, isolated runs, configurable models/reasoning, and the PR review workflow are the starting point.

Success means a person can enable Circular's identity, connect a Linear team or project to a Circular repository and agent, delegate a test issue, observe exactly one run, and see Circular's progress and result in Linear and app-authored activity on GitHub. A human retains issue ownership and decides whether to merge a PR.

Accepted default: requests start automatically only after someone explicitly enables a routing rule. Offer `automatic` and `approval` modes; neither is enabled by connecting an account. Approval of this design covered that default; no live automation was enabled during implementation.

## Scope and release boundaries

Deliver three independently useful stages:

1. **Identity:** GitHub App installation authentication, Linear app-actor grants, guided setup, and app-authored existing publications. Works without webhooks.
2. **Reception:** dedicated webhook listener, verified durable deliveries, revocation handling, and setup diagnostics. Does not launch runs.
3. **Linear requests:** mention/delegation routing, one coding run per agent session, native session activities, stop, and a console request view.

First release does not implement GitHub comment commands, arbitrary external PR review, GitHub assignee/reviewer management, automatic merging, a bot account for each internal agent, conversational editing of a running workload, or a hosted Circular relay service. Those are separate extensions. Existing PR reviewers continue to review PRs published from Circular runs.

The external identity is one installed Circular app per provider account/workspace. Internal agent name and role appear in messages; they are not separate GitHub or Linear users. Use provider-returned names, avatars, actor IDs, and GitHub bot login; never assume the application is literally named `circular`.

## Approaches considered

| Approach                                          | Benefit                                                        | Cost                                                                                  | Decision                |
| ------------------------------------------------- | -------------------------------------------------------------- | ------------------------------------------------------------------------------------- | ----------------------- |
| Extend existing provider apps and delivery queues | Reuses installations, encrypted storage, and recovery behavior | Requires explicit separation of user grants and app grants                            | Selected                |
| Dedicated human service accounts/API tokens       | Familiar user profile                                          | Extra accounts and token administration; does not supply native Linear agent sessions | Rejected                |
| Hosted identity and webhook relay                 | Could eventually offer very simple hosted onboarding           | Introduces account login, tenancy, remote secret custody, and a new service           | Separate future project |

## Existing implementation and seams

| Area                                                             | Current behavior                                                                         | Planned seam                                                                            |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `internal/integrations/apps.go`                                  | One encrypted app registration per provider; GitHub manifest discards PEM/webhook secret | Retain app signing material and verified public identity metadata                       |
| `internal/integrations/service.go`, `providers.go`               | Project-scoped user OAuth, browser/state/PKCE binding, locked token refresh              | Keep user authorization; add workspace/installation app identities and project bindings |
| `internal/integrations/imports.go`, `github_create.go`           | User-token imports, clone/fetch, personal repository creation                            | Resolve credentials by operation; personal repository creation stays user-authorized    |
| `github_publish.go`, `pr_review_publish.go`, `linear_updates.go` | Durable outgoing requests and receipt reconciliation                                     | Capture author identity before a write and use it during recovery                       |
| `internal/httpapi/background.go`                                 | Independent durable integration workers                                                  | Add inbox/session/outbox consumers without calling the control API over HTTP            |
| `internal/httpapi/api.go:createRun`                              | Transactional keyed launch, task/agent checks                                            | Extract reusable launch logic before using it for external requests                     |
| `internal/postgres/execution.go`                                 | Coding inputs read from current task/agent; PR review inputs snapshotted                 | Snapshot external request inputs without changing existing coding behavior              |
| `apps/web/src/pages/integrations.tsx`                            | Large connection/import page                                                             | Add focused identity, reception, routing, and request components                        |

## Global constraints

- Use Go 1.27.1, PostgreSQL 17, the existing React/shadcn UI, generated OpenAPI client, and Fumadocs user documentation; no new framework or queue service.
- Use the existing encrypted integration vault; credentials never appear in public API responses, MCP results, logs, run inputs, or workload containers.
- Keep the control API and MCP private; public ingress exposes only the dedicated webhook receiver.
- Existing connections and automation settings remain unchanged until an explicit setup or enable action; migrations do not backfill publications or start runs.
- Preserve numeric provider identities, publication receipts, original authors, and idempotency across retries, restarts, renames, and reconnections.
- Use existing agent model/reasoning controls and the repository's Astra default; inbound text cannot change credential access, routing, or execution policy.
- Test against owned provider fixtures and disposable PostgreSQL/Docker resources; use live providers only for separately selected acceptance actions.
- Preserve the shared checkout's existing changes; this planning work does not authorize staging, committing, pushing, deployment, or live automation.

## Identity storage and ownership

Keep `integration_apps` as the encrypted server-wide registration. Extend its encrypted GitHub record with app ID, private key, and optional webhook secret. Preserve client credentials and slug. Verify uploads by signing a JWT, calling GitHub's app endpoint, and matching the returned client ID to the registered app before replacing any key. Fetch the bot actor from GitHub and retain its stable numeric ID. New manifest exchanges retain the signing key automatically; existing installations get a single PEM file-upload step. Environment-managed credentials remain read-only in the console; provide a file-based private-key environment option with no API-supplied filesystem paths.

Introduce `provider_identities`: UUID, provider, app client ID, provider account ID (GitHub installation or Linear workspace), external actor ID/name/login, enabled/status, encrypted credentials where needed, scopes, and credential generation. Uniqueness is `(provider, app_client_id, account_id)`. A Linear app token is stored once per workspace, not copied into every project's connection; refresh serializes on this identity row.

Introduce `integration_identity_bindings`: project/provider to identity UUID with an enabled flag. A binding is enabled explicitly after capability verification. Existing `integrations` user credentials remain available for existing imports and personal GitHub repository creation. Normal project disconnect disables its binding while retaining the selected app mode; it must not revoke a shared app grant used by another project or revert publications to the user. Only projects that have never selected app mode default to legacy user mode. Global removal uses the provider's existing app settings, linked alongside the affected projects; a separate console-wide uninstall/revoke operation is outside this release.

OAuth state for app installation records purpose, initiating project/browser, requested scopes, app client ID, expected workspace when reconnecting, and generation. A callback cannot silently attach a different workspace or overwrite a newer successful grant. Bind the verified workspace `organization.id` and app `viewer.id` before switching a project to the new grant. A failed/cancelled upgrade leaves the old usable connection intact.

### Credential selection and publication

Choose credentials by a typed operation, not a global replacement of `withToken`:

- User authorization handles account/installation selection and personal GitHub repository creation.
- Enabled GitHub identities issue repository-scoped installation tokens for trusted clone/fetch, draft PR publishing, PR inspection, and review publishing. Refresh before expiry; retry authentication once for a safe read, not by blindly repeating a write.
- Linear app grants handle selected project's issue reads and new outgoing comments; legacy projects retain user grants until explicitly upgraded.
- A configured app credential failure pauses the operation and shows a repair action. It never silently posts as the user.

Public status separates `connected account`, `acting as`, and capability readiness. A key being present is not proof that an installation still grants repository access.

Store a publication actor snapshot on existing GitHub delivery, review publication, and Linear update ledgers: mode, identity UUID when applicable, provider actor ID, provider account ID, and app client ID. Freeze it before the first remote mutation. Rows already marked started keep their recorded author and original receipt markers. Previously unstarted rows are bound once at their next attempt; later connection changes do not rewrite that intent. A recovered remote receipt must match actor, target, content marker, and existing workflow-specific checks. An uncertain remote write is reconciled before another mutation is considered.

Legacy exception: old draft-PR and ordinary Linear comment rows do not record an author, and Linear comment attempts can lose their local transaction after a remote success. Before upgrading identity, reconcile their existing deterministic PR marker or comment UUID using the existing exact target checks; record the observed historical actor only from a confirmed receipt. Do not fabricate an original author from the current token. For a pre-upgrade pending row whose write history is uncertain and no receipt can be proven, show `uncertain`/repair-needed and prohibit an automatic repost under the new identity. Stage 1 adds durable started/actor reservations for all future writes, including ordinary Linear comments.

Provider facts: GitHub attributes installation-token requests to the app and limits tokens to the installation's granted access. Linear supports app installation using `actor=app`. Sources: [GitHub installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation), [Linear OAuth actor authorization](https://linear.app/developers/oauth-actor-authorization).

## Console experience

Use the existing Integrations page with three separate capability states:

1. **Circular identity:** verified name/avatar, actor mode, account/workspace, permissions, repair action. GitHub offers PEM upload for existing apps; Linear opens an admin OAuth reconnect. Display affected projects before replacing a shared grant or key.
2. **Receive requests:** public receiver address, provider configuration, most recent verified delivery, and explicit waiting/error states. Identity can be ready while reception is unconfigured.
3. **Start work from Linear:** route to repository and enabled agent, model/reasoning summary, automatic/approval mode, and a separate enable action. Existing switches for PR publishing and PR review remain visible and independent.

Reuse shadcn controls, current Markdown rendering, loading/empty/error states, keyboard support, and mobile layouts. Do not require client IDs, JWTs, token commands, or internal database concepts in routine user flows. A generated key is uploaded directly to Circular; it is never requested in a chat or copied into task text. Provider branding is edited through the provider app settings link.

Show requests under Integrations and at `/requests/{request_id}`. The detail view gives source issue, requester, selected project/repository/agent/model, current status, request text, linked run/PR, and relevant actions. Unmapped requests appear in an installation-level inbox accessible from Integrations; a project filter must not hide them.

## Webhook receiver

Add `cmd/circular-webhooks` and `internal/webhooks`. The listener serves `POST /webhooks/github`, `POST /webhooks/linear`, and a minimal `GET /health`. It does not mount the normal API, MCP, OAuth callbacks, artifacts, or setup handlers. Compose binds it to loopback port 8001 by default. A hosted reverse proxy or a stable HTTPS tunnel can expose this listener. `CIRCULAR_PUBLIC_API_URL` remains the browser OAuth/control origin; the independently stored webhook origin must be public HTTPS.

This release supports a configured public endpoint; it does not provision a tunnel provider or buy a domain. The console explains this one-time deployment prerequisite, supplies exact provider URLs, and automates provider configuration where supported. GitHub's app webhook configuration API can set URL/content type/secret; provider-only event subscription changes remain a guided settings action. Linear's app webhook secret and Agent session event checkbox remain guided app settings steps. Enable Agent session events only when stage 3 is deployed and ready. [GitHub webhook configuration](https://docs.github.com/en/rest/apps/webhooks), [Linear webhook requirements](https://linear.app/developers/webhooks).

Receiver rules:

- Limit body to 1 MiB, require JSON, verify HMAC-SHA256 over raw bytes using constant-time comparison, then parse. Linear's signed `webhookTimestamp` must be within 60 seconds of receiver time. Do not trust an unsigned header as a substitute for that value.
- Return 200 only after durable acceptance, or for a verified duplicate/unsupported event that needs no processing. Return 401 for an invalid signature, 400 for malformed required fields, 413 for oversize bodies, and 503 for storage failure. Complete the handler within 4 seconds; make no provider or model calls there.
- Use `(provider, app_client_id, delivery_id)` uniqueness and retain a SHA-256 body digest; the same ID with a different body is rejected. Domain consumers also deduplicate on provider session/activity identity because headers alone do not protect against replay.
- Store encrypted raw payloads with a seven-day expiry. Retain small delivery/semantic deduplication receipts while referenced requests/runs exist. Do not log payloads, signatures, secrets, or token responses.
- Process with PostgreSQL leases and `SKIP LOCKED`; handle process death between acceptance, routing, and launch. Never hold an open database transaction while making a slow provider call.
- Store capability diagnostics from actual provider checks and verified delivery receipts. Merely saving a URL must never show reception as verified; a localhost browser reachability check is insufficient.

GitHub's first inbound events are installation/repository-access changes, not comment commands. Revalidate affected identities and imported numeric repository mappings. Linear app revocation and permission changes disable affected capability/routing. Treat these events as invalidation signals and verify fresh access before later operations; a delayed event cannot incorrectly reactivate an identity.

## Linear routing and requests

Agent installation adds `app:mentionable` and `app:assignable` to the required app OAuth scopes, with targeted comment permissions. Verify the current agent activity/session mutations under this grant in a disposable provider acceptance workspace; if the provider requires general `write`, add an explicit additional capability upgrade and record why. Never silently request `admin`. The documented agent APIs are a Developer Preview, so isolate them in a small adapter and record provider schema fixtures. [Linear agent setup](https://linear.app/developers/agents).

Each enabled route belongs to a verified Linear identity and an exact Linear project or team. It selects one Circular project, repository, and enabled coding agent. A project-specific route wins over its team's fallback. Enforce uniqueness for each identity/scope; save rejects duplicate routes instead of choosing the first project in the database. A repository and agent must belong to the chosen Circular project. An existing imported issue with a different repository is a routing conflict, not permission to move the task.

Incoming `AgentSessionEvent.created` yields one durable external request keyed by `(identity_id, session_id)`. Preserve a bounded immutable prompt/context snapshot of at most 128 KiB; oversize input receives an explicit error. Validate the workspace, app actor, issue/team/project identities, current issue access, and route at launch. No matching route produces `needs_routing` and an explanatory Linear activity, without executing a model. An `approval` route produces `awaiting_approval`; an automatic enabled route queues launch. A session with no responsible human creator requires console approval even on an automatic route. Resolving an old/unmapped request requires an explicit launch action; enabling a route does not replay historical requests.

The transition from approved request to task/run is one transaction. Reuse the existing Linear issue import identity and keyed launch invariant. Lock the request and route, recheck enable/generation and matching task/repository, create or reuse the task, snapshot prompt/agent settings, insert the run with request key `linear-session:<session UUID>`, and save its ID on the request. At most one run exists per session; delivery retries and process crashes cannot start another attempt.

Preparation exposes a fingerprint of the canonical normalized prompt, agent settings, repository identity, and route generation. Start checks that fingerprint against a fresh prepared snapshot; changed settings before approval return a conflict. Once queued, the committed snapshot is immutable. Allow only one queued/running external request per identity/issue; another session waits visibly with the active run link and requires an explicit later start instead of launching competing work.

Only issue-backed sessions are supported initially. A document mention without an issue receives an explanation of supported entry points. Accept native delegation as intent to work; preserve the human assignee. Requests do not create agents or change their permissions automatically; existing agent proposals keep their review/apply flow.

## Linear progress, stop, and follow-ups

Use a separate durable agent-activity outbox, keyed by request plus semantic phase/message ID. Stage 3 prioritizes acknowledgement and cancellation over routine progress. Healthy-service target: provider webhook accepted within 4 seconds and the initial activity or session link sent within 10 seconds; provider downtime is visible as delayed delivery, never as a successful acknowledgement.

Publish short operational updates: received, waiting for route/approval, queued/running, final result, or error. Include internal agent name and links to the run and available PR. Never forward raw model reasoning, tool logs, diffs, credentials, or arbitrary events. Set `externalUrls`, not the deprecated singular field. Send a periodic operational status at most once per minute during long runs so the session does not silently go stale; coalesce pending status behind terminal results. [Linear agent interaction](https://linear.app/developers/agent-interaction).

Persist a UUID before `agentActivityCreate`; use the provider-supported activity ID field and query it before retrying after an uncertain response. Verify that a receipt belongs to the intended session/actor. Keep ordinary Linear comments for manually launched runs; suppress duplicate start/result/PR/review comments for session-linked runs and send those updates through their session outbox instead. Publication failures do not change the coding result.

Handle a verified `stop` signal before launch/progress work: fence the request, cancel the linked queued/active run using existing cancellation semantics, and suppress new queued publication or automatic PR-review launches caused by this request. Send only the final stopped acknowledgement after cancellation. Already committed remote changes remain linked; if an external write was in flight, reconcile its receipt without initiating another write. Test stop racing completion and publication. [Linear stop signal](https://linear.app/developers/agent-signals).

The durable write reservation is the boundary: stop prevents any later reservation; already reserved external requests may finish and require read-only receipt reconciliation. Stop also cancels active descendant reviews launched for the request. A definitive app revocation stops active external work; an ordinary transient provider outage does not pretend to revoke access. Late create/status events cannot clear a stop tombstone.

The initial version executes one request per session. `prompted` messages are durably recorded; stop is supported, and other follow-ups receive a concise status/help reply that explains they do not modify the running instructions and links to the console. Do not claim live conversational steering or automatically replay a follow-up against a fresh default branch. Continuous conversation and safe branch/workspace continuation require a separate design.

## Proposed persistence additions

| Stage | Tables/columns                                                                                                                | Key invariant                                                                   |
| ----- | ----------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| 1     | `provider_identities`, `integration_identity_bindings`, app OAuth states, actor snapshots on publication ledgers              | One stored app grant per provider account; immutable author once writing starts |
| 2     | `integration_webhook_settings`, `integration_webhook_deliveries`                                                              | Durable acknowledgement, signature verification, bounded payload retention      |
| 3     | `external_request_routes`, `external_requests`, `external_request_messages`, `external_run_inputs`, `linear_agent_activities` | One run per session, immutable execution inputs, duplicate-safe activities      |

Allocate additive migrations 0013/0014/0015 if still free at execution time; rebase numbering onto the then-current sequence without rewriting an applied migration. Existing project user grants are not relabeled as app grants. Capture provider identity in typed columns rather than trusting arbitrary `external_refs` to select credentials.

## Testing and acceptance

Identity fixtures must cover wrong-app PEM, expired/cached token rotation, repository access removal, renamed bot, same-workspace grant reuse, different-workspace reconnect, concurrent refresh, and secret redaction. Publication tests cover a lost response across an identity upgrade, author mismatch, and legacy pending rows. Existing personal repository creation/import tests must remain green.

Receiver tests cover raw-body signature verification, tampering, stale signed timestamps, duplicate headers and semantic IDs, oversized payloads, database failure, process restart, and requests to forbidden control/MCP routes. Browser tests distinguish identity-ready, receiver-waiting, and automation-enabled.

Request tests cover team/project precedence, conflicting task repository, disabled/wrong-project agent, duplicate events across processes, immutable model/prompt snapshots, stop-before-created ordering, stop versus queued publication, token revocation, and non-issue mentions. Exercise the whole route with real disposable PostgreSQL, owned provider fixtures, and the fake Docker workload.

Live acceptance is a separate final exercise after review: configure the existing apps, deliberately enable one `circular-test` route, mention/delegate one designated test issue, inspect app-authored output and one run, then test stop. Do not run a paid model, post provider test comments, expose an endpoint, or enable a live route as a side effect of planning.

## Delivery and rollback

Each stage ships with console controls, generated API types, user docs, and migration/upgrade coverage. There is no frontend-only happy path. Existing durable publication statuses and retained artifacts survive deployment.

Disable a route to stop future starts; surface already active work with a separate stop action. Identity disable pauses new app-authored delivery without switching authors. Remove public ingress independently if reception must be rolled back; manual console work continues. Roll back behavior using feature settings and forward-compatible code rather than deleting identity, request, or receipt rows.

## Review checklist

- [x] Concrete intent and first-release boundaries captured.
- [x] Existing code seams and legacy compatibility identified.
- [x] Credentials and provider-account ownership defined.
- [x] Public ingress kept separate from the unauthenticated local control plane.
- [x] Run creation, author binding, cancellation, and remote uncertainty specified.
- [x] Three staged plans and fixture/live acceptance boundaries defined.
- [ ] User review of draft automation default and written design.
- [ ] Implementation and provider acceptance.
