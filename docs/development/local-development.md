---
title: Local development
description: Run Circular from source and verify frontend, API, worker, and browser changes.
---

Circular uses Go for its API, worker, migrations, and workload processes. The
console uses React, TypeScript, and Vite. Python is not required to build or run
the project.

## Prerequisites

Use Go 1.27.1 or later, Node.js 22 with Corepack/pnpm, Git, PostgreSQL, and Docker.
The repository's `package.json` pins the pnpm version.

For a fresh checkout, copy `.env.example` to `.env` and configure `DATABASE_URL`
for your development database. Then install dependencies, apply migrations, and
build the simulated workload image:

```bash
corepack pnpm install --frozen-lockfile
go run ./cmd/circular-migrate
docker build -f infra/fake-agent-workload.Dockerfile -t circular-runner:dev .
```

## Start the services

Run each command in its own terminal:

```bash
go run ./cmd/circular-api
go run ./cmd/circular-worker-go
corepack pnpm dev
```

Open the console at `http://localhost:5173`, its wiki at
`http://localhost:5173/docs`, and the interactive API documentation at
`http://localhost:8000/docs`.

`go run ./cmd/circular-worker-go --check` validates worker configuration without
claiming runs or connecting to PostgreSQL or Docker. It is a configuration check,
not a service health check.

For containers instead of separate processes, use the
[README setup instructions](../../README.md). Configure real model execution
using the [Codex backend guide](codex-backend.md).

## Verification

Run the fast checks from the repository root. Database and Docker scenarios skip
unless explicitly enabled:

```bash
go test -race ./...
go vet ./...
go build ./...
corepack pnpm contracts:check
corepack pnpm typecheck
corepack pnpm test
corepack pnpm build
```

For complete integration coverage, point the tests at a disposable PostgreSQL
database:

```bash
export TEST_DATABASE_URL=postgresql://circular:circular@localhost:5432/circular_test
CIRCULAR_RUN_DOCKER_TESTS=1 go test -race ./... -count=1 -timeout=300s
corepack pnpm exec playwright install chromium
corepack pnpm test:e2e
```

Database tests create and remove their own random schemas using the production
migrations. Browser tests build an isolated API, worker, and workload image and
exercise the console against that stack. Automated execution uses simulated
workloads and does not call a model provider.

To test an already-running disposable Compose stack, set
`CIRCULAR_E2E_COMPOSE=1` and `CIRCULAR_EXECUTION_HOST_ROOT` to that stack's absolute
host root before running the browser suite. This mode leaves its records for
inspection and does not reset the database. See
[CI and local reproduction](ci.md) for matching CI output.

## API contracts

`contracts/openapi.json` is the authoritative HTTP contract. The Go API serves
that document, and the TypeScript client is generated from it. After changing a
contract, regenerate the client:

```bash
corepack pnpm contracts:generate
```

Keep both the contract and generated client in the change. HTTP integration
tests verify implementation behavior as well as the types.

## Editing the wiki

The Fumadocs user guide is part of the web app at `/docs`. Only content in
`docs/user-guide/` is published and indexed. Keep architecture, API internals,
build instructions, CI and contributor references in the other `docs/` folders.
Write published guides around user tasks and visible console labels.

Add a `.md` or `.mdx` file in `docs/user-guide/` with `title` and
`description` frontmatter, then list its filename without the extension in the
folder's `meta.json` to control navigation order. `index` is the folder's home page.

Link to other pages using `/docs/path-without-extension`. Markdown supports
tables, code blocks, and heading anchors; MDX also supports Fumadocs components
such as `Cards`, `Card`, and `Callout`. Pages load on demand, and search indexes
their titles, headings, and body text locally in the browser.

Run `corepack pnpm dev` to preview changes and `corepack pnpm build` to validate
the compiled content. After installing Playwright's Chromium, run the docs-only
browser checks without a database or worker:

```bash
corepack pnpm exec playwright test --config=playwright.docs.config.ts
```

The Docker web image includes this content. Rebuild the web service to publish
documentation changes to a Compose installation.

## Further reading

- [Architectural foundation](../architecture/foundation.md)
- [Console components](ui-components.md)
- [Managed execution directories](execution-directories.md)
- [Go backend upgrades](go-migration.md)
