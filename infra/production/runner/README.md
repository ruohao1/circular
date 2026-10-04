# Go and PostgreSQL workload runner

This packages the optional runner used for the ISQ-365 investigation. It keeps
the standard Codex workload and Node tools, adds Go 1.27.1 and PostgreSQL 17,
and installs `circular-go-test` for an isolated database inside the workload.
The helper never uses the Circular control database.

Build the two source-based parent images from the repository root, then build
the runner from this directory's context:

```sh
docker build -f infra/codex-agent-workload.Dockerfile -t circular-codex-runner:dev .
docker build -f infra/go.Dockerfile --target build -t circular-go-build:local .
docker build -f infra/production/runner/Dockerfile \
  -t circular-codex-runner:go-postgres-local infra/production/runner
```

Existing immutable parent images can be selected with
`--build-arg CIRCULAR_CODEX_RUNNER_IMAGE=...` and
`--build-arg CIRCULAR_GO_BUILD_IMAGE=...`. Select the resulting image explicitly
with the worker's `CIRCULAR_CODEX_IMAGE`; building it does not change a deployment.

The helper copies cached modules into an owned writable directory, starts
PostgreSQL on a private Unix socket, runs `go test` with the supplied arguments,
and removes its temporary database and caches on exit. The `module/` manifest
preserves the additional cache required by GitHub main at
`4d6bd4c43c9e080096b170851ff7c6aec9d9043e`; the parent build image supplies the
release's current module cache.

```sh
circular-go-test ./internal/postgres -count=1
```

The runtime remains UID/GID 65532, with no inherited PostgreSQL volume or server
entrypoint. It works with the normal read-only workload root and writable
workspace. `CGO_ENABLED=0` matches the deployed runner; race and Docker-daemon
checks belong in the external verification environment.
