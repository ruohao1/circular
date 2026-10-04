---
title: "GitHub and Linear connections"
description: "Connect GitHub repositories and import Linear issues from the Circular console."
---

Circular connects providers per Project in **Setup → Integrations**. GitHub
provides Repository access and draft PR delivery, and Linear issues become Circular Tasks.
The console guides first-time app registration in your own browser. Each app is
registered once for the Circular server; account connections belong to individual
Projects. Codex retains its separate subscription login.

## Connect from the console

Open **Setup → Integrations** and select a Circular Project.

- **GitHub:** click **Connect GitHub**. Optionally enter an organization name;
  leave it blank for a personal account. Click **Continue on GitHub**, create the
  preconfigured private app, and install it on the repositories you select.
  Circular saves the app settings automatically. When GitHub returns you to
  Circular, click **Connect GitHub** to authorize your account.
- **Linear:** click **Connect Linear**, then **Create Linear app**. Linear opens
  a prefilled app form in another tab. Create the app in your workspace, copy its
  public **Client ID** into Circular, and click **Save and connect Linear**.
  Approve the requested read and comment access and return to Circular. No client secret or
  personal API key is needed for this flow.

In Linear's private-app form, leave **Public**, **Client credentials**, and
**Webhooks** off. **Developer URL** is optional for this setup and Circular leaves
it blank: Linear's homepage validator rejects `http://localhost:5173`. If you
opened an older setup link, clear that field. Keep the prefilled **Redirect URI**;
it is the browser callback to Circular and has a separate purpose.

After that one-time setup, **Connect** and **Reconnect** open the provider's
authorization screen directly. No server restart is needed when saving app settings
in the console. Linear's **App settings** action lets you correct its Client ID
while no Linear connections are enabled in any Circular Project. Changing it
invalidates pending Linear sign-ins. GitHub registration cannot overwrite an
existing app, and settings supplied through the server environment take precedence.

If you rename the GitHub app, open **GitHub → App settings**, paste the new
`https://github.com/apps/…` URL, and save. Circular verifies that the URL belongs
to the same app before updating installation links; account connections and
pending sign-ins remain valid. **Manage GitHub access** opens the selected
installation's settings using its numeric ID, so that link survives an app rename.
When no installation is available yet, it opens the app's installation page instead.

