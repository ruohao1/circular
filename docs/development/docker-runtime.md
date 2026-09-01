# Docker runtime adapter

`DockerRuntime` is the execution-isolation adapter behind the shared `Runtime` interface.
It controls Docker; it does not implement an agent reasoning loop, normalize backend
events, provision Git worktrees, or decide Run lifecycle transitions. Worker composition
and Docker socket installation are deliberately deferred.

## Resolved policy

Every `ContainerSpec` identifies one Run with a UUID and carries the exact one-shot bytes
to write to standard input before EOF. `DockerRuntime.resolve()` validates the request and
returns a frozen, side-effect-free `DockerContainerPlan` for inspection before launch. The
plan contains no environment values and hides stdin from its representation.

The adapter enforces:

- a deterministic `circular-run-<UUID hex>` name and managed, Run, and policy labels;
- exactly one read-write bind mount from `<trusted Docker host root>/<Run UUID>` to
  `/workspace`;
- `/workspace` as the working directory and `65532:65532` as the default trusted,
  constructor-owned non-root identity;
- a read-only root filesystem, `--cap-drop ALL`, `no-new-privileges`, and restart policy
  `no`;
- explicit CPU and memory limits;
- network mode `none` by default, or explicit `bridge` only when the caller enables it;
- argv-only process creation without a shell.

The spec cannot add mounts, change the container user or working directory, or inject
Docker CLI flags through its image, command, path, or environment fields. A Docker-host
path is accepted only when it is the direct canonical UUID child matching the Run. The
path need not exist in the worker namespace because a worker container and its host Docker
daemon can have different filesystem views. Existing local symlinks and paths outside the
trusted root are rejected.

This one-mount policy intentionally does not expose the Repository cache. A linked Git
worktree stores a `.git` file that points into that cache, so Git metadata is not usable
inside the initial Run container even though ordinary worktree files are available. A
later, dedicated isolation slice must design narrowly scoped Git metadata access; mounting
the whole shared Repository cache would violate this adapter's boundary.

The bind is configured read-write, but host ownership still governs actual writes. Worker
composition must provision the Run worktree for the trusted container UID (or supply an
equivalent user-namespace mapping); the runtime does not fall back to root when ownership
is incompatible.

## Environment policy

The environment-name allowlist defaults to empty. Names that control the Docker client or
its process—such as `PATH`, `HOME`, `DOCKER_*`, `SSH_*`, proxy, loader, certificate,
runtime, database, and `CIRCULAR_*` variables—cannot be allowlisted. Values are snapshotted
before the first asynchronous operation.

For an explicitly allowed name, the value is present only in the minimal Docker `create`
client environment and Docker receives `--env NAME`; the value never appears in argv, the
resolved plan, persisted labels, or an adapter error. Ambient worker variables are not
copied. This is a transport boundary, not the future scoped-secret lifecycle: the default
remains no environment, and the worker must not place platform credentials in a spec.

## Lifecycle and observation

The adapter runs `docker create --interactive`, followed by
`docker start --attach --interactive`. It drains stdout and stderr immediately into one
stream in the order the adapter observes chunks, writes stdin once, and closes it. Input
delivery and EOF are bounded by the Docker operation timeout. `start()` does not expose a
handle until inspection proves the immutable container is running or already exited. After
the attached client finishes, inspection of that same immutable identity supplies the
authoritative exit status; a Docker `wait` process is not involved.

Chunk boundaries are transport details and may not align with backend event records. Output
is a one-consumer stream; it remains available after fast completion, and `wait()` never
depends on the caller consuming it. The MVP queue is intentionally unbounded in memory so
the adapter can always drain child pipes even when no caller consumes output. The immediate
event-ingestion and budget slice must consume it while a Run is active and define bounded
backpressure or durable spooling before production-scale output is accepted.

`wait()` is cancellation-shielded and returns one stable result. `stop()` is bounded,
idempotent, shared by concurrent callers, and completes the owned stop operation before
propagating caller cancellation. Cancellation during `create` waits for an unambiguous
result and removes only a container that invocation created. Docker `create` output is
strictly validated as a full immutable container ID; every post-create inspect, start, stop,
kill, and cleanup operation targets that ID. The deterministic name is retained only for
creation, pre-create conflict detection, and public correlation, so later name reuse cannot
redirect an owned operation to a foreign container.

Calling `start()` repeatedly with the exact same resolved launch is idempotent only within
one adapter instance. A changed stdin or environment value is an in-process conflict even
though those confidential values are excluded from labels. Any deterministic name already
present in Docker is a typed conflict and is never adopted, stopped, or removed. Crash
recovery, reconciliation, and cleanup of retained containers belong to the later workspace
cleanup slice.

Docker daemon and CLI details are removed from raised errors; raw stderr, argv, stdin, and
environment values are never included.

## Verification

Pure policy and CLI-boundary tests run in the normal suite. The real Docker security smoke
builds the isolated fake workload image, launches it through `DockerRuntime`, inspects the
resulting container policy, and removes only the uniquely labeled test container:

```bash
CIRCULAR_RUN_DOCKER_TESTS=1 uv run pytest -q tests/test_docker_runtime_image.py
```
