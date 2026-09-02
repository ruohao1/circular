# Docker runtime adapter

`DockerRuntime` is the execution-isolation adapter behind the shared `Runtime` interface.
It controls Docker; it does not implement an agent reasoning loop, normalize backend
events, provision Git worktrees, or decide Run lifecycle transitions. The worker composes
it behind workspace provisioning and passes its exact live handle to event ingestion.

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

Immediately after `docker create` returns, the adapter inspects the immutable full
container ID before invoking `docker start`. The effective configuration must match the
resolved plan exactly: one read-write bind at `/workspace`, the network and root-filesystem
settings, capability and security options, CPU and memory limits, user, working directory,
restart policy, and the complete reserved `io.circular.*` label set. Missing, changed, or
additional Circular labels fail closed; benign image metadata outside that namespace,
including OCI labels, is portable and allowed. Images remain trusted runtime inputs whose
`VOLUME` declarations must not expand the resolved mount policy; an image-declared volume
makes creation fail closed before its entrypoint can run.

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

The returned `ContainerHandle` deliberately has two identities. Its adapter-local `id`
routes live `output`, `wait`, and `stop` calls, while `resource_id` is the verified,
immutable full Docker ID that workspace provisioning persists for later ownership checks
and cleanup. Live consumers retain the original complete handle; they do not reconstruct
one from the persisted resource ID. Every live operation compares both fields, so a
same-name replacement or a forged resource identity is rejected.

Chunk boundaries are transport details and may not align with backend event records. Output
is a one-consumer stream; it remains available after fast completion, and `wait()` never
depends on the caller consuming it. `RuntimeEventIngestor` consumes this stream while a Run is
active and bounds each backend JSON Line to 1 MiB, but the runtime's MVP queue remains
intentionally unbounded so the adapter can always drain child pipes. Production-scale output
still requires bounded backpressure or durable spooling between the pipe pump and consumer.

`wait()` is cancellation-shielded and returns one stable result. `stop()` is bounded,
idempotent, shared by concurrent callers, and completes the owned stop operation before
propagating caller cancellation. Every create attempt adds a cryptographically random,
ephemeral nonce label that is not part of the stable resolved plan. If create times out, is
cancelled, or returns an invalid identity, the adapter first scans all containers for that
exact nonce, then validates the immutable full ID, nonce, and deterministic name. Ambiguous
outcomes are rechecked through a bounded settle window so a daemon commit arriving after the
client was killed is still found and removed. The attempt is cleared as absent only after a
final empty nonce scan and exact-name listing; an unavailable or inconsistent check retains
the attempt for the next same-instance reconciliation. A foreign or same-name replacement
is never started, stopped, or removed. Cleanup is cancellation-safe; cancellation during
retained-attempt removal completes and records cleanup before being re-raised, and a cleanup
failure retains the original failure as its cause. Rejection cleanup also removes anonymous
volumes created from image declarations. Every post-create policy inspect, start, stop, kill,
and cleanup operation targets the immutable ID.

`discard()` is a narrower operation than general Workspace cleanup. It exists only to
compensate a container that was created but could not be durably handed off: it finishes
termination and permanently removes that exact immutable resource, including anonymous
volumes. The operation is cancellation-safe and shared by concurrent callers. After it
succeeds, live operations reject the released handle, repeated discard is a tombstoned
no-op, and a later `start()` may create a fresh allocation rather than reuse the removed
execution. Failure is typed and prominent because the caller then has neither a durable
identity nor proof of release.

Calling `start()` repeatedly with the exact same resolved launch is idempotent only within
one adapter instance. A changed stdin or environment value is an in-process conflict even
though those confidential values are excluded from labels. Any deterministic name already
present in Docker is a typed conflict and is never adopted, stopped, or removed. Crash
recovery, reconciliation, and cleanup of durably recorded containers belong to the later
workspace cleanup slice; `discard()` does not implement that lifecycle.

Startup reservations are per deterministic Run name. Concurrent calls for one Run remain
serialized for idempotence, while unrelated Runs can create and verify containers in
parallel.

Docker daemon and CLI details are removed from raised errors; raw stderr, argv, stdin, and
environment values are never included.

## Verification

Pure policy and CLI-boundary tests run in the normal suite. The real Docker security smoke
builds the isolated fake workload image, launches it through `DockerRuntime`, inspects the
resulting container policy, and removes only the uniquely labeled test container. It also
builds a hostile fixture image with an extra `VOLUME` and a host-visible entrypoint marker,
proving the policy gate removes the created container without ever starting it:

```bash
CIRCULAR_RUN_DOCKER_TESTS=1 uv run pytest -q tests/test_docker_runtime_image.py
```
