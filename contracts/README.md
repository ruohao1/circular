# API contracts

`openapi.json` is the authoritative HTTP contract. The Go API embeds and serves the
exact document at `/openapi.json`; interactive documentation is at `/docs`.

Edit the contract first when changing the public interface, update the Go handler and
its HTTP integration tests, then run:

```bash
corepack pnpm contracts:generate
corepack pnpm contracts:check
```

Commit the contract together with `apps/web/src/generated/api.ts`. Generation needs
Node/pnpm only and does not connect to PostgreSQL or start a service. The Go HTTP tests
verify behavior, status codes, data shapes, replay, and artifact boundaries.
