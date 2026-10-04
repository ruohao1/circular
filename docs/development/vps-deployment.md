# Circular deployment — 2026-10-01

Status: **public vitrine at https://circular.ruohao.dev; working app private,
accessible through SSH only**.

The user clarified that the domain should present Circular publicly while the
working app stays private. The earlier public-console deployment was removed.
The VPS remains the active application host, preserving all existing data.

## Public and private surfaces

The public domain serves the static product presentation from `apps/vitrine`.
It needs no login and contains no console bundle, API client, or application
records. The public root, CSS, font, icon, robots file, and sitemap are served
from an explicit seven-file build allowlist.

Public requests to the control API, MCP, setup, requests, console assets, and
credential filenames return 404, including requests with valid owner
credentials. The two exact webhook receiver routes remain publicly reachable
for signed provider deliveries; they do not expose the control API. Unsigned
requests are rejected. Both providers have verified signed deliveries and
authorized bot identities. Live agent-session acceptance passed on 2026-10-03.

The console, control API, and MCP use a separate listener published only at
`127.0.0.1:8080` on the VPS. The owner password is still required there. Open an
SSH tunnel from the computer running the browser:

```sh
ssh -i ~/.ssh/imoki-lab -N \
  -L 127.0.0.1:18080:127.0.0.1:8080 ubuntu@141.95.112.190
```

Then open **http://localhost:18080**. The private login is saved in
`infra/production/.local/access.txt`; its password is unchanged. Keep the tunnel
running while using the app, and close it with Ctrl+C. The `.local/` directory
is ignored by git and excluded from Docker build contexts.

## Host and configuration

Target: `ubuntu@141.95.112.190`, SSH key `~/.ssh/imoki-lab`, deployment directory
`/opt/circular`. Ubuntu 26.04 runs Docker 29.1.3 and Compose 2.40.3 on 2 CPUs and
4 GB RAM. Server images now use release tag
`vps-worker-receipts-20261004`. Caddy is pinned by digest and retains its
certificate state in a Docker volume.

The production configuration is in `infra/production/`. The public vitrine is
built with `node apps/vitrine/build.mjs`. The private console is built with an
empty `VITE_API_URL` for same-origin requests. `CIRCULAR_APP_ORIGIN` is set to
`http://localhost:18080` for application links, callbacks, CORS, and MCP.

The DNS record remains Porkbun A host `circular`, answer `141.95.112.190`, TTL
600. The root domain's existing records were not changed. Public ports 80 and
443 serve the vitrine; private port 8080 remains bound to loopback. Backend and
database services have no published host ports.

## First real repository request — 2026-10-03

The owner approved preparing real work in `ruohao1/circular` while retaining
manual approval. The repository is already imported into Circular project
**Test 1** (`d2d86e01-9d58-45ac-b493-dcb38a5dc33c`). Both existing app identities
are now bound to that project, and its new Coding agent uses `gpt-6-astra`
with `low` reasoning. Existing human connections and other agents are preserved.

The Linear-project route was moved from the acceptance repository to
`ruohao1/circular`. Route `aa9fa54e-026f-4810-b056-55a650fb8d5b` is enabled at
generation 12 in **approval** mode. Automatic PR publication and automatic PR
review are disabled for this project.