GitHub's [app manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest)
returns app credentials directly to Circular. Linear's
[app manifest form](https://linear.app/developers/oauth-app-manifests) prefills the
registration fields; its public Client ID is copied back to Circular once.

Circular omits the GitHub manifest's optional `hook_attributes` section because
it does not receive webhooks. GitHub rejects localhost webhook URLs even when
the hook is inactive; the browser registration and OAuth callbacks still use
the configured local API URL. If retrying a rejected manifest, return to
**Setup → Integrations** and start **Connect GitHub** again to submit fresh settings.

## Server configuration

Use the existing ignored `.env` file for local Compose configuration. Add these
values; never put credentials in Repository URLs, Agent configuration, or task text:

```dotenv
CIRCULAR_PUBLIC_API_URL=http://localhost:8000
CIRCULAR_WEB_URL=http://localhost:5173
CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY=<one persistent base64-encoded 32-byte key>
```

Generate the encryption key once with `openssl rand -base64 32` and save it in
`CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY`. Preserve it alongside your database backup.
The API and worker must use the same key. Console-registered app settings are
encrypted in the shared database and become available to both processes immediately.
Leave provider client settings empty to use console registration. If the encryption
key is absent, connection actions are disabled; the rest of Circular continues to work.

The public API URL and web URL are browser-facing origins with no path. For a
local installation, consistently open `localhost`, including during authorization.
For a remote deployment use HTTPS and keep the web and API on the same site so
the browser can store the callback cookie. If using native Vite, also set
`VITE_API_URL` to the public API origin. CORS defaults to `CIRCULAR_WEB_URL`;
an explicit `CORS_ORIGINS` override must include that exact origin, not `*`.
Provider connections do not add login protection to Circular itself; retain the
existing trusted local deployment boundary.

## Optional manual GitHub App configuration

For an app already managed outside Circular, set `CIRCULAR_GITHUB_CLIENT_ID`,
`CIRCULAR_GITHUB_CLIENT_SECRET`, and `CIRCULAR_GITHUB_APP_SLUG` in the API and
worker environment instead of using console registration.

1. In GitHub **Settings → Developer settings → GitHub Apps**, create an app for
   your account or organization. Set its homepage to your Circular web URL.
2. Set the user authorization callback URL to
   `http://localhost:8000/api/v1/integrations/github/callback` (replace the origin
   for another deployment). Leave **Request user authorization during installation**
   off: Circular starts authorization with its own state and PKCE challenge.
3. Set Repository **Contents: Read and write**, **Pull requests: Read and write**,
   and **Administration: Read and write**; **Metadata: Read-only** is included.
   Contents and Pull requests permit draft PR delivery. Administration permits repository creation.
   Keep user access token expiration enabled. This slice does not need a webhook
   receiver, so disable the active webhook setting.
4. Generate a client secret. Store the app's **Client ID**, client secret, and
   app slug in the corresponding server settings. The numeric App ID is different.
5. Install the app on the GitHub account or organization and select the desired
   repositories. After restarting Circular with the configuration, select your
   Circular Project and click **Connect GitHub**.

Circular uses the GitHub App user authorization flow. Its access is limited to the
intersection of the app installation and the signing-in user's permissions.
The account picker lists accessible installations and the Repository picker lists
their authorized source code. [GitHub user authorization documentation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app).

Contents permission supports HTTP Git access with an app user token. Circular uses
the token in trusted services for worker clone/fetch and API-side publishing;
coding agents never receive it. [GitHub Git permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app).

If no repositories appear, check the app installation, selected repositories, and
your user access, then use **Refresh GitHub repositories**. A repository needs an
initial commit and default branch before a Run can use it. After changing app
access, refresh the list; after authorization expires or is revoked, reconnect.

## GitHub repository creation

**Create repository** uses the existing GitHub App user token. Personal creation
uses `POST /user/repos` only when the installation owner is the authenticated
user; organizations use `POST /orgs/{org}/repos`. The installation must report
Administration write permission. Existing apps require the owner to update
permissions and the installation owner to approve them; reconnecting by itself
does not grant new app permissions. Read-only Contents access still supports
clone/fetch; draft PR delivery requires the separate write permissions below.

`POST /api/v1/projects/{project_id}/integrations/github/repositories` accepts
`installation_id`, `name`, optional `description`, `visibility` (private by default),
and a stable UUID `request_key`. Circular always sends `auto_init=true`, validates
GitHub's response, verifies installation access, and retains its provider identity
when registering the repository for private clone/fetch.

Revision `0010` journals each creation intent before the external request. A
confirmed receipt can finish attachment after retries or restarts. A request with
an uncertain remote result never sends another create request or automatically
adopts a repository merely because its owner/name matches. Reusing a key with
changed settings is rejected. Definitive permission rejections may be retried
with the same key after approval; ordinary name/validation errors can be corrected.
The UI retains unfinished requests in the browser tab's session storage and
shows explicit recovery instructions.

GitHub normally grants the app access to repositories it creates, including
selected-repository installations. If access or README initialization is not yet
available, Circular reports `needs_access` with the created repository link. It
never reports full success until the repository is attached to the project.
[GitHub repository API](https://docs.github.com/en/rest/repos/repos) and
[GitHub App installation behavior](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app).

## Deliver runs as draft pull requests

**Automatically open draft pull requests** opts a Project into publishing future
successful runs. It is off by default. Existing successful runs have a separate
**Create draft pull request** action. Both paths queue the same durable delivery,
identified by the Run ID; retries do not start another coding run.
Disabling automatic publishing stops future work; a delivery already in progress
may finish. Existing branches and PRs remain available for review.

The GitHub App installation must approve Repository **Contents** and **Pull
requests** as **Read and write**. Existing installations need the permission
change approved by their owner. Reconnecting alone cannot grant it. Circular
always requests a draft PR and never falls back to a ready PR if the repository
or account cannot support drafts.

Revision `0011` adds project settings and a delivery ledger. A successful-run
trigger queues future opted-in work. The API processes that queue separately
from coding execution and Linear delivery. It verifies the captured diff's size
and SHA-256, reconstructs changes against the run's pinned original commit, and
publishes Git blobs, a tree, a commit and a new `circular/run/<run UUID>` branch.
The default branch is never updated. Existing branches must match the recorded
delivery exactly; Circular does not force-push or overwrite unrelated work.

The worker records the original commit before starting an agent. The API reads
the trusted repository cache and artifacts through read-only mounts; it needs
Git installed, `CIRCULAR_REPOSITORY_CACHE_ROOT`, and `CIRCULAR_ARTIFACT_ROOT`.
It does not publish workspace archives or ignored files. Legacy runs may use
their retained run branch; if the original base cannot be proven, delivery fails
with guidance instead of guessing the current default branch.

Delivery supports binary files, deletions, executable files and symlinks.
Workflow changes and submodules are rejected. Limits are 32 MiB per patch,
20 MiB per changed blob, 64 MiB total changed blob content and 1,000 changed files.
All changes are checked before uploading blobs. Missing artifacts, conflicting
branches and unsupported changes are shown on the Run without changing its
successful coding result.

Provider receipts survive restarts. Before creating a PR, Circular persists its
intent and uses an exact branch, repository identity and PR-body marker to
recover a lost response. An ambiguous create response with no confirmed PR is
shown as **uncertain**; checking again inspects GitHub without repeating the
create request. A confirmed PR queues one link comment through the existing
opt-in Linear outbox when the originating issue still belongs to the connected
workspace.

Settings use `GET/POST /api/v1/projects/{project_id}/integrations/github/run-delivery`.
Run status and publication use `GET/POST /api/v1/runs/{run_id}/github-delivery`.
POST queues work and returns its current state; clients poll GET until delivery
finishes. GitHub credentials stay in the trusted integration service.

## Linear authorization and issue import

The console prefills `http://localhost:8000/api/v1/integrations/linear/callback`
as the redirect URL, using the configured API origin for another deployment.
Circular requests `read,comments:create` access through user authorization with PKCE. To use an
app managed outside Circular, set `CIRCULAR_LINEAR_CLIENT_ID` in the server
environment and restart. `CIRCULAR_LINEAR_CLIENT_SECRET` is optional; PKCE code
exchange and refresh work with the public Client ID alone.

Linear supports PKCE, rotating refresh tokens, and token revocation. Circular
refreshes credentials automatically and shows a reconnect action when access
can no longer be refreshed. [Linear OAuth documentation](https://linear.app/developers/oauth-2-0-authentication).

Select a Linear team and optional project filter, then choose the Circular
Repository where the work belongs. **Import issue** saves the issue title,
description, identifier, and URL as a Task and opens its launcher. Choose an
enabled Agent and start the Run. Importing an issue twice within the same Circular
Project returns the existing Task without changing its saved details or Repository.

## Publish run updates

After authorization, **Publish run updates** opts a Project into start and result
comments on its imported Linear issues. Existing connections need another OAuth
authorization to grant `comments:create`; stored credentials are never assumed to
have the new scope. Importing issues continues to work with read-only credentials.

The API process drains a PostgreSQL outbox independently of HTTP requests and
coding execution. Revision `0009` adds the settings, granted scopes and outbox.
A trigger records future `running`, `succeeded`, `failed` and `cancelled` Run
transitions in the same transaction as the transition. Enabling publishing does
not backfill historical Runs. Each update retains its original issue and Linear
workspace identity. Disabling publishing or disconnecting skips pending updates.

Comments contain the Circular Run link and a bounded final-message excerpt with
reported GitHub pull-request links. Raw logs, diffs and artifacts are not attached.
If a Run finishes before its start comment is delivered, only its result is sent.
The outbox UUID is also the comment UUID; retries query that identity before
creating a comment so an accepted request with a lost response is not duplicated.
Transient provider failures retry with backoff. Authorization failures wait for
reconnection; public status endpoints expose safe delivery errors in the console.

The settings endpoints are `GET/POST
/api/v1/projects/{project_id}/integrations/linear/run-updates`, and individual Run
delivery status is `GET /api/v1/runs/{run_id}/linear-delivery`. Provider calls use
the same encrypted Project connection as imports. Multiple API processes serialize
delivery against the connection and outbox rows.

## Apply configuration and database upgrade

After updating from a version before integrations, wait for active Runs to finish,
then build and apply the additive migration before restarting API and worker:

```bash
docker compose build migrate api worker web
docker compose run --rm --no-deps migrate
docker compose up -d --no-deps api worker web
```

Revision `0003` preserves existing Projects, Repositories, Tasks, Runs, and
Integration metadata. It adds encrypted credentials, pending authorization state,
and unique external-resource identities. Existing duplicate GitHub/Linear IDs in
hand-edited `external_refs` must be resolved before the unique indexes can apply;
a failed migration rolls back without a partial schema update.
Revision `0004` adds encrypted provider app settings, browser-bound GitHub app
registration state, and the app identity for pending OAuth callbacks. It preserves
existing account credentials, connection metadata, and imported resources.

## Connection behavior and implementation

**Disconnect** clears local credentials and pending sign-ins, then attempts provider
revocation. If the provider is unavailable, the console confirms local disconnection
and asks you to remove access in the provider's settings. Imported Tasks and
Repositories remain. Future GitHub fetches require reconnection; a Run already
using its prepared Workspace can finish. Reconnect refreshes the account access
for the selected Project.

`internal/integrations` owns authorization, provider calls, imports, and encrypted
credential refresh. OAuth state is single-use, expires after ten minutes, and binds
the provider, Project, browser cookie, and connection generation. Stored tokens use
AES-GCM with the Integration identity as authenticated data. Refresh uses a database
row lock across API and worker processes. Public responses expose connection metadata
and the public Linear Client ID only. App settings use a separate encrypted record
bound to the provider name. The GitHub manifest exchange discards unused private
signing keys and webhook secrets. App changes are serialized with account sign-ins.
The worker verifies the imported GitHub Repository identity and exact HTTPS
clone URL before supplying a URL-scoped authorization header through Git's process
environment. Tokens are absent from command arguments, stored remotes, and Run
containers. The API and worker are trusted services with access to the encryption key.

The API contract documents registration, status, connect, disconnect, provider resource
browsing, imports, and callbacks under the **Integrations** tag. The browser sends
credentials when beginning a connection or GitHub app registration so it can store
the HttpOnly binding cookie. Callbacks verify that cookie before exchanging codes.

Run `go test -race ./...` with `TEST_DATABASE_URL` for authorization, concurrent
refresh, project isolation, imports, HTTP boundary, and Git credential tests.
`corepack pnpm test:e2e` starts owned provider fixtures on `127.0.0.1:18001` alongside
the isolated API and fake worker. It starts without configured apps and verifies
console registration, browser redirects, repository selection,
issue import through successful execution, reconnect, disconnect, and mobile layout.
Provider fixtures are enabled only by that test stack; production endpoint URLs
cannot be overridden through requests or environment variables.

This integration reads source code, creates repositories, delivers opted-in runs
as draft pull requests, imports issues and publishes opted-in Run comments.
Linear workflow status changes, automatic issue edit sync, and webhook handling
remain follow-up work.
Live provider validation requires the registered apps and the user's
authorization; the automated suite uses no real provider accounts.

### Provider app identity

Schema 0013 adds shared `provider_identities` and explicit per-project bindings. No binding means legacy user authorization; disabling a binding retains app mode. Linear app grants use `actor=app` with `read,comments:create`, browser-bound PKCE state, same-workspace checks, and serialized grant refresh. Failed upgrades preserve existing grants. GitHub registration retains its signing key and optional webhook secret in the encrypted app payload; existing apps can upload a bounded RSA PEM that is verified against `GET /app` before replacement.

For environment-managed GitHub applications, `CIRCULAR_GITHUB_PRIVATE_KEY_FILE` names a file in the trusted API/worker filesystem. With Compose, place it under `.circular/secrets` and use `/var/lib/circular/secrets/github-app.pem`. The API mounts that directory read-only. Run workloads receive no app signing keys. Installation access tokens are limited to one verified imported repository and its operation, and cached only until shortly before expiration.

Publication ledgers pin an immutable publisher before mutation. Linear comments use a durable lease, author/body reservation, and a started bit, with network mutation outside the database transaction. Pre-upgrade pending rows require receipt reconciliation even when their recorded attempt count is zero. Receipt checks verify target, body and original actor. Provider outages do not trigger a change of author.

### Dedicated webhook reception

`circular-webhooks` exposes `POST /webhooks/github`, `POST /webhooks/linear`, and minimal `GET /health`. Compose binds it to `127.0.0.1:8001`; operator-managed HTTPS ingress must target that listener. It has no Docker socket, repository/artifact mount or control API routes. Browser tests use receiver port 18002 and owned providers.

The receiver verifies HMAC over at most 1 MiB of raw JSON and Linear's signed millisecond timestamp (60-second window), then commits encrypted payloads and immutable digest receipts before acknowledging. A four-second handler deadline bounds reception. The API consumes leased rows independently; payloads expire after seven days while deduplication receipts remain. Unsupported events are permanently ignored, never replayed by enabling a later feature.

Accepted events remain retryable during provider verification outages until their
payload expires. Backoff grows from ten seconds to at most five minutes; a retry
count alone never discards an acknowledged event.

App-wide setup is private and explicit. GitHub uses its app-authenticated webhook configuration API. Pending rotation keys are persisted before that call; failed configuration retains the prior working key. Old-key overlap lasts at most ten minutes. Status includes only metadata and the last verified delivery, never a signing secret.

## Native Linear requests

Migration 0015 adds explicit workspace/scope routes, normalized requests, immutable
external run inputs, relational source/review session links and the native activity
outbox. The shared `postgres.CreateRun` and `postgres.CancelRun` operations also
serve ordinary HTTP launches/cancellation. An `external_refs` object never grants
session authority. Agent-session inputs are verified against the app viewer,
organization and current GraphQL session before routing and again before launch.

Project routes precede team fallback, including disabled project routes. The
identity/scope unique key prevents ambiguous destinations. Agent instructions,
model/effort, repository identity and normalized source enter the fingerprint.
Approval must present the current fingerprint. Snapshot/run/session association is
committed atomically; the worker loads its immutable inputs through that relation.

Automatic launch attempts reserve one ready request for thirty seconds and use a
twenty-five-second deadline. Per-request retry scheduling lets other projects
progress during an outage; a crashed consumer's reservation expires. Attempt
numbers fence late failure updates, and permanent identity/input errors move to
visible repair or approval states. Stopping or manually starting a request takes
precedence over retry completion.

A stop takes the request lock and persists a tombstone, even before creation.
Launch, publication and review operations use the same lock at their durable
reservation point. A write reserved before stop is in flight and can complete;
stop prevents subsequent reservations. HTTP is performed after the reservation
transaction commits. Started uncertain activities retain their UUID and frozen
app actor; reconciliation checks actor, session and content and never blindly
reissues the creation. Session link updates also reserve their write. Definitive
revocation disables affected routes and cancels linked work, while temporary
provider errors pause deliveries. Non-stop prompts are deduplicated and saved,
without injecting instructions into an active workload.

The private API exposes `/api/v1/external-requests` and project Linear
`request-routes`; the read-only MCP has `circular_list_requests` and
`circular_get_request`. Full control additionally exposes `circular_start_request`
and `circular_stop_request`. No MCP tool uploads keys or enables routes.

The browser fixture runs the real receiver, background consumers, PostgreSQL and
an isolated deterministic Docker workload. Its Git Data API writes actual Git
objects into an owned temporary repository, so draft publication and descendant
review use the same verified commits. `/fixture/*` endpoints exist only in the
disposable test command, never in the production API or receiver.
