# Public vitrine and private app

`https://circular.ruohao.dev` serves the static product presentation built from
`apps/vitrine`. It has no sign-in prompt, console bundle, control API, or MCP
route. Only the two exact signed webhook routes reach the dedicated receiver.
Provider webhooks still need their own configuration and verified deliveries.

The working console and its API are served by a separate Caddy listener on
container port 8080, published **only on VPS loopback** at `127.0.0.1:8080`.
The owner password protects this private listener as an additional check.
PostgreSQL and backend services have no published host ports. One worker runs
jobs serially, with each agent limited to one CPU and 2 GiB of RAM.

## Open the private app

Run this on the computer where you will open the browser, and leave it running:

```sh
ssh -i ~/.ssh/imoki-lab -N \
  -L 127.0.0.1:18080:127.0.0.1:8080 ubuntu@141.95.112.190
```

Open **http://localhost:18080** and use the owner login saved locally in
`infra/production/.local/access.txt`. The password is unchanged. SSH encrypts
the connection between your computer and the VPS. Stop the tunnel with Ctrl+C.
The private URL works only while the tunnel is running. Use `localhost`, as the
configured application origin and write-origin checks expect that hostname.

`CIRCULAR_APP_ORIGIN=http://localhost:18080` configures the app's links, provider
callbacks, CORS, and MCP addresses. The private console is built with an empty
`VITE_API_URL` so API requests use the same origin. If the tunnel's local port
changes, update the app origin and restart the trusted services too.

## Files and builds

The deployment directory is `/opt/circular`. It contains `compose.yaml`,
`Caddyfile`, a private `.env`, root-owned `owner.users`, public static files in
`vitrine/`, the private console build in `web/`, and persistent app files in
`state/`. Docker volumes hold PostgreSQL and Caddy's certificates. Keep the
parent directory private. Backups need both the database and state; encrypted
provider credentials require the original `CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY`.

Build the vitrine with `node apps/vitrine/build.mjs`; deploy only its `dist/`
output into `vitrine/`. Its build allowlist excludes application code and data.
Build the console with `VITE_API_URL=''` and the existing web build command;
deploy that separate output into `web/`. Preserve the package lockfile.

Build server images using `infra/go.Dockerfile` and assign an immutable release
tag. Keep the Codex and fake runner images installed on the VPS. Set the
release, public domain, app origin, absolute host state root, preserved provider
configuration, integration encryption key, strong database password, and tested
Caddy image digest in the private `.env`. Database passwords must contain only
URL-safe characters. The state root must match the host path mounted at the
worker's `/var/lib/circular`.

`CIRCULAR_API_RELEASE` and `CIRCULAR_MIGRATE_RELEASE` optionally override the
shared `CIRCULAR_RELEASE` for scoped upgrades. After a schema upgrade, retain a
compatible migrator even if the API is rolled back. Replace the Compose
`migrate` service with the new image; an obsolete completed container may be
started again by dependency handling. Use `docker start` to resume unchanged
worker/webhook containers without restarting their dependencies.

The owner password is hashed with `caddy hash-password` through stdin, then
stored as `owner <hash>` in `owner.users`. Set `.env` and `owner.users` to 0600,
with `owner.users` owned by root. Make `Caddyfile` and both frontend builds
readable by the gateway (0644 files and 0755 directories). Plaintext owner
credentials remain in the ignored local directory, excluded from Docker builds.

## Verify and operate

With the SSH tunnel running:

```sh
python3 infra/production/verify.py https://circular.ruohao.dev \
  --credentials infra/production/.local/owner-auth.json
```

The checks verify a public vitrine with no login, absent public API/MCP/console
routes even with valid credentials, private app access through the tunnel,
rejected cross-origin writes, and isolated signed webhook receiver routes.
No model runs or provider messages are created.

Operational commands run in `/opt/circular` on the VPS:

```sh
sudo docker compose ps --all
sudo docker compose logs --tail 50 api worker gateway
sudo docker compose exec -T postgres pg_dump -U circular -d circular -Fc > backup.dump
sudo docker compose run --rm --no-deps worker circular-worker-go --check
```

Before an upgrade, back up the database and state. Restart the gateway after
configuration changes (`admin off` disables its reload API). The production
DNS record is Porkbun A host `circular`, answer `141.95.112.190`. Public ports
80 and 443 serve the vitrine; private port 8080 must remain bound to loopback.
Caddy retains and renews certificates in its data volume.

## Task ownership upgrade (revision 0019)

This migration preserves historical Tasks, Runs, Events, schedules, Workspaces
and delegation records. It creates no synthetic sessions or historical contact.
It must not run beside old writers: an old API can create coding Runs without
the required session, which new workers deliberately refuse to execute.

1. Back up database and state. Prevent new coding launches from the console, MCP
   clients and automatic Linear routes. Keep the old workers available to drain.
2. Finish or cancel all coding Runs, including suspended parents and their
   children, through their existing lifecycle. Let workers finish cleanup.
3. Confirm the following query returns no rows. A terminal status alone is not
   enough: worker ownership, lease metadata and unreleased Workspaces also block
   migration.

   ```sql
   SELECT r.id, r.status, r.worker_id, r.lease_expires_at
   FROM runs r
   WHERE r.kind = 'coding'
     AND (
       r.status NOT IN ('succeeded', 'failed', 'cancelled')
       OR r.worker_id IS NOT NULL
       OR r.lease_expires_at IS NOT NULL
       OR EXISTS (
         SELECT 1 FROM workspaces w
         WHERE w.run_id = r.id AND w.status <> 'released'
       )
     );
   ```

4. Stop API/background consumers, workers and other writers. Install the new
   release and run its migrator with writers stopped. Keep the gateway's current
   public vitrine/private app separation.
5. Start only new API/worker/consumer binaries and the matching console. Verify
   private health and migration revision before allowing new launches. Mixed
   old/new writers and rolling back only the API/worker are unsupported.

If coding work is not drained, the migration fails transactionally with:

> Drain coding Runs and finish worker/Workspace cleanup before upgrading; stop old writers and retry

The old schema and records remain intact. Resolve cleanup using the old release
and retry the coordinated upgrade. Never force terminal status, erase a lease or
delete resource records to bypass the guard. PR reviews do not acquire coding
Task claims; the guard excludes them, but all writers must still stop for upgrade.
After upgrade, interrupted native coding work retains Task ownership even after
recovery cleanup. A user must explicitly resolve that hold before a new attempt.

Never run `docker compose down -v`. Early migration backups contain the previous
public-console gateway: restore data and images selectively while preserving
the current public/private separation. Stop VPS writers before restoring an
older database, and preserve any new data first. See the deployment record in
`docs/development/vps-deployment.md` for snapshot locations.
