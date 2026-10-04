# Circular App Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish existing GitHub and Linear work as Circular after a guided upgrade of the existing provider apps.

**Architecture:** Add shared app identities alongside project user grants. Resolve credentials by operation and bind every external write to a durable author snapshot. Add console controls without requiring webhooks or enabling incoming work.

**Tech Stack:** Go 1.27.1, PostgreSQL 17, existing encrypted vault and provider HTTP client, React/shadcn, generated OpenAPI, Go/Playwright tests.

**Spec:** [Circular identity design](../specs/2026-09-19-circular-identity-design.md), sections Identity storage, Credential selection, Console experience, and Testing.
**Tracking:** [ISQ-259](https://linear.app/isqrd/issue/ISQ-259/enable-circular-app-identity-and-app-authored-publications).

**Execution status (2026-09-20):** All 5 implementation tasks complete in the shared checkout, verified by the [final record](2026-09-19-circular-identity.md#plan-completion-record). The original steps below remain the execution recipe; actual checks and rulings are recorded in `.superpowers/sdd/2026-09-19-circular-identity-auth/progress.md`. Live acceptance is separately prepared in [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test).

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

1. A lost write response precedes an identity switch: task 4 must reconcile the original author without a second POST.
2. A renamed app has the same numeric bot ID: tasks 2 and 4 must preserve author receipts and refresh display metadata.
3. Two projects refresh/disconnect the same workspace grant: task 3 serializes refresh and detaches only the requested project.
4. A PEM belongs to another app or an installation lost one repository: task 2 fails without replacing the working key or widening access.
5. An OAuth reconnect returns a different workspace or a stale callback: task 3 leaves the existing binding intact.

## File and interface map

Keep credential/actor logic in `internal/integrations`; it already owns the vault and provider requests. Add focused files instead of expanding the already-large connection page and service files indefinitely.

| New file                                           | Responsibility                                                                 |
| -------------------------------------------------- | ------------------------------------------------------------------------------ |
| `internal/integrations/identity.go`                | Public identity status and project binding operations                          |
| `internal/integrations/identity_store.go`          | Shared identity records, generation/refresh locks, encrypted credential access |
| `internal/integrations/github_identity.go`         | Key verification, app metadata, scoped installation token acquisition          |
| `internal/integrations/linear_identity.go`         | App OAuth states, workspace grant reuse, refresh, verification                 |
| `internal/integrations/publication_identity.go`    | Selection and use of immutable publication author snapshots                    |
| `internal/httpapi/integration_identity.go`         | Private console endpoints and public safe DTOs                                 |
| `apps/web/src/components/integration-identity.tsx` | Identity summary and project enable/repair controls                            |
| `apps/web/src/components/github-app-key.tsx`       | PEM upload with no persisted browser secret                                    |

Define these types in `identity.go`; JSON response schemas mirror the public types only:

```go
type IdentityStatus struct {
    Provider string `json:"provider"`
    Mode string `json:"mode"` // user or app
    IdentityID string `json:"identity_id"`
    AccountID string `json:"account_id"`
    AccountName string `json:"account_name"`
    ActorID string `json:"actor_id"`
    ActorName string `json:"actor_name"`
    ActorLogin string `json:"actor_login"`
    AvatarURL string `json:"avatar_url"`
    Status string `json:"status"` // available, enabled, disabled, needs_setup, needs_access, reconnect_required
    Capabilities []string `json:"capabilities"`
    AffectedProjects []string `json:"affected_projects"`
    EnvironmentManaged bool `json:"environment_managed"`
    Reason string `json:"reason"`
}
type PublisherIdentity struct {
    Mode string `json:"mode"`
    IdentityID string `json:"identity_id,omitempty"`
    Provider string `json:"provider"`
    AccountID string `json:"account_id"`
    ActorID string `json:"actor_id"`
    AppClientID string `json:"app_client_id,omitempty"`
}
type GitHubPurpose string
const (
    GitHubRead GitHubPurpose = "read"
    GitHubPublish GitHubPurpose = "publish"
    GitHubReview GitHubPurpose = "review"
)
```

`Service` interfaces introduced by tasks below:

```go
Identity(ctx context.Context, project, provider string) (IdentityStatus, error)
BindIdentity(ctx context.Context, project, provider, identityID string) (IdentityStatus, error)
DetachIdentity(ctx context.Context, project, provider string) error
SaveGitHubIdentity(ctx context.Context, project string, pem []byte) (IdentityStatus, error)
BeginLinearAppAuthorization(ctx context.Context, project, purpose string) (Authorization, error)
PublicationIdentity(ctx context.Context, project, provider, accountID string) (PublisherIdentity, error)
```

`purpose` is the validated enum `identity` or `agent`; stage 1 implements identity and rejects agent with an actionable unavailable response until stage 3. No caller can supply arbitrary OAuth scopes. Token-bearing helpers remain unexported or restricted to the existing trusted `GitCredential` interface.

### Task 1: Shared identity records and explicit project bindings

**Files:** Create `internal/migrate/0013.sql`, `internal/migrate/identity_test.go`, `internal/integrations/identity.go`, `identity_store.go`, `identity_test.go`. Modify `internal/migrate/migrate.go`, `internal/integrations/service.go`.

**Consumes:** Existing `integration_apps`, `integrations`, `Service.seal/open`, and `testsupport.Database(t)`.
**Produces:** `Identity`, `BindIdentity`, `DetachIdentity`, identity generation/locked refresh storage. Add typed `ErrIdentityConflict` and `ErrIdentityUnavailable` errors.

- [ ] Add upgrade tests starting at schema 0012 with enabled legacy integrations and pending publication rows. After migration, assert no bindings, unchanged grants/settings/receipts, and no new runs.
- [ ] Write the binding contract test using the existing `setup(t)` fixture and `f.connect` helpers. Insert a verified identity in the test database; never call a real provider.

```go
func TestIdentityUpgradeStartsInUserMode(t *testing.T) {
    f := setup(t)
    f.connect(t, "github")
    got, err := f.service.Identity(t.Context(), f.project, "github")
    if err != nil || got.Mode != "user" || got.IdentityID != "" {
        t.Fatalf("legacy identity changed: %+v, %v", got, err)
    }
}
```

- [ ] Run `go test ./internal/integrations ./internal/migrate -run 'Identity|Upgrade' -count=1` with disposable `TEST_DATABASE_URL`; confirm the intended failure before implementation.
- [ ] Add the identity table and binding uniqueness. Include timestamps, disabled/status fields, encrypted credential bytes, and scopes as defined in the spec. Use a composite foreign key/validation to prevent binding a GitHub identity to a Linear integration.

```sql
CREATE TABLE provider_identities (
    id UUID PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('github','linear')),
    app_client_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    account_name TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_name TEXT NOT NULL,
    actor_login TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    credentials BYTEA,
    granted_scopes TEXT[] NOT NULL DEFAULT '{}',
    generation BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider,app_client_id,account_id),
    UNIQUE(id,provider)
);
CREATE TABLE integration_identity_bindings (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    identity_id UUID NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(project_id,provider),
    FOREIGN KEY(identity_id,provider) REFERENCES provider_identities(id,provider)
);
```

- [ ] Seal each grant with associated data `provider-identity:<UUID>`; never reuse a project's legacy ciphertext under a new identity. Return only `IdentityStatus`. Detach disables the binding while retaining app mode; it does not delete the binding, shared identity, or grant. Test that a disabled binding pauses app publication rather than selecting a user token. Only a project with no binding defaults to legacy user mode.
- [ ] Pass the targeted suite, including concurrent bindings, wrong provider, disabled identity, and ciphertext/public-DTO redaction assertions. Record the result in the implementation issue. Commit only if separately authorized for the execution checkout.

### Task 2: GitHub App verification and repository-scoped tokens

**Files:** Create `internal/integrations/github_identity.go`, `github_identity_test.go`, `github_identity_internal_test.go`, `internal/testsupport/github_identity.go`. Modify `internal/integrations/apps.go`, `apps_test.go`, `config.go`, `imports.go`, `internal/testsupport/providers.go`, `.env.example`, `compose.yaml`.

**Consumes:** Task 1 identity records, existing OAuth app/client ID, imported repository numeric installation/repository IDs.
**Produces:** `SaveGitHubIdentity`; internal JWT signer and `withGitHubRepository(ctx, project, repositoryID string, purpose GitHubPurpose, use func(token string, actor PublisherIdentity) error) error`. Legacy user mode is selected only when no app binding record exists; a disabled app binding returns an unavailable error.

- [ ] Extend the owned provider fixture to verify signed RSA JWT claims and capture requested installation-token repository/permission scope. Add fixture controls for app/client mismatch, expired token, suspended installation, missing repository, and renamed bot. Return real generated test RSA keys, never a production PEM.
- [ ] Write a signer unit test against `githubJWT(key *rsa.PrivateKey, clientID string, now time.Time) (string, error)`. Decode the JWT, verify with `rsa.VerifyPKCS1v15`, and assert `iss`, bounded expiry, and clock skew. Add provider integration tests asserting token calls contain exactly the requested repository and never use `/user` with installation credentials.
- [ ] Run `go test ./internal/integrations -run 'GitHubIdentity|GitHubJWT|InstallationToken' -count=1` and confirm failure.
- [ ] Parse bounded PEM (maximum 64 KiB, RSA PKCS#1 or PKCS#8), verify the app with `GET /app` using the registered client ID as issuer, then obtain the bot numeric ID from the provider. Replace the encrypted stored key only after all identity checks pass. Preserve existing OAuth credentials and slug updates.

```go
claims := struct {
    Iss string `json:"iss"`
    Iat int64 `json:"iat"`
    Exp int64 `json:"exp"`
}{clientID, now.Add(-60 * time.Second).Unix(), now.Add(9 * time.Minute).Unix()}
```

- [ ] Request installation tokens for the verified imported repository. Use metadata/contents read for clone, and only the existing required write permissions for publication/review. Cache by installation, repository, purpose, and key generation until at least 60 seconds before expiry; invalidate on key rotation or access errors. Never follow cross-origin redirects.
- [ ] Retain GitHub manifest `id`, `pem`, and optional webhook secret encrypted for newly registered apps. Update the existing test that explicitly expects their absence. Existing apps show `needs_setup` until a valid key upload; do not recreate them.
- [ ] Keep personal repository creation and installation/account selection on existing user authorization. Add regression tests for `GitCredential` verifying exact clone URL and numeric repository mapping under both modes, plus app failure without user fallback.
- [ ] Pass targeted tests, including concurrent refresh/rotation and renamed bot. Record results; create a commit only under execution authorization.

### Task 3: Workspace-scoped Linear app authorization

**Files:** Create `internal/integrations/linear_identity.go`, `linear_identity_test.go`, `internal/testsupport/linear_identity.go`. Modify `internal/integrations/service.go`, `providers.go`, `linear.go`, `imports.go`, `internal/migrate/0013.sql` before application, and callback handling in `internal/httpapi/integrations.go`.

**Consumes:** Task 1 identities and existing browser/state/PKCE helpers.
**Produces:** `BeginLinearAppAuthorization`; verified app identity callback; `withLinearIdentity(ctx, identityID string, use func(token string) error) error`, with locked refresh; binding-aware issue reads.

- [ ] Define an app-specific OAuth state table carrying state/browser digests, initiating project, purpose, encrypted verifier, generation, client ID, expected workspace, and expiry. Add a per-project attempt generation so older callbacks cannot overwrite newer setup. A state is consumed once regardless of provider success.
- [ ] Add a URL test verifying identity-purpose app authorization and targeted scopes; preserve legacy `Begin` behavior.

```go
func TestLinearIdentityAuthorizationUsesAppActor(t *testing.T) {
    f := setup(t)
    auth, err := f.service.BeginLinearAppAuthorization(t.Context(), f.project, "identity")
    if err != nil { t.Fatal(err) }
    u, err := url.Parse(auth.URL)
    if err != nil { t.Fatal(err) }
    if u.Query().Get("actor") != "app" || u.Query().Get("scope") != "read,comments:create" {
        t.Fatal("wrong actor or identity scopes", u.Query())
    }
}
```

- [ ] Run `go test ./internal/integrations ./internal/httpapi -run 'LinearIdentity|AppAuthorization' -count=1` and confirm failure.
- [ ] Fetch `organization { id name urlKey }` and `viewer { id name avatarUrl }` with the returned app grant. Validate provider identity and requested scopes before committing the shared identity and project binding. Preserve the exact actor=app authorization intent in the state; do not infer an app actor from a display name.
- [ ] Serialize workspace grant replacement and refresh by `(provider, client ID, workspace ID)`. Other projects bind that same stored grant. Reject a reconnect to another workspace with a specific conflict response; offer a distinct new-workspace setup path instead of rewriting imported tasks.
- [ ] Separate project detach from provider-wide removal, which remains in the linked provider app settings for this release. Cancelling authorization, using the wrong browser, replaying a state, switching client ID, and concurrent refresh must leave existing valid grants intact. Reconnect only repairs rows belonging to the same workspace/actor; do not blindly reset every failed publication.
- [ ] Pass tests for two projects sharing one grant, one-project detach, refresh-token rotation, scope loss, revoked access, and redaction. Record results and authorized checkpoint.

### Task 4: Pin authors on existing publications

**Files:** Create `internal/integrations/publication_identity.go`, `publication_identity_test.go`. Modify `github_publish.go`, `github_delivery.go`, `pr_review_publish.go`, `pr_review_launch.go`, `pr_review_freshness.go`, `pr_review_linear.go`, `linear_updates.go`, existing publication tests, and additive actor columns in `internal/migrate/0013.sql` before application.

**Consumes:** `PublisherIdentity`, token resolvers from tasks 2/3, existing durable publication ledgers.
**Produces:** `PublicationIdentity` and actor-aware receipt reconciliation for draft PRs, review COMMENTs, and Linear comments. Same workflow results, verified app authors.

- [ ] Add a nullable `publisher JSONB` column to `github_run_deliveries`, `pr_review_publications`, and `linear_run_updates`. Validate the JSON against `PublisherIdentity` before use. A legacy review's `expected_author_id` remains authoritative; no migration guesses an app actor. Draft-PR and ordinary Linear rows lack a stored author and need the explicit legacy-reconciliation path below.
- [ ] Add durable `started`, lease owner/expiry, and receipt/uncertainty state to ordinary Linear publication. Refactor its current transaction-spanning provider call into claim -> commit body/actor intent -> remote request -> fenced completion, preserving refresh-token rotation and existing ordering/coalescing. Extend its status constraint/API/UI with `uncertain` rather than hiding an ambiguous write as an ordinary retry.
- [ ] Mark all pre-upgrade pending legacy Linear rows for receipt reconciliation, including zero-attempt rows: their old transaction could have rolled back after remote success. Check the deterministic comment UUID and exact issue/body first. Likewise reconcile a legacy draft PR using its exact marker/repository/branch. A confirmed receipt records its observed historical actor; when no original actor/write outcome can be established, pause for repair without reposting as the app. Add migration tests for the zero-local-attempt/remote-success case.
- [ ] Add fault-injection tests to the existing fixture: provider accepts POST, response is lost, project enables an app identity, worker restarts. Assert one remote object, original actor receipt recovered, no second POST. Add forged/wrong-actor receipt rejection and stale PR head regression.
- [ ] Run `go test ./internal/integrations -run 'PublicationIdentity|Receipt|Publish|Linear.*Update|GitHub.*Delivery' -count=1` and confirm the new identity tests fail.
- [ ] Before the first remote mutation, persist the exact selected `PublisherIdentity` with the existing body/marker intent. Existing started publications reconcile before current capability/freshness checks, preserving the workflow's existing uncertainty semantics. Once a publisher is set, retry uses it even if the operation has not reached its remote-write reservation yet.

```sql
UPDATE pr_review_publications
SET publisher=$3::jsonb, expected_author_id=$4
WHERE review_id=$1 AND lease_owner=$2 AND NOT started
  AND publisher IS NULL AND lease_until>now();
```

- [ ] Replace review publishing's unconditional `/user` lookup with verified actor selection. Receipt matching uses the stored numeric actor ID. Reads for reconciliation may use renewed credentials for the same provider identity; never change the intended author or repeat a mutation because credentials changed.
- [ ] Update preparation, permission checks, freshness checks, Git clone, PR publishing, and review publishing together so app-enabled projects remain usable when their user token expires. Keep personal repository creation's user-mode path explicit.
- [ ] Expose actor metadata in delivery status and show the internal agent name in bounded messages. Do not rewrite historical comments/reviews or backfill old runs. Fail app authorization visibly without falling back to a human.
- [ ] Pass targeted suites, then `go test -race ./internal/integrations ./internal/postgres`; use disposable PostgreSQL and inspect that database-dependent tests ran. Record results and authorized checkpoint.

### Task 5: Guided console setup, contracts, and user docs

**Files:** Create `internal/httpapi/integration_identity.go`, `integration_identity_test.go`, `apps/web/src/components/integration-identity.tsx`, `github-app-key.tsx`, `tests/browser/identity.spec.ts`. Modify `internal/httpapi/integrations.go`, `schema.go`, `apps/web/src/pages/integrations.tsx`, `apps/web/src/api.ts`, generated contracts, `internal/testsupport/providers.go`, and `docs/user-guide/connections.md`, `docs/user-guide/troubleshooting.md`, `docs/development/integrations.md`.

**Consumes:** Public status and operations defined above.
**Produces:** Private identity setup routes, verified app identity cards, upgrade documentation. No webhook enablement or automatic run setting is changed.

- [ ] Specify/implement these private endpoints using current project/origin/JSON/no-store guards:

```text
GET  /api/v1/projects/{project_id}/integrations/{provider}/identity
POST /api/v1/projects/{project_id}/integrations/github/identity/key
     { "private_key": "PEM bytes" }
POST /api/v1/projects/{project_id}/integrations/linear/identity/connect
     { "purpose": "identity" }
POST /api/v1/projects/{project_id}/integrations/{provider}/identity/bind
     { "identity_id": "UUID" }
POST /api/v1/projects/{project_id}/integrations/{provider}/identity/detach
     {}
```

- [ ] Add HTTP tests for wrong origin, oversized key, foreign identity, and response redaction. Add a fixture-backed browser test of old user connection -> upload/reconnect -> verified Circular identity, with cancelled and failed setup preserving the old state.

```ts
await expect(
  page.getByRole("heading", { name: "Circular identity" }),
).toBeVisible();
await page.getByRole("button", { name: "Use Circular identity" }).click();
await expect(
  page.getByText("Acting as Circular", { exact: true }),
).toBeVisible();
await expect(
  page.getByText("External requests are off", { exact: true }),
).toBeVisible();
```

- [ ] Run `go test ./internal/httpapi -run Identity -count=1` and the new browser file with an owned fixture stack; confirm failure before adding the form. Build key upload using `<input type="file">` inside the existing shadcn form; clear PEM state after submission/cancel/unmount and never put it in query cache, storage, error messages, or URLs.
- [ ] Show provider-returned branding, account/workspace, affected-project count, and capability-specific repair action. Distinguish account authorization from bot readiness; communicate user authorization remains needed for personal repository creation. Link to existing provider app settings for name/icon and permission approval.
- [ ] Regenerate OpenAPI/client types. Update user docs with existing-app upgrade and new-app automatic key retention; leave webhook instructions clearly for the later reception stage. Keep environment administration details in development docs.
- [ ] Run the stage verification commands from the umbrella plan, existing repository creation/publication/review browser regressions, and mobile 390x844 inspection. Require a real fixture-backed bot author on both providers. Update the Linear issue with evidence; do not claim live-provider acceptance from fixture results.

## Exit criteria

- [x] Guided setup uses the existing apps and confirms the real provider actor.
- [x] New selected publications and trusted repository operations use app credentials; personal GitHub creation still works.
- [x] Legacy pending/uncertain deliveries recover correctly across the upgrade.
- [x] No existing integration or automation is silently switched on.
- [x] Tests, generated contracts, user docs, and stage review are complete.
