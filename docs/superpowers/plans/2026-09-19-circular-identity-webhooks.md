# Circular Webhook Reception Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Receive provider events through a separate, verified, durable endpoint and make reception/setup status visible in Circular.

**Architecture:** A small listener verifies and persists incoming deliveries using the existing encrypted integration store. API background consumers process the inbox separately. The listener never exposes control routes and stage 2 never launches runs.

**Tech Stack:** Go 1.27.1 net/http and crypto, PostgreSQL 17, existing vault, Compose, React/shadcn, Go/Playwright tests.

**Spec:** [Circular identity design](../specs/2026-09-19-circular-identity-design.md), sections Webhook receiver, Console experience, and Testing.
**Dependency:** [Stage 1 identity](2026-09-19-circular-identity-auth.md).
**Tracking:** [ISQ-260](https://linear.app/isqrd/issue/ISQ-260/receive-provider-webhooks-through-a-dedicated-circular-listener).

**Execution status (2026-09-20):** All 3 implementation tasks complete in the shared checkout, verified by the [final record](2026-09-19-circular-identity.md#plan-completion-record). The original steps below remain the execution recipe; actual checks and rulings are recorded in `.superpowers/sdd/2026-09-19-circular-identity-webhooks/progress.md`. Live acceptance is separately prepared in [ISQ-262](https://linear.app/isqrd/issue/ISQ-262/validate-circular-app-identity-in-circular-test).

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

1. Valid signature with a foreign workspace/install ID: task 2 must ignore/quarantine it without borrowing another identity.
2. Changed bytes or reused delivery ID: task 1 must reject tampering and prevent duplicate effects.
3. Local URL or tunnel points to the control API: task 3 must explain the separate receiver and never label a saved URL verified.
4. Database fails or the process dies after acknowledgement: tasks 1/2 must acknowledge only persisted deliveries and recover leases.
5. Secret rotation or late revocation races new setup: tasks 2/3 must preserve valid receipts and never reactivate from a stale event.

## File and interface map

| New file                                           | Responsibility                                                             |
| -------------------------------------------------- | -------------------------------------------------------------------------- |
| `internal/webhooks/handler.go`                     | Allowed routes, bounded raw-body parsing, signature/timestamp verification |
| `internal/webhooks/types.go`                       | Narrow receiver contracts and immutable verified-delivery value            |
| `internal/integrations/webhook_store.go`           | Encrypted secrets/payloads, acceptance uniqueness, claims, retention       |
| `internal/integrations/webhook_process.go`         | Provider identity resolution and access-change invalidation                |
| `internal/integrations/webhook_settings.go`        | Configuration and safe diagnostic projection                               |
| `cmd/circular-webhooks/main.go`                    | Receiver-only process, deadlines, health, shutdown                         |
| `internal/httpapi/webhook_settings.go`             | Private setup/status endpoints                                             |
| `apps/web/src/components/integration-webhooks.tsx` | Reception setup and verified-delivery status                               |

`internal/webhooks` imports no HTTP API or integrations package. `integrations.Service` implements its interfaces. Compose the receiver with that service in the new command.

```go
type SigningKey struct {
    AppClientID string
    Current []byte
    Previous []byte
    PreviousUntil time.Time
}
type VerifiedDelivery struct {
    Provider string
    AppClientID string
    DeliveryID string
    Event string
    Body []byte
    Digest [32]byte
    ReceivedAt time.Time
}
type Acceptance struct {
    ID string
    Duplicate bool
}
type SigningKeys interface {
    WebhookSigningKey(context.Context, string) (SigningKey, error)
}
type Inbox interface {
    AcceptWebhookDelivery(context.Context, VerifiedDelivery) (Acceptance, error)
}
// NewHandler(keys SigningKeys, inbox Inbox, now func() time.Time) http.Handler
```

Token/secret-bearing values are never JSON-marshaled. Receiver API types do not enter the public OpenAPI schema. `Service.ProcessIntegrationWebhook(context.Context) (bool, error)` is the background consumer contract; stage 3 extends its dispatch for agent events only after that consumer exists.

### Task 1: Verified, bounded, durable acceptance

**Files:** Create `internal/webhooks/types.go`, `handler.go`, `handler_test.go`, `internal/integrations/webhook_store.go`, `webhook_store_test.go`, `internal/migrate/0014.sql`, `internal/migrate/webhooks_test.go`. Modify `internal/migrate/migrate.go`.

**Consumes:** Stage 1 registered app identity and existing vault.
**Produces:** `webhooks.NewHandler`, `Service.WebhookSigningKey`, `Service.AcceptWebhookDelivery`, inbox rows and durable deduplication receipts.

- [ ] Write table-driven HMAC tests with an in-memory `SigningKeys`/`Inbox` implementation: original raw JSON succeeds, reserialized/tampered JSON fails, invalid hex fails, missing signature fails, body >1 MiB returns 413, and an old signed Linear timestamp fails even with a current unsigned header. Define the test double to record the exact received bytes and optionally return a storage error.

```go
func signedBody(secret, body []byte) string {
    mac := hmac.New(sha256.New, secret)
    _, _ = mac.Write(body)
    return hex.EncodeToString(mac.Sum(nil))
}
// GitHub header: X-Hub-Signature-256 = "sha256=" + signedBody(secret, raw)
// Linear header: Linear-Signature = signedBody(secret, raw)
// Tests sign the transmitted raw bytes, not an independently encoded object.
```

- [ ] Add PostgreSQL tests for duplicate delivery IDs, identical semantic payload under a second header ID, changed digest for an existing ID, failed insert, and encrypted-at-rest payloads. Acceptance IDs return only after commit. Uniqueness alone is insufficient for domain run idempotency; stage 3 separately owns that invariant.
- [ ] Run `go test ./internal/webhooks ./internal/integrations ./internal/migrate -run 'Webhook|Inbox' -count=1` and confirm failure before adding handlers/storage.
- [ ] Add `integration_webhook_settings` keyed by provider/client ID with encrypted signing keys, public HTTPS origin, previous-key expiry, last verified delivery time, and configuration status. Add inbox records with this schema boundary:

```sql
CREATE TABLE integration_webhook_deliveries (
    id UUID PRIMARY KEY,
    provider TEXT NOT NULL,
    app_client_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    event TEXT NOT NULL,
    body_sha256 BYTEA NOT NULL,
    payload BYTEA,
    received_at TIMESTAMPTZ NOT NULL,
    payload_expires_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'received',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner UUID,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    UNIQUE(provider,app_client_id,delivery_id)
);
```

- [ ] Validate header lengths/required fields, supported providers, JSON content type, UUID delivery IDs, and signatures before accepting. Derive app client ID from the selected server signing-key record, never the request. After signature verification validate signed Linear timestamp within 60 seconds; use handler clock injection for deterministic tests.
- [ ] Bound HTTP handling to four seconds with a three-second persistence context. No provider network calls in handler code. Treat verified no-op events and exact duplicate receipts as 200; changed payload for an existing ID is 409; invalid signature 401; malformed 400; storage unavailable 503.
- [ ] Seal raw payloads with associated data `webhook-delivery:<UUID>`. Duplicate lookup compares the stored digest without decrypting/reparsing another body. Scrub public errors and logs. Pass the targeted tests plus `-race` for receiver/inbox concurrency; record evidence.

### Task 2: Separate receiver process and recoverable access-change processing

**Files:** Create `cmd/circular-webhooks/main.go`, `main_test.go`, `internal/integrations/webhook_process.go`, `webhook_process_test.go`. Modify `internal/httpapi/background.go`, `infra/go.Dockerfile`, `compose.yaml`, `.env.example`, `internal/testsupport/providers.go`, `cmd/circular-e2e-stack/main.go`, `scripts/e2e-stack.mjs`.

**Consumes:** Task 1 receiver/inbox, stage 1 provider identities/token verification.
**Produces:** Packaged receiver on loopback port 8001, `ProcessIntegrationWebhook`, retention processing, safe identity invalidation. Fixture stack gets its own unused receiver port.

- [ ] Add receiver routing tests that POST to `/api/v1/runs`, `/mcp`, `/api/v1/integrations/github/callback`, and `/artifacts`: all return 404. `/health` returns only readiness. No control API handler is imported or mounted.
- [ ] Add durable processing tests: accept event -> kill consumer after claim -> expire lease -> second consumer finishes once. Add delayed installation/repository permission changes and unknown Linear organization/client ID. Assert no runs are created in stage 2.
- [ ] Run `go test ./internal/webhooks ./internal/integrations ./cmd/circular-webhooks -run 'Webhook|Receiver' -count=1` and confirm intended failures.
- [ ] Start only `webhooks.NewHandler` using an `http.Server` with short read/header/write deadlines and bounded headers. Share the encrypted integration configuration and database, but give the receiver no Docker socket, artifact mount, repository cache, or worker execution roots.

```yaml
webhooks:
  build:
    context: .
    dockerfile: infra/go.Dockerfile
    target: webhooks
  ports:
    - "127.0.0.1:8001:8001"
  environment:
    DATABASE_URL: postgresql://circular:circular@postgres:5432/circular
    CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY: ${CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY:-}
  depends_on:
    migrate:
      condition: service_completed_successfully
```

- [ ] Claim inbox rows with `FOR UPDATE SKIP LOCKED`, short transactions, lease owner/expiry fencing, and bounded retries. Provider calls occur after claim commit. On access-change events, resolve by verified app client ID plus installation/workspace ID; invalidate and freshly verify the affected identity. Never adopt an unknown account or reactivate from payload text.
- [ ] Have periodic retention clear encrypted bodies after seven days; unprocessed expired events become permanently expired and cannot launch later. Retain digest/delivery receipts needed for deduplication and request references. Expired lease claims are recoverable without a manual retry.
- [ ] Mark unsupported agent-session events `ignored` with a safe reason while stage 3 is absent; installing stage 3 must not replay ignored history. Add revocation/permission-change handlers that mark relevant identities unavailable and pause their pending work without deleting receipts.
- [ ] Pass process/routing/lease tests and restart the owned fixture receiver to prove recovery. Build the receiver image without deploying the live Compose stack. Record evidence and authorized checkpoint.

### Task 3: Reception setup, capability diagnostics, and user documentation

**Files:** Create `internal/integrations/webhook_settings.go`, `webhook_settings_test.go`, `internal/httpapi/webhook_settings.go`, `webhook_settings_test.go`, `apps/web/src/components/integration-webhooks.tsx`, `tests/browser/webhooks.spec.ts`. Modify `internal/httpapi/integrations.go`, `schema.go`, `apps/web/src/pages/integrations.tsx`, `apps/web/src/api.ts`, generated contracts, `docs/user-guide/connections.md`, `docs/user-guide/troubleshooting.md`, and `docs/development/integrations.md`.

**Consumes:** Receiver receipts, stage 1 identity status, GitHub app JWT capability.
**Produces:** Private console configuration/status routes and honest readiness display; no public administrative routes.

```text
GET  /api/v1/integrations/{provider}/webhooks
POST /api/v1/integrations/{provider}/webhooks
     { "public_origin": "https://receiver.example", "signing_secret": "write-only optional input" }
POST /api/v1/integrations/{provider}/webhooks/check
     {}
```

All routes use the existing private origin/JSON/no-store boundary. Settings are app-wide; responses include affected projects and derived callback URLs but omit secrets. `check` reads provider configuration and recent verified receipts; it does not launch work or manufacture a success event.

- [ ] Write validation tests for localhost, private IP literals, non-HTTPS, embedded credentials, query strings/fragments, and unexpected paths. Do not make arbitrary server-side requests to a user-entered URL. Test wrong origin and secret redaction on success and provider error responses.
- [ ] Add browser tests where identity is ready and reception is waiting. Saving a syntactically correct URL keeps `Waiting for a verified delivery`; injecting a signed fixture event changes to `Receiving events`. Include secret/configuration mismatch, provider outage, and permission-repair states.

```ts
await page
  .getByLabel("Public receiver address")
  .fill("https://receiver.example");
await page.getByRole("button", { name: "Save receiver settings" }).click();
await expect(
  page.getByText("Waiting for a verified delivery", { exact: true }),
).toBeVisible();
await expect(
  page.getByText("External requests are off", { exact: true }),
).toBeVisible();
```

- [ ] Run the new HTTP and browser tests and confirm failure. Implement focused shadcn forms with exact provider links, current status/time, copyable callback URLs, and actionable errors.
- [ ] Automate GitHub webhook URL/content type/secret through its app-authenticated configuration API after the explicit save action. Verify existing configuration first and show its app-wide impact. Manual permission/event subscription steps remain provider links with a subsequent capability check. New-app manifests include hooks only when a public receiver is configured; local-only identity setup continues to omit them.
- [ ] Store Linear's signing secret from the app settings as a write-only value. Keep Agent session events instructions gated until stage 3 is available; permission/revocation reception can be configured earlier. For rotations, persist a pending key before provider configuration, allow the old key for at most ten minutes after success, and surface failed configuration without discarding the old working key.
- [ ] Explain the deployment split in user docs: local browser OAuth can use localhost; incoming webhooks need public HTTPS to port 8001's dedicated receiver. Support operator-managed HTTPS/tunnels without assuming a vendor or promising one-click tunnel provisioning. Distinguish reachability from verified provider delivery. Do not instruct users to tunnel ports 8000/5173.
- [ ] Regenerate/check contracts and run targeted browser, typecheck, build, and docs checks. Inspect mobile/desktop layout and confirm fixture control APIs remain unreachable through the receiver. Record evidence in Linear.

## Exit criteria

- [x] Valid events survive acknowledgement/restart and invalid requests have no effects.
- [x] Receiver image exposes only its three routes and is independently deployable.
- [x] Revocation/access changes cannot route across accounts or erase receipts.
- [x] Console distinguishes configured, waiting, receiving, and repair-needed states.
- [x] No Linear agent sessions or coding runs are activated by this stage.
