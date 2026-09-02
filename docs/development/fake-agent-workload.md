# Fake agent workload process

The fake agent workload is a deterministic process and container image for exercising the
future Run → container → worktree → backend execution path. It is not a control-plane
process, a fourth Compose service, or the in-worker `FakeAgentBackend`. Workspace
provisioning now starts the configured runner image with this version-1 request; output
ingestion and replacement of the temporary in-worker fake execution path remain separate.

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

The event-ingestion slice will translate these records into `EventEnvelope` values and
preserve the raw lines. That integration is intentionally separate from provisioning.

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