[ISQ-365](https://linear.app/isqrd/issue/ISQ-365/investigate-lost-first-worker-progress-event-after-backend-failure)
investigates the intermittent missing first worker progress event. It includes
the prior observations, reproduction command, and focused acceptance criteria.
The issue remains owned by ruohao and is delegated to Circular. Native session
`d875d089-749e-47e6-b1b0-f4e658ab08ee` produced
[request 21ae0065](http://localhost:18080/requests/21ae0065-e970-4a6b-9dbd-53e701d9845a),
which the owner subsequently approved. Run
`64eba433-8b29-434d-98d0-e1051e75c2cd` succeeded at `2026-10-03T21:44:01Z`
using the real repository, Coding agent, and Astra/low settings. Its two-file
patch and workspace archive were retained, and the workspace was released.
The agent reported 30 passing regression repetitions and a passing Go suite;
Docker and race checks were unavailable inside that workload. The controller
subsequently adapted, verified, and deployed the fix as recorded below. Nothing
was merged or published as a PR. Linear's completion receipt is now confirmed;
an older queued notice remains uncertain.

Independent review on October 4 found no Standards or Spec issues in that
artifact. All three focused regression tests passed 30 repetitions against
disposable PostgreSQL, without skipped database cases. The artifact applies to
its recorded GitHub main commit, but not the deployed worker: backend decoding
has since moved into `internal/backends`. The deployed decoder files matched
the newer local checkout. The controller adapted the fix to that interface
without overwriting the newer decoder with the old file. Original artifact
review evidence: `infra/production/.local/isq-365-review-20261004/`.

The original runner lacked Go and PostgreSQL. The worker now selects
`circular-codex-runner:go-postgres-20261003`, image
`sha256:ef3a73d645814c4c19e1a01615e3b3906e4f78f7dab90ec4242f929add510e15`.
It preserves the trusted workload binary, Codex CLI, and Node package tools,
and adds Go 1.27.1, PostgreSQL 17, cached main-branch modules, and
`circular-go-test`. That helper creates a disposable database on a private Unix
socket, supplies `TEST_DATABASE_URL`, and removes its workspace scratch files
after normal success or failure. It never uses the control-plane database.

Independent review found and resolved missing executable paths and omitted
Node command links. Runtime validation passed with the workload's sanitized
environment, UID 65532, a read-only root, no network, one CPU, 2 GB memory, and
128 MB of temporary storage. The stable `before_events` database test passed
without skipping; an intentional missing-package test confirmed failure exit
status and cleanup. This verifies the runner, not the intermittent defect.
Validation used the GitHub main source at
`4d6bd4c43c9e080096b170851ff7c6aec9d9043e`.

Only the idle worker was recreated; the API, receiver, gateway, and database
containers were preserved. No migration or model run occurred during this
preparation; the owner-approved run followed as recorded above. Private health
returns 200, the public vitrine returns 200, and public API/MCP/request paths
return 404. Configuration and route backups are under
`/opt/circular/backups/real-circular-task-20261003/`. The old runner image remains
available for rollback by restoring its `CIRCULAR_CODEX_IMAGE` setting and
recreating only the worker. Build files, review notes, and verification evidence
are retained in `infra/production/.local/real-circular-task-20261003/`.

## Worker progress and Linear completion receipts — 2026-10-04

The owner authorized integration and deployment of the reviewed ISQ-365 fix.
Release `vps-worker-receipts-20261004` uses the exact archived source of
`vps-linear-permissions-20261003` plus seven reviewed source/test files. It
does not include the shared checkout's unrelated task-planning or migration
changes. The scoped changes were also copied into the shared checkout after
checking that all seven file baselines still matched.

A validated fake-backend diagnostic on stderr now allows pending stdout to be
decoded before the run fails with the original diagnostic. Malformed protocol
output still stops interpretation. Transport, cancellation, and persistence
errors still abort ingestion, and Codex output handling is preserved. The
deterministic regression reproduced the stderr-first and fragmented-stdout
failure before the change. The current worker/database regression passed 30
repetitions with the race detector.

`go test -race -p 1 ./... -count=1 -timeout=300s` passed against disposable
PostgreSQL 17: 23 packages and 1,041 tests/subtests. Database cases were not
skipped. Docker-only checks and an intentional subprocess helper remained
skipped. The first full-suite attempt was interrupted when its temporary
database exited; its exact cause was not established. The replacement
disk-backed fixture remained running throughout the successful rerun. Both
attempts, fixture evidence, and red/green regression results are retained.

Linear had received the completion activity
`d57647a6-6ab5-443a-b7d1-3b7c24efc5a9`, but changed five top-level Markdown
list markers from `-` to `*`. Receipt comparison now tolerates that observed
rewrite while preserving identity, session, actor, type, and other content
checks. Code fences, indented code, HTML, empty-item/heading collisions, and
thematic-break collisions remain strict. Independent review found two marker
and fence edge cases; regressions reproduced them before correction, and the
follow-up review found no remaining blockers.

After deployment, the existing completion activity reconciled to **delivered**.
Linear still has the same 12 native activities; all 13 local activity IDs are
unchanged. The older queued activity
`bc9b378d-4535-410b-8648-1a948ddd1c61` has no matching receipt, including in
archived/session-history queries. Its original provider outcome cannot be
proved. It remains **started/uncertain** and cannot be sent a second time;
it was not reset, cancelled, or falsely marked delivered. Consequently, the
request's aggregate delivery indicator still reads uncertain even though its
completion is confirmed.

Deployment backed up configuration and PostgreSQL under
`/opt/circular/backups/isq-365-port-20261004/`, then replaced only the API,
worker, and webhook services. All 19 run records, identities and credentials,
approval route generation 12, and the existing Go/PostgreSQL runner were
preserved. Gateway and database containers were unchanged. No migration,
new model run, commit, push, merge, or PR publication occurred. Automatic PR
publication and review remain disabled.

Private health returns 200, the public vitrine returns 200, public API/MCP/
request paths return 404, and correctly framed unsigned Linear webhooks
return 401. The private listener remains bound to `127.0.0.1:8080` and rejects
unauthenticated access. The prior release images remain available for rollback
by restoring `CIRCULAR_RELEASE=vps-linear-permissions-20261003` and recreating
only `api`, `worker`, and `webhooks`; no database rollback is required.

Evidence, exact release source, image digests, reviews, and guarded deployment
scripts: `infra/production/.local/isq-365-port-20261004/`. The three labeled
disposable test resources were removed after their evidence was retained.

## Linear permission recovery — 2026-10-03

The current release preserves previously approved Linear scopes on reconnect
and rejects callbacks that would reduce the shared identity's permissions.
Definite HTTP 400 GraphQL permission rejections now release the activity's
reservation for retry with the same UUID. Partial results, execution paths,
and mixed unknown errors retain their receipt recovery fence.

Regression tests failed before the changes and passed afterward. Independent
review found no Critical or Important issues. Verification using the exact
release build image passed 1,003 cases/subtests across 23 Go packages with
packages run sequentially. Container-specific suites were disabled. The first
parallel run exposed the existing intermittent worker test
`TestSupervisorHonorsAgentFailureBehaviorAndRetainsRawBackendDiagnostics/after_first_event`;
it also failed in 18 of 30 unchanged-baseline repetitions. That separate
failure is retained in the verification evidence and was not changed here.

The release audit found that the preceding receipt release omitted the October
2 reconnect patches. The new release restores those patches and retains the
receipt fix. Its build image contains the complete source used to build the
binaries, and a source manifest and archive are retained with the evidence.

Deployed only the API, worker, and receiver after a private database/configuration
backup at `/opt/circular/backups/linear-permissions-20261003`. The backup contains
12,076,350 bytes of PostgreSQL data. No migration ran. Identity credentials and
generations, the gateway, database, and all 18 historical runs were preserved.

Production verification confirms that reconnect requests all five existing
scopes, including `write`, without exchanging a token or changing the grant.
Health and public/private separation checks passed. At the end of this release,
the existing `circular-test` route was enabled at generation 10 in **approval**
mode. Automatic PR review was disabled and no new run was started. The subsequent
real-repository setup above moved the route and prepared ISQ-365 for approval.

Evidence: `infra/production/.local/linear-permissions-20261003/`.

## Native Linear receipt recovery — 2026-10-03

Deployed a focused receipt comparison fix after Linear's real API normalized
Markdown links and Circular failed to recognize its own app-authored replies.
Equivalent HTTP(S) link destination formatting now compares equally; the
activity UUID, actor, session, type, and remaining content checks are preserved.

Both regressions failed on the preceding deployed source, then passed after the
fix. The full Go suite passed with disposable PostgreSQL (972 cases/subtests,
23 tested packages); container-specific cases were disabled for this integration
change. Independent code review found no Critical or Important findings.

The release was built from the prior deployed source plus only the focused
receipt fix and tests, preserving unrelated work in the shared checkout. API,
worker, and receiver were replaced without migrations or gateway/frontend
changes. Private database/configuration backup:
`/opt/circular/backups/linear-receipts-20261003`. The preceding release images
remain available for recovery.

The controlled live run succeeded and published app-authored draft PR #2, with
native Linear progress/results and links confirmed. The test route is paused,
all 18 runs are terminal, and automatic review is disabled. Public-vitrine and
private-app separation checks passed. Full evidence and outstanding permission
recovery follow-ups are in
[the acceptance record](../superpowers/plans/2026-09-19-circular-identity-live-acceptance.md).

## Verification evidence — 2026-10-01

- Frontend TypeScript check and private-console production build passed.
- Public build validation confirmed complete local assets and working anchors.
- All 51 separation checks passed in private staging and again against the
  public HTTPS deployment, including credentialed attempts to reach the app.
- The console and API load through the SSH tunnel. Public requests cannot
  obtain the console assets. Foreign and null-origin writes are rejected on
  the private listener.
- HTTP redirects to HTTPS with status 308. TLS 1.3 validates a Let's Encrypt
  certificate for `circular.ruohao.dev`, initially expiring 2026-12-30.
- External connections to ports 8080, 8081, 8443, 8000, 8001, 5432, and 5173 fail.
- All 39 database table counts match the final migration snapshot, including
  all 17 historical runs. Schema remains at migration `0015`.
- The worker and trusted services are running. No model run, provider message,
  new route enablement, or provider permission change was performed.

Repeat the separation checks with the SSH tunnel running:

```sh
python3 infra/production/verify.py https://circular.ruohao.dev \
  --credentials infra/production/.local/owner-auth.json
```

The original local API, receiver, worker, and console remain stopped. The local
PostgreSQL database and state remain intact as the original migration copy.

## Linear authorization recovery — 2026-10-02

Linear returned HTTP 400 with `invalid_request` and `Refresh token revoked`
when Circular attempted to renew the existing bot grant. Circular previously
treated that response as a temporary provider failure, leaving the bot marked
enabled and hiding its reconnect action.

The deployed fix recognizes this exact token-endpoint response and marks the
same bot identity as requiring reconnection. Reconnecting also preserves the
existing mention/delegation scopes and rejects incomplete consent before
replacing saved credentials. The human connection and paused routes are
preserved.

Regression tests reproduced both failures before the fix. The full Go suite
passed with isolated PostgreSQL integration tests, and the change passed code
review. The API, worker, and receiver were rebuilt and deployed; the gateway
and database were not restarted, and no migration was needed. Before
reauthorization, the private API returned the expected HTTP 409 and
`reconnect_required` identity state.

The owner then registered the private OAuth callback in Linear and completed
reconnection. Verification at `2026-10-02T20:47:23Z` confirmed the same bot and
identity, all four prior scopes, and authorized run updates. Linear teams and
projects API requests now return HTTP 200. The callback and consent blockers
are resolved.

Eight live health/privacy checks passed: the public vitrine returns 200;
public API, setup, and MCP requests return 404; the public API also rejects
valid owner credentials; the private console requires authentication; and
the authenticated private console and health endpoint return 200. All 17
historical runs remain terminal, with zero external requests and zero enabled
routes. The acceptance route remains in approval mode at generation 1.

## Backups and rollback

The initial rehearsal snapshot is retained under
`/opt/circular/backups/migration-20261001/`. The final consistent database/state
snapshot, checksums, table counts, and environment copies are under
`/opt/circular/backups/cutover-20261001/`. The vitrine correction's files and
verification evidence are under `/opt/circular/backups/showcase-20261001/`.
The authorization recovery backup is under
`/opt/circular/backups/linear-reconnect-20261002/`, including the preceding
database, environment and gateway configuration, before/after counts, and
health/privacy evidence. The previous `vps-20261001` images remain available.

Earlier backups include public-console ingress. Restore data or release images
selectively while retaining the current private-app gateway. Keep the original
integration encryption key when restoring encrypted provider connections.
Back up new VPS data before any rollback. Never delete PostgreSQL volumes.

The old local migration helper was built at migration `0002`; it cannot be
rerun against schema `0015`. Its previous attempt failed and rolled back.
Rebuild that helper from current source before using it again. If the original
local services are deliberately restored, start the existing service containers
directly after stopping VPS writers and preserving any newer data.

## Remaining provider setup

Both incoming receivers now have signing secrets and public origins saved.
GitHub verified a real signed ping at `2026-10-02T20:03:08.561149Z`; Linear's
latest verified signed delivery is `2026-10-02T20:40:56.511857Z`. Both report
`receiving`. Observed ordinary Linear `Issue` events were ignored as expected;
they do not prove agent-session acceptance.

Both bots are enabled and authorized. Linear reconnection preserved the same
identity and its `read`, `comments:create`, `app:mentionable`, and
`app:assignable` permissions. No acceptance route was enabled and no run was
started during verification.

Use the private app's displayed callback URLs for provider settings. The
current Linear OAuth callback is
`http://localhost:18080/api/v1/integrations/linear/callback`; successful
reconnection confirmed it works with the SSH tunnel. Signed event URLs are
`https://circular.ruohao.dev/webhooks/github` and
`https://circular.ruohao.dev/webhooks/linear`.

The live app-identity acceptance subsequently passed; see the current release
and acceptance sections above.
