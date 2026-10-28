# Approve Task scope

Coding clients propose new work through `propose_task`. The response identifies
an immutable revision and supplies a private review URL. The user opens that URL,
signs in with GitHub, reviews the scope, and approves or rejects it.

Approval creates one Task or confirms/amends the selected existing Task. It does
not start a Run, publish changes, or merge a pull request. Clear new work becomes
Ready when it has a Repository and its prerequisites are complete. Open Tasks
retain their stage when prerequisites later complete. Move eligible work to Ready
before an external claim. Incomplete requirements need revision.

Before claiming existing work, read `get_task_approval` and `get_task_work`. Use
the reviewed purpose and acceptance criteria. Unchanged approved scope needs no
second decision. A Linear issue, label, status, comment, or agent assertion cannot
supply a user decision. Linear imports reuse Circular's verified receipt for the
same Project, workspace, issue, and unchanged scope.

## Instance setup

1. Keep the console, control API, and MCP behind their existing private access.
2. Use the existing registered GitHub App, or its configured OAuth client ID and
   secret. Keep the integration encryption key unchanged.
3. Add this **additional** GitHub App user authorization callback URL:
   `<CIRCULAR_PUBLIC_API_URL>/api/v1/approval-auth/github/callback`.
   New App manifests include both callbacks. For an existing App, keep the
   integration callback and add the approval callback. For the production SSH console, the
   default private callback is
   `http://localhost:18080/api/v1/approval-auth/github/callback`.
4. Set `CIRCULAR_APPROVER_GITHUB_IDS` to the allowed users' immutable numeric
   GitHub IDs, separated by commas. Obtain an ID from the GitHub `/user` API for
   your account or the public `/users/{login}` API. Do not use a login name,
   installation ID, organization ID, or bot ID. This setting is not editable by
   project APIs or MCP.
5. Restart the API through the normal deployment process. Open Proposals and
   sign in. Missing configuration leaves decisions disabled and proposals pending.

GitHub documents PKCE and additional callbacks in its
[user authorization guide](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
and [callback URL guide](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url).

The browser sign-in is separate from project integration grants. Login state lasts
ten minutes and binds the browser, PKCE challenge, app, and return page. The
provider token is discarded after identity lookup. It is neither stored nor
revoked through the integration connection. Approval sessions last thirty minutes.
Sign out to delete the session. Removing an ID from the configuration revokes its
approval access after the API restarts.

Serve the review page with `Cache-Control: no-store`, `X-Frame-Options: DENY`, and
`Content-Security-Policy: frame-ancestors 'none'`. The supplied private Caddy
listener supplies these headers. Do not expose the new callback on the public
vitrine or webhook listener. GitHub returns the user's browser to the private URL;
it does not need a server-to-server route to that callback.

## Compatibility

Existing Tasks, Runs, work sessions, and history remain in place. Historical
consent without a verified receipt is shown as unknown. A new Run or external
claim needs a receipt for the current scope. Existing admitted work can continue.

`POST /tasks`, discovery, Linear import, semantic Task patches, and completion
policy changes can return **202 with a TaskProposal**. Callers must not treat that
response as a Task or start work. Scheduling and priority edits retain their
existing behavior. The console opens the review page. MCP returns the proposal.
Use the versioned proposal API for idempotent creation with a retained request key.
No ordinary model tool can approve or reject a proposal.

Native delegation retains the parent checkpoint while waiting. Approval records
the child Task. The worker then checks the saved execution intent, current Agent
policy, Task scope, ownership, and readiness before admitting the child once.
Refusal, or an execution intent that is no longer eligible, resumes the parent
with an explicit outcome and no child Run. The parent must continue its original
scope and must not repeat the rejected proposal.
