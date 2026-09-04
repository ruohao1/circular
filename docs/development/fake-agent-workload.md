# Fake agent workload process

The fake agent workload is a deterministic process and container image for exercising the
Run → container → worktree → backend execution path. It is not a control-plane process or
a persistent Compose service. Workspace provisioning starts the configured runner image with
this version-1 request, and the worker consumes its output through runtime event ingestion.

Its only interface is one UTF-8 encoded JSON object on standard input, UTF-8 JSON Lines on
standard output and standard error, and the process exit code. The byte encoding is explicit
and does not change with `PYTHONIOENCODING`. The process reads no environment-based
configuration and does not need or accept a database URL, platform credentials, or the
control-plane source tree.

## Version 1 input

Close standard input after writing exactly one object:

```json
{
  "protocol_version": 1,
  "run": {
    "id": "00000000-0000-4000-8000-000000000170",
    "task_title": "Add health endpoint",
    "task_description": "Return a stable response.",
    "instructions": "Work carefully."
  },
  "behavior": {
    "delay_ms": 0,
    "failure": "none"
  }
}
```

Every object has an exact field set; unknown, missing, and duplicate fields are rejected.
`run.id` is a canonical UUID, `task_title` is non-empty, and the other Run text fields are
strings. Every Run text field must be valid Unicode text that can be encoded as UTF-8;
lone UTF-16 surrogates are rejected before any output is emitted. `delay_ms` is an integer
from `0` through `10000` and is applied before every emitted event. `failure` is one of:

- `none`: emit the complete success stream;
- `before_events`: fail before writing an event;
- `after_first_event`: emit one delta, then fail.

## Version 1 output

Success writes four canonical JSON Lines records to standard output in this order:

1. `agent.message.delta` containing the fixed completion prefix;
2. `agent.message.delta` containing the task title;
3. `agent.message.completed` containing the combined message;
4. `usage.updated` containing deterministic word-count usage.

Each event contains exactly `protocol_version`, `run_id`, `source`, `type`, and `data`.
There are deliberately no generated IDs or timestamps, so identical input produces
identical output. JSON strings are ASCII-escaped and records are written directly as UTF-8
bytes. Every record is flushed before the next delay or injected failure.

Invalid input writes one record to standard error with code `invalid_input` and exits `2`.
Malformed UTF-8 is invalid input and never produces a Python traceback. Validation messages
identify field names but never echo field values. An injected failure writes one
`injected_failure` record to standard error and exits `20`; its record includes the Run ID.
Success leaves standard error empty and exits `0`.

The runner-side ingestion adapter translates these records into backend-neutral
`EventEnvelope` values and preserves each complete decoded wire object in `raw`.

## Worker event ingestion

`FakeBackendEventStream` consumes the backend-neutral `Runtime.output()` iterator. Runtime
chunks are transport details: one record may span chunks (including inside a multibyte UTF-8
character), one chunk may contain several records, and stdout and stderr keep independent
partial-line buffers. A record is accepted only after its terminating newline. Each line is
bounded to 1 MiB before decoding so an unterminated or oversized record cannot grow a buffer
without limit.

Version 1 stdout events use exact field sets and the canonical Run ID and source. The adapter
normalizes `agent.message.delta`, `agent.message.completed`, and `usage.updated`; unknown event
types are execution failures rather than silently dropped facts. It rejects malformed UTF-8,
duplicate JSON fields, non-standard constants such as `NaN`, numeric values that decode as
non-finite, lone Unicode surrogates, unknown versions, and invalid type-specific data. The
normalized `data` and the complete decoded wire object remain separately available on the
envelope.

Version 1 stderr records are errors, not normalized events. The two workload-defined shapes are
supported: `invalid_input` without a Run ID and `injected_failure` with the matching canonical
Run ID. A valid record raises `BackendReportedError` carrying its decoded raw object. Malformed
stderr, an unexplained nonzero exit, a stopped execution, output observation failure, and result
observation failure have distinct typed errors. Synchronous output-iterator acquisition and
iterator cleanup failures are output observation failures too; ordinary output failures still
wait for the terminal process result, while cancellation propagates unchanged. A protocol or
backend-reported failure observed before process completion takes precedence over output cleanup
and the exit status.

`RuntimeEventIngestor` commits every normalized event in its own transaction. PostgreSQL's
per-Run lock remains the sequence allocator, so concurrent writers retain one contiguous event
sequence. Because each record commits before the next output chunk is requested, the existing
polling event reader and SSE endpoint can expose deltas while the backend is still running. A
later protocol, process, or persistence failure does not roll back prior event commits.

`RunExecutor.execute_runtime()` is the post-provisioning coordinator for an already-running Run.
One database read verifies the Run is `running`, its Workspace is `ready`, and the Workspace's
immutable container identity matches the exact live handle before output is consumed. It then
invokes the ingestor and performs the existing `running` to `finalizing` to `succeeded`
transitions. Invalid caller preconditions do not mutate lifecycle state; operational failures
use the terminal path. A backend-reported error, or a valid decoded event rejected by version,
identity, source, type, or data schema, attaches its raw object to `run.failed`; malformed bytes
and ambiguous duplicate-key documents do not. Run state and `run.failed` use the same
database-safe error projection, capped at 4,000 characters. If recording that failure also
fails, the original execution error remains primary and, when possible, receives only the
secondary exception type as a sanitized note. Cancellation and release remain separate
lifecycle concerns owned by `RunSupervisor`.

The worker passes `--write-output` to the workload. After validating the request, the
workload creates `circular-result-<Run UUID>.txt` in its working directory without
overwriting existing files. This gives finalization a real untracked file to capture.
The flag is optional for standalone protocol tests. A `before_events` failure does not
create output; an `after_first_event` failure retains its partial output.

Agent `backend_config` can set `delay_ms` and `failure` using the values above. Other
configuration is not passed through as environment variables or Docker settings. By
default, the worker derives the delay from `CIRCULAR_FAKE_DELAY_SECONDS`.

## Build and run

Build the dedicated image target:

```bash
docker build \
  --file infra/fake-agent-workload.Dockerfile \
  --tag circular-fake-agent-workload:dev \
  .
```

The image copies only the workload package. A hardened smoke invocation needs no host
mounts or network access:

```bash
printf '%s\n' '{"protocol_version":1,"run":{"id":"00000000-0000-4000-8000-000000000170","task_title":"Add health endpoint","task_description":"Return a stable response.","instructions":"Work carefully."},"behavior":{"delay_ms":0,"failure":"none"}}' \
  | docker run --rm --interactive \
      --network none \
      --read-only \
      --cap-drop ALL \
      --security-opt no-new-privileges \
      circular-fake-agent-workload:dev
```

The real image smoke test is opt-in because it builds and starts Docker resources:

```bash
CIRCULAR_RUN_DOCKER_TESTS=1 uv run pytest -q tests/test_fake_agent_workload_image.py
```
