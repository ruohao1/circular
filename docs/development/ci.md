# Go-only CI

The [CI workflow](../../.github/workflows/ci.yml) runs three independent jobs on
GitHub-hosted Ubuntu 24.04 runners for pull requests, pushes to `main`, and manual runs.

- **Go / PostgreSQL / Docker** checks the Go-only source guard, formatting, module
  checksums/tidy state, vet/build, and the full race suite. `TEST_DATABASE_URL` and
  `CIRCULAR_RUN_DOCKER_TESTS=1` are explicitly set so integration tests execute.
- **Frontend / contracts** installs from the pnpm lockfile, checks generated OpenAPI
  types, typechecks, runs frontend tests, and builds the web app.
- **Browser / local and Compose** runs success, cancellation, and failure against
  the native local Go stack, then builds and starts the default Compose deployment
  and repeats those scenarios after both HTTP endpoints are ready. It rejects
  focused tests and retains failure reports and traces for seven days.

The Go version comes from `go.mod` through
[setup-go's version-file support](https://github.com/actions/setup-go#usage).
Node 22 matches the web image; Corepack uses the exact pnpm version in `package.json`.
The workflow installs Chromium and its system dependencies as described in the
[Playwright CI guide](https://playwright.dev/docs/ci-intro).

## Isolation and permissions

The workflow uses `pull_request`, not privileged `pull_request_target`, and needs
only `contents: read`. Action revisions are pinned to full commit SHAs and checkout
does not persist its Git credentials. No application, deployment, or integration
secrets are supplied; the database credentials are disposable fixture values.

Each database test and local browser stack owns a random schema. The PostgreSQL
service uses the standard [GitHub service-container pattern](https://docs.github.com/en/actions/tutorials/use-containerized-services/create-postgresql-service-containers).
The Compose browser pass owns a separate `circular-ci` project, database volume,
and `mktemp` execution root. Its ports are loopback-only and its PostgreSQL port is
5433 to avoid the service container on 5432. Cleanup runs even after a failed step
and targets only that project, never unrelated Docker resources. The hosted runner
is disposable if a job is forcibly terminated.

Run containers retain the same isolation policy as development: non-root, no network,
one Run worktree mount, no Docker socket, and no control-plane credentials. The
trusted worker alone receives the socket in the Compose deployment.

## Local reproduction

Use the [README verification commands](../../README.md#verification) with an explicitly
configured disposable PostgreSQL database. For browser report output matching CI:

```bash
PLAYWRIGHT_HTML_OUTPUT_DIR=playwright-report/local PLAYWRIGHT_HTML_OPEN=never \
  corepack pnpm test:e2e --forbid-only --reporter=line,html --output=test-results/local
```

Compose testing is optional locally. Use a unique `COMPOSE_PROJECT_NAME`, a newly
created `CIRCULAR_EXECUTION_HOST_ROOT`, and the loopback port overrides in
`.github/compose.ci.yaml`. Never aim a cleanup command at a development or production
stack. Set `CIRCULAR_E2E_COMPOSE=1` when running the browser suite against that stack.

Adding the workflow does not change repository branch protection, deploy software,
push commits, or prove that a hosted run has passed. The first hosted results become
available after the commit is pushed. If required checks are desired, configure the
three job names above in the repository's rules after that first run.
