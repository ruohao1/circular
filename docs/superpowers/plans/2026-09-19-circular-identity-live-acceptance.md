# Circular identity: prepared live acceptance

**Status:** The controlled live acceptance passed on 2026-10-03. Exactly one Astra/low acceptance run succeeded, producing the requested 35-byte file and app-authored draft PR #2. Linear received native progress, completion, and request/run/PR links. Both permission-recovery follow-ups are fixed and deployed. The owner then approved real work in `ruohao1/circular`. The route now targets that repository in Circular project **Test 1**, at generation 12 in manual approval mode. The owner approved [ISQ-365's request](http://localhost:18080/requests/21ae0065-e970-4a6b-9dbd-53e701d9845a), and its Astra/low run succeeded with a retained two-file patch. On October 4, the controller adapted the fix to the deployed backend decoder, completed independent review and race/database verification, and deployed `vps-worker-receipts-20261004` with the owner's authorization. Linear's existing completion receipt is now confirmed, with no duplicate activity. An older queued notice remains started/uncertain because its provider outcome cannot be proved, so the aggregate delivery indicator can still read uncertain. Automatic PR publication and review remain disabled. No fix was committed, merged, or published as a PR, and no additional model run occurred. Both run authorizations were for their individual runs; do not replay either run. Details and evidence are recorded in [the deployment record](../../development/vps-deployment.md). `circular.ruohao.dev` remains the public vitrine, with the working app private through SSH at `http://localhost:18080`.
**Tracking:** [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test), under [ISQ-258](https://linear.app/isqrd/issue/ISQ-258/give-circular-its-own-github-and-linear-identity).

The implementation has been exercised against owned providers and real disposable
Docker workloads. The user authorized local deployment on 2026-09-20; the API,
worker, console and dedicated webhook receiver were deployed with the reviewed version.
Migrations reached 0015 after a verified database backup, preserving all 13
projects, 3 repositories, 39 agents, 17 runs and 6 artifacts. Health and new API
readiness checks passed. At deployment, both existing user connections remained
connected; Circular identities and incoming events reported `needs_setup`, with
no request route enabled. No live model run was launched. The checkpoint below
records the current readiness state.

The user clarified that `circular.ruohao.dev` should serve the public vitrine,
with the working app kept private. The console/API/MCP are available only
through an SSH tunnel, with the owner password still required. Exact signed
webhook routes use the dedicated receiver. Hosting work does not enable the
acceptance route or launch a live model run.

## Completed acceptance — 2026-10-03

The owner approved Linear's additional `write` permission at 19:09:14 UTC.
Identity generation 6 retains the same Circular actor and all five granted
scopes. A mutation validation with execution skipped then passed, confirming
the permission rejection was resolved. Removed the temporary permission page.

Recovered only the two previously rejected replies for the selected session,
after verifying no matching provider activities existed. Their original UUIDs
and contents were preserved, and their database state was backed up at
`/opt/circular/backups/identity-acceptance-20261003/native-activity-before-write-recovery.json`.
The worker delivered the replies, revealing a receipt comparison defect:
Linear serializes Markdown link destinations as `[label](<url>)`, while the
submitted body used `[label](url)`. The actor, session, ID, and substantive
content were correct, but exact byte comparison kept the replies uncertain.

Added unit and PostgreSQL regression coverage, reproduced both failures on the
previously deployed source, and fixed comparison of equivalent HTTP(S) link
destination formatting. Other content and ownership checks remain intact.
Focused tests and the full Go suite passed: 972 test cases/subtests across 23
tested packages. The container-specific suites were disabled for this scoped
integration fix; the subprocess-only helper also retains its intentional skip.
An independent review found no Critical or Important issues.

Built release `vps-linear-receipts-20261003` from the prior deployed build's
source, adding only the receipt implementation and its two test files. The
source snapshot excluded unrelated shared-workspace changes. Deployed the API,
worker, and receiver after a private database/configuration backup at
`/opt/circular/backups/linear-receipts-20261003`. No migration, gateway, frontend,
or credential configuration changed. The native receipts then reconciled
without creating duplicate activities.

Final acceptance evidence:

- Linear session: `b8e6ac6d-1ece-48aa-8ca5-b2a1d90e6a74`, complete, under Circular
  actor `c6894fae-15d6-4616-a9c8-092f4fd0acc7`.
- Circular request: `e5eae4fb-77d0-4964-bfee-a6ab4938ef64`.
- Run: `ac5634a2-e7af-413e-93c5-d6d72c532b99`, started through the approval API
  at 19:26:20 UTC, succeeded at 19:27:05 UTC. Coding agent used
  `gpt-6-astra` with `low` reasoning. Its workspace is released.
- [Draft PR #2](https://github.com/ruohao1/circular-test/pull/2) is authored by
  `isqrd-circular[bot]`, actor `329667970`, in the private test repository.
  Head commit: `5ba1ab63aeaaf40f5dbf4dac4d8b8ce0085d39cd`.
- The only changed file is added `identity-smoke.txt`. Independent retrieval
  of the published blob verified exactly `Hello from Circular's app identity`
  followed by one newline, 35 bytes, SHA-256
  `831459c9944fb4df18afe91844cda564e3d3b99d58038b13e44c28fee7de61e0`.
- Six native app activities have matching delivered receipts: acknowledgment,
  saved-message notice, queued, running, succeeded, and PR published. The
  provider session contains the private request/run links and PR #2 link.
  The run generated zero ordinary Linear run comments.
- Exactly one run was created today; 18 runs now exist, all terminal. Route
  generation 9 is paused. Automatic review remains disabled. PR #2 is open,
  draft, and unmerged. Main and the earlier draft PR #1 are unchanged.
- The public vitrine returns HTTP 200, public control API and MCP return 404,
  and unauthenticated private access returns 401. The app remains private.

Evidence is retained in
`infra/production/.local/identity-acceptance-20261003/` and
`infra/production/.local/linear-activity-recovery-20261003/`. The disposable
regression-test database and network were removed. The earlier unrouted stub
`b1b062f6-69f4-4bef-8ea3-2252352b2d05` remains diagnostic evidence with no run;
do not approve or replay it.

Two follow-up defects were discovered outside this successful execution path:
reconnect could discard the separately approved `write` scope, and HTTP 400
GraphQL `FORBIDDEN` responses were classified as generic provider failures.
Both were subsequently fixed as recorded below. The current five-scope grant
does not require another reconnection.

## Permission recovery and manual approval — 2026-10-03

Release `vps-linear-permissions-20261003` preserves existing scopes in both
authorization entry points and checks the current shared identity's scopes
under lock before accepting replacement credentials. A callback that loses
permissions is rejected without overwriting the existing authorization.

Definite permission rejection now leaves the durable activity pending and
unstarted. Once access returns, a restarted consumer publishes the same UUID
exactly once. Responses with partial data, field execution paths, or mixed
unknown errors remain fenced for receipt recovery. Tests reproduce the original
failures and verify permission restoration, credential preservation, human
connection and route isolation, and duplicate protection.

The focused suite and independent review passed. The full suite on the release
build image passed 1,003 cases/subtests across 23 packages with `go test -p 1
-json ./... -count=1`; container-specific cases remained disabled. The first
parallel run encountered the pre-existing intermittent
`TestSupervisorHonorsAgentFailureBehaviorAndRetainsRawBackendDiagnostics/after_first_event`
failure (missing the first message delta). The unchanged baseline reproduced
that same failure in 18 of 30 repetitions; five isolated repetitions of the
updated source passed. Logs retain every result rather than treating the first
run as green.

The release audit also found that the preceding receipt release's source
snapshot omitted the earlier reconnect patches. The new release includes both
those patches and the receipt fix. The build image now retains the exact source
used for compilation, with a separately verified manifest and source archive.

Deployment backed up the database and configuration at
`/opt/circular/backups/linear-permissions-20261003` before replacing only API,
worker, and receiver services. No migration ran. All 18 existing runs, identity
credentials/generations, gateway, and database were preserved. The deployed
reconnect endpoint requests all five scopes without completing another OAuth
exchange. Health and public/private checks pass.

The owner-authorized next step enabled the existing route at generation 10,
still in approval mode and still targeting `ruohao1/circular-test`. Automatic
review is disabled. No real issue has been selected and no second live run was
launched. The earlier unrouted diagnostic stub remains untouched. Evidence is
in `infra/production/.local/linear-permissions-20261003/`.

## Live acceptance continuation — 2026-10-03

After the owner confirmed the webhook setting, verified that no earlier native
session or Circular request existed. Enabled the same approval-only route at
generation 4 and cleared then restored the Circular delegate through the official
Linear plugin. Linear created session `c2e72f09-36d7-4ff1-94e0-9b2436773c2c`
at 10:03:59 UTC, owned by the expected Circular app user. The human assignee
remained unchanged. No model run was started.

The receiver's last verified delivery remained an ordinary `Issue` event at
09:54:42 UTC; the request list remained empty. Receiver and gateway logs do
not record individual HTTP requests, so their empty output cannot establish
whether an earlier delivery was attempted or rejected.

Sent one follow-up through the existing session's comment thread at 10:14:09
UTC, using the same smoke task and single-run constraint. Comment
`6bd6ff42-5ca1-4271-bed2-15fad7e48320` produced prompt activity
`21f33e64-34c8-4d2c-9eaa-4d31c711dce5` in the same session. It was not queued
or skipped by Linear. No second session was created. A temporary, metadata-only
packet observation saw no incoming provider POST during that follow-up window.
No request bodies, signatures, or credentials were logged or saved. The
observation processes exited automatically; no service configuration changed.

A separate unsigned control probe confirmed the observation path and returned
HTTP 400. An external probe with a deliberately invalid signature returned
HTTP 401, and public DNS resolved only to the expected VPS IPv4 address. These
probes verify reachability and rejection of invalid requests; they do not prove
successful provider delivery or matching signing secrets.

The bot's read-only webhook configuration query was denied with `Invalid role:
admin required`. The existing owner connection could not authenticate for a
read-only diagnostic query. No scopes were expanded, credentials replaced, or
provider settings changed. The official Linear plugin cannot inspect OAuth app
webhook settings. Asked the owner for the saved URL and enabled state in
Linear's Circular application settings.

Paused the route at generation 5 while awaiting that information. Resume this
existing session and inspect deliveries before further triggers. The original
one-run approval persists. Evidence and safe helper scripts are under
`infra/production/.local/identity-acceptance-20261003/` and
`infra/production/.local/`; no new deployment was performed.

At 14:18:13 UTC, after the owner confirmed delivery was enabled, sent one more
follow-up in the same session. Comment `dd3de41a-d2f0-4f64-aa07-bab851be6b2b`
created prompt activity `b59c82e7-ac2a-41e9-ad37-f458758c40da`; Linear reports it
was neither queued nor skipped. A fresh 50-second metadata observation recorded
zero packets to the receiver. The request list remained empty and the receiver's
last accepted delivery remained 09:54:42 UTC. The route was left paused
throughout this retry.

The owner's screenshot confirms application
`2b0b4015-0727-49fb-a9e7-001871b32560`, client ID
`f808b5b0f05b04043932eabc4092d7f8`, delivery enabled, the exact expected HTTPS
URL, and Agent session events selected. The client ID matches the configured
Circular identity. No fully exhausted delivery failures are shown in Linear.
Google and Cloudflare DNS both resolve the hostname to the VPS, and an external
TLS connection verifies its certificate. UFW is inactive. These checks do not
establish why Linear has not delivered the session event; do not assume a bad
signing secret, missing checkbox, or required reconnection without more evidence.
The owner showed the delivery-status menu: it only offers **Disable webhooks**,
with no test-delivery action. Delivery was left enabled.

At 14:29:02 UTC, submitted one final plugin-generated diagnostic reply in the
same session (comment `d4a3e25c-2503-4824-8155-047d7899c360`). A 95-second
trace covered TCP 443 traffic from Linear's published outbound IPs and TCP 8001
to the receiver, including the initial attempt and first retry window. It
recorded zero packets. This narrows the observed failure to before the
receiver; it does not distinguish missing dispatch from a problem upstream of
the VPS. No server configuration or authorization was changed.

Asked the owner to send `continue` directly in the existing Linear session UI.
All prior triggers used the official plugin, so a direct UI prompt is the next
controlled comparison. This is a diagnostic hypothesis, not a claim that API
prompts are unsupported. The route remains paused at generation 5 and the
one-run approval remains unused.

At 18:27 UTC, inspected the owner's latest screenshot in
`~/Pictures/2026-10-03_20-26.png`. Linear shows **Agent didn't start. The request
may not have reached the agent.** The owner has dismissed earlier sessions and
created two additional sessions: `ddb30b3b-ef91-4c4e-998c-aecc12d44288` at
18:13:36 UTC and `d2fe4693-0fb6-47d0-9d4f-50806e944ebd` at 18:15:40 UTC.
Provider reads confirm both are stale, with the same expected Circular app
identity. The latest session's thread is `c129b082-285b-4015-8b91-e405b2144d0f`.
Both project and unrouted request lists remain empty, and the receiver still
reports 09:54:42 UTC as its last verified delivery. No model run has started.

Do not send the owner back to the original archived session or keep creating
new sessions. If deliveries arrive later, the authorization covers exactly one
run; inspect the latest current session and approve only that request. The
provider's **Webhook delivery failures** section is now the next source of
evidence. Its contents cannot be read through the official plugin or the
non-admin bot token. The owner-visible error is needed before changing any
working app configuration or guessing at a reconnection.

At 18:29 UTC the owner confirmed, with another screenshot, that Linear still
records no webhook delivery failures. The app-wide settings are correct, but
the workspace subscription remains unverified. Linear's public documentation
states that workspace webhooks are created when an organization authorizes an
OAuth app after its webhook settings are configured. This makes reauthorization
a controlled diagnostic, not an established fix.

The existing owner connection had an expired access token but a valid refresh
grant. Refreshed it under the integration row lock and committed the encrypted
replacement before use, preserving its `read` and `comments:create` scopes.
The read-only `webhooks` query then returned `Invalid scope: admin required`:
the user's role permits the read, but the existing grant lacks the required
scope. No additional scope was requested. Encrypted recovery material is at
`/opt/circular/backups/identity-acceptance-20261003/owner-refresh.sealed.json`.
The Circular bot grant was not changed by this diagnostic.

Prepared a temporary owner-operated page at
`http://localhost:18080/linear-reconnect.html`, served only from the private
console directory `/opt/circular/web/`. Its button invokes the existing bot
OAuth endpoint with purpose `agent`, preserving the same four capabilities.
The page validates the returned application, actor, and exact scope set before
navigating to Linear. OAuth state and browser cookies use the existing API
implementation; no login or consent is bypassed. Loading the page does not
start authorization. The page exists because the current console does not
offer reconnect while an app identity is healthy.

JavaScript syntax validation passed. The page returns HTTP 200 with private
loopback authentication, HTTP 401 without authentication, and HTTP 404 on the
public domain. An extra probe using owner credentials on the public URL was
rejected by automatic approval review; it was not executed. Completed the
safer public check without credentials. The request route remains paused at
generation 5. No service restart, code deployment, model run, or new native
session was performed. Remove the temporary page after the owner completes
reauthorization and the connection is verified.

The local SSH forward was not running, so restored the previously authorized
`127.0.0.1:18080` to VPS `127.0.0.1:8080` tunnel using the existing SSH key.
An unauthenticated local request now returns HTTP 401. The user can open the
prepared page directly; the app remains private and owner authentication is
still required.

The owner completed reauthorization at 18:41:32 UTC. The same enabled Circular
identity is available at generation 5 with all four existing scopes. A reply
through the latest existing session at 18:42:50 UTC produced a real inbound
`AgentSessionEvent` POST to `/webhooks/linear`, delivery
`34719af0-2349-4b15-8fe1-bc0d1bdd743b`. The metadata-only observation captured
the receiver's HTTP 401 response. No signature values or request bodies were
printed or saved. This confirms delivery now reaches the receiver and narrows
the current failure to signature verification; it does not distinguish an
incorrect secret from an invalid signature header.

The next owner action is to copy **Webhooks → Signing secret** from the exact
Linear OAuth app settings into **Setup → Integrations → Linear → Work from
Linear → Verify incoming events → Edit receiver settings → Signing secret**
in the private app, then save receiver settings. The public receiver origin
remains `https://circular.ruohao.dev`. The secret must stay in the provider and
private app settings, not chat. This uses the existing form and requires no
restart or code deployment.

Removed the temporary reconnect page from `/opt/circular/web/` after verifying
the completed connection. The SSH tunnel remains available for the private
settings form. At 18:50:38 UTC the route was still paused at generation 5,
both request lists were empty, and all 17 historical runs remained terminal
(8 succeeded, 9 failed). Evidence is saved in
`infra/production/.local/identity-acceptance-20261003/reconnected-signature-rejected.json`.

The latest session's creation predates successful workspace authorization and
has never been accepted by Circular. A later prompted event can create only a
request stub until its real creation event arrives. After signature verification
works, inspect the received events before deciding whether one fresh native
session is necessary. The authorization still permits exactly one model run.

At 18:57:12 UTC, after the owner saved the OAuth application's signing secret,
a reply to the existing session produced delivery
`f9848f75-c9ab-42ed-8e3d-7e68272c946a`. The receiver returned HTTP 200 and recorded
a verified delivery at 18:57:12.692402 UTC. The prompted event created request
stub `b1b062f6-69f4-4bef-8ea3-2252352b2d05`, waiting for the old session's missing
creation event. This resolves the observed signature failure.

Verified the selected Coding agent, repository, Astra/low, authorized draft-PR
delivery, enabled Linear updates, and disabled automatic review. Enabled the
same approval route at generation 6 and cleared then restored the delegate
once through the official Linear plugin. The human assignee remained ruohao.
Linear created session `b8e6ac6d-1ece-48aa-8ca5-b2a1d90e6a74` at 18:58:39 UTC,
thread `a787f47c-c4bc-4dae-bd44-735c4e8c92b0`. Its real creation event produced
request `e5eae4fb-77d0-4964-bfee-a6ab4938ef64` in `awaiting_approval`, with the
correct destination and no run. A follow-up in that thread explicitly supplied
the current one-file task and distinguished it from historical setup notes.
The complete request and latest message are saved in
`infra/production/.local/identity-acceptance-20261003/request.before-start.json`.

The app's native acknowledgment did not appear. A read-only receipt query
returned no activity for reserved ID `79adda4b-6d48-466f-9a1d-88d0f4fcf6d3`.
Validating the exact activity mutation with `@skip(if: true)` returned HTTP 400,
GraphQL code `FORBIDDEN`, and **Invalid scope: `write` required**. The skip
directive prevents creating an activity during this probe. The live input
schema accepts the existing supplied fields; this is a permission rejection,
not an unsupported mutation shape. No model run has been started.

Prepared `http://localhost:18080/linear-agent-access.html` in the private console.
It clearly explains the additional `write` scope and requires the owner to
click **Review and allow Linear write access**, then approve Linear's consent
screen. The page invokes the existing browser-bound PKCE flow, validates the
expected client, app actor, callback, challenge, and four original scopes, then
adds only `write` to the authorization URL. The existing callback requires the
original scopes and persists the complete provider-returned grant. Loading the
page has no side effects. No scope has been granted by preparing the page.

JavaScript syntax passed, and deployed bytes match the prepared page. Verified
private authenticated HTTP 200, private anonymous HTTP 401, public anonymous
HTTP 404, and local SSH tunnel HTTP 401 without authentication. The owner
credentials were used only on private loopback. The route is paused at
generation 7 while consent is pending. Remove this temporary page after the
owner completes the upgrade and the grant is verified.

Recovery detail: the current HTTP helper maps this HTTP 400 GraphQL rejection
to a generic provider error before reading its `FORBIDDEN` code. Consequently,
the four attempted native activities are marked started and later uncertain,
although no app activity is visible. Granting `write` alone will not resend
those reserved writes. After consent, verify receipts and recover the same
reserved activity IDs using the provider's confirmed permission rejection;
never blindly duplicate an uncertain activity or approve the old request stub.
Preserve the one-run constraint and the new session/request above. The source
error classification also needs a focused regression fix so future permission
rejections retain normal reconnect/retry behavior.

## Live acceptance attempt — 2026-10-02

The user's approval covers the prepared single Astra/low run, native Linear
updates, and a draft PR. Automatic PR review remains disabled. This approval
has not yet been consumed by a model run and persists when setup resumes.

Verified the selected repository and agent, enabled the existing approval-only
route at generation 2, and submitted the smoke instruction once through the
official Linear plugin. Comment `58da12ce-09ca-4b07-81d0-f12bdd429201` was saved
at 20:55:16 UTC, but its `agentSession` is null. Delegation through the same
plugin at 20:57:24 UTC set the delegate to Circular and retained the human
owner, ruohao. A direct provider read at 20:58:43 UTC confirmed the expected
delegate and zero agent sessions on ISQ-262.

At 21:03:19 UTC, corrected the existing comment to use Linear's documented
profile-URL mention format, replacing the plugin's literal `@circular` text.
No second comment was created. A subsequent read still found zero native
sessions and zero Circular requests.

The receiver accepted signed ordinary `Issue` events and ignored them as
expected. No `AgentSessionEvent` arrived, no Circular request appeared, and
all 17 historical runs remain terminal. The first check is the OAuth app's
**Agent session events** subscription; the official plugin cannot edit that
app setting. The owner was asked to check it in Linear. Do not infer that the
setting is disabled solely from the missing session.

Paused the route at generation 3 while waiting. The worker is configured for
ChatGPT authentication with Codex enabled, and both runner images exist. The
private repository's `main` tree still contains only README.md; hello.txt is
on the preserved, unmerged draft PR #1. No new PR, review, or run was created.

Evidence is under `infra/production/.local/identity-acceptance-20261002/` and
`/opt/circular/backups/identity-acceptance-20261002/`. When the setting is
confirmed, inspect existing provider sessions and requests before another
trigger, so the resumed test still starts only one run.

## Readiness checkpoint — 2026-10-02

- GitHub's receiver reports `receiving` after a real signed ping returned HTTP
  200 at `2026-10-02T20:03:08.561149Z`. Its existing app identity and draft-PR
  delivery remain enabled and authorized.
- Linear's receiver also reports `receiving`, most recently at
  `2026-10-02T20:40:56.511857Z`. Observed ordinary `Issue` events were ignored
  as expected. Agent-session events and the live workflow are not yet verified.
- Linear's bot refresh grant had been revoked. The provider returned HTTP 400 with
  `invalid_request` and `Refresh token revoked`. Circular now classifies that
  exact token-endpoint response as requiring reconnection rather than a
  transient failure. After the owner updated the allowed callback and approved
  consent, reconnection restored the same bot identity and all four prior
  scopes. Linear teams and projects requests return HTTP 200, and run updates
  are enabled and authorized.
- The recovery fix also requests all four previously granted agent scopes
  during reconnect and validates them before replacing credentials. Regression
  tests reproduced the failures before the fix; the full Go suite with an
  isolated PostgreSQL database and code review passed.
- Release `vps-linear-reconnect-20261002` is deployed to the API, worker and
  receiver. The gateway and database were preserved without a schema change.
  Eight live health/privacy checks passed, and all services remain running.
- All 17 runs remain terminal, with zero external requests and zero enabled
  routes. The existing acceptance route remains paused in approval mode at
  generation 1. No model run, issue comment, PR, or review was created.
- Successful reconnection confirmed the private OAuth callback
  `http://localhost:18080/api/v1/integrations/linear/callback`. The remaining
  work is the deliberately authorized agent-session acceptance below. Backup
  and deployment evidence are in
  [the VPS deployment record](../../development/vps-deployment.md).

The checkpoints below record historical states.

## Readiness checkpoint — 2026-10-01

- Both app identities are enabled: GitHub `isqrd-circular[bot]` in `ruohao1`
  and Linear `Circular` in `iSQRD`. The earlier GitHub setup blocker is resolved.
- The production build and all 39 database tables, including 17 historical
  runs, are verified on `141.95.112.190`. The public vitrine and private app
  passed 51 separation checks, including attempts with owner credentials.
- Porkbun resolves `circular.ruohao.dev` to the VPS. A trusted Let's Encrypt
  certificate was issued and HTTP redirects to HTTPS. The public site exposes
  no console assets, control API, or MCP; those work through the SSH tunnel.
- The VPS worker is running without restarts. The local console, API, receiver,
  and worker are stopped, with the original database and state retained.
  No run or provider message was created during deployment.
- Both providers still need incoming webhook configuration and a real signed
  delivery. Details and rollback evidence are in
  [the VPS deployment record](../../development/vps-deployment.md).

The September checkpoint below is historical.

## Readiness checkpoint — 2026-09-26

The existing Postgres, API, receiver, worker and web containers had all stopped
at 17:20:05 UTC with exit code 255, no OOM flag, and restart policy `no`.
The initiating host/Docker event was not established. Restarted the existing
containers, waiting for database health and verifying no queued or active runs
before restoring the worker. No rebuild or migration was needed.

Verified after restoration:

- All five services are running; Postgres is healthy. Console port 5173,
  `/api/v1/health` on port 8000, and `/health` on port 8001 return HTTP 200.
- The dedicated receiver returns 404 for `/`, `/api/v1/health` and `/mcp`, and
  405 for GET requests to its two POST-only webhook endpoints.
- Linear identity is enabled as **Circular** in **iSQRD**, actor
  `c6894fae-15d6-4616-a9c8-092f4fd0acc7`. Its grant now includes `read`,
  `comments:create`, `app:mentionable` and `app:assignable`. The earlier missing
  mention/delegation authorization is resolved.
- GitHub identity still reports `needs_setup`. The saved account connection
  cannot authorize installation access: the API returns HTTP 409 with
  `connection authorization is unavailable; reconnect in Setup`. Reconnect the
  existing GitHub account and complete the existing app's signing-key setup.
- Both providers' webhook settings report `needs_setup`, with no public origin,
  signing secret or verified delivery. Tailscale is connected under
  `ubuntu-elitemini-series.taila4d02b.ts.net`, with no Funnel configured. The public
  endpoint choice remains pending.
- The existing acceptance destination remains paused (`enabled=false`) in
  approval mode, targeting the recorded repository and enabled Coding agent
  with `gpt-6-astra` / `low`. There are zero external requests and 17 terminal
  runs (8 succeeded, 9 failed); no run was launched during this check.
- Existing draft-PR delivery is enabled but currently unauthorized. Automatic
  review remains disabled; Linear run updates are enabled and authorized.

Provider setup still needs a signed-in browser; no browser was connected to the
agent session during this checkpoint. Ingress, provider credentials, publishing
settings and request-route enablement were not changed.

## Selected destination

Read-only Circular queries confirmed these existing records on 2026-09-20:

| Setting           | Selection                                                                                            |
| ----------------- | ---------------------------------------------------------------------------------------------------- |
| Circular project  | `circular-test` · `7d2dc377-c59e-4eb1-8b78-6a475ff4b64b`                                             |
| Repository        | Private `ruohao1/circular-test`, branch `main` · `0c6d70cc-b534-4626-876d-7d1d1bec31aa`              |
| GitHub mapping    | Repository `1376894315`, installation `161991156`                                                    |
| Coding agent      | `Coding agent` · `37e59620-5065-4ec6-83c1-ca03ac4300ef`                                              |
| Model / reasoning | `gpt-6-astra` / `low`                                                                                |
| Route scope       | Linear project `Circular` · `d183ccba-6211-4b9b-afba-6ba159c220d9`                                   |
| Acceptance mode   | **Ask in Circular first**                                                                            |
| Test issue        | [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test)    |
| Optional reviewer | Existing `PR reviewer` · `4dffc1c4-8a1b-415a-9bc5-49daecd80ab4`; preview Astra / low before enabling |

No agent or repository needs to be created. Keep the completed ISQ-253 smoke test
and existing PR #1 intact. This approval-mode route is a deliberate acceptance
choice; the product's new-route default remains automatic after explicit enablement.

## Setup and actor preview

1. Open the SSH tunnel described in the deployment record, then visit
   `http://localhost:18080` with the saved owner login. The public domain serves
   the vitrine and exact signed webhook routes, not the working console.
2. In **Setup → Integrations**, select `circular-test` and verify the existing
   GitHub/Linear **Circular identity** connections. Review each provider-returned
   account, **Acting as** name and stable actor ID; both are already configured.
3. Verify the installed Linear grant's
   `read`, `comments:create`, `app:mentionable` and `app:assignable` capabilities.
   Reconnection has restored all four existing permissions.
   Record an actual permission rejection if the provider requires an additional
   scope; any upgrade must be explicit.
4. Both receivers already report **Receiving events** after real signed
   deliveries. Before the live test, confirm **Agent session events** are
   enabled in Linear. Ordinary issue deliveries do not verify that workflow.
5. Select the destination above, choose **Ask in Circular first**, review the model
   and explicitly enable the route. Independently select draft-PR publication and
   automatic review if those outputs are included in this acceptance.

## One issue, one run

Delegate ISQ-262 to the installed app, or mention that app once on the issue with:

> Add a root-level `identity-smoke.txt` containing exactly `Hello from Circular's app identity` followed by one newline. Preserve README, hello.txt and all other files. Verify the exact file bytes using existing tools. Do not install dependencies, commit, push, or contact external services from the workload. Circular captures the diff and handles the selected draft PR and review publication. Finish with the change and the check actually performed.

Expect an app-authored acknowledgment and one request in Circular. Approval mode
must leave its run empty. Open the request, review its text, repository, Coding
agent and Astra/low settings, then start once. Follow the same native Linear
session for progress, final result and Circular links; there should be no duplicate
ordinary issue comments.

If selected, inspect the draft GitHub PR and review under the GitHub App actor.
The internal agent's name should appear in the message, and the same Linear
session should link the PR and report review progress/results. The human remains
the issue owner and decides whether to merge.

## Evidence and finish

Record in ISQ-262:

- Actual provider actor IDs and displayed names, with permission outcome.
- Linear issue/session and Circular request/run URLs.
- Selected model/reasoning and the verified file-content result.
- Draft PR/review URLs when those publications were selected.
- Any missing capability, delivery repair, or unexpected duplication.

Pause the acceptance route afterward. Preserve the draft PR for human review.
Do not merge, replay the session, or start additional runs as cleanup. Cancellation,
restarts, duplicate deliveries, long outages and post-completion publishing fences
already have fixture coverage; extra live destructive/revocation checks are not
part of this one-run acceptance.
