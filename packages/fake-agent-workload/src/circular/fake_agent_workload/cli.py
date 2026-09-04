import json
import os
import sys
import time
from dataclasses import dataclass
from enum import StrEnum
from typing import Any, BinaryIO
from uuid import UUID

PROTOCOL_VERSION = 1
SOURCE = "fake-container-workload"
INVALID_INPUT_EXIT = 2
INJECTED_FAILURE_EXIT = 20


class InvalidInput(ValueError):
    """A safe validation error whose message never contains an input value."""


class DuplicateField(ValueError):
    def __init__(self, field: str) -> None:
        super().__init__(field)
        self.field = field


class FailureMode(StrEnum):
    NONE = "none"
    BEFORE_EVENTS = "before_events"
    AFTER_FIRST_EVENT = "after_first_event"


@dataclass(frozen=True, slots=True)
class RunContext:
    id: UUID
    task_title: str
    task_description: str
    instructions: str


@dataclass(frozen=True, slots=True)
class Behavior:
    delay_seconds: float
    failure: FailureMode


@dataclass(frozen=True, slots=True)
class WorkloadRequest:
    run: RunContext
    behavior: Behavior


def _write_json_line(stream: BinaryIO, payload: dict[str, Any]) -> None:
    line = json.dumps(
        payload,
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    stream.write(line + b"\n")
    stream.flush()


def _event(run_id: UUID, event_type: str, data: dict[str, Any]) -> dict[str, Any]:
    return {
        "protocol_version": PROTOCOL_VERSION,
        "run_id": str(run_id),
        "type": event_type,
        "source": SOURCE,
        "data": data,
    }


def _error(code: str, message: str, run_id: UUID | None = None) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "protocol_version": PROTOCOL_VERSION,
        "error": {"code": code, "message": message},
    }
    if run_id is not None:
        payload["run_id"] = str(run_id)
    return payload


def _object_without_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for field, value in pairs:
        if field in result:
            raise DuplicateField(field)
        result[field] = value
    return result


def _validate_fields(document: dict[str, Any], scope: str, required: set[str]) -> None:
    unsupported = sorted(set(document) - required)
    if unsupported:
        raise InvalidInput(f"{scope} contains unsupported fields: {', '.join(unsupported)}")
    missing = sorted(required - set(document))
    if missing:
        raise InvalidInput(f"{scope} is missing required fields: {', '.join(missing)}")


def _parse_run(value: Any) -> RunContext:
    if not isinstance(value, dict):
        raise InvalidInput("run must be an object")
    _validate_fields(
        value,
        "run",
        {"id", "task_title", "task_description", "instructions"},
    )

    raw_run_id = value["id"]
    try:
        run_id = UUID(raw_run_id) if isinstance(raw_run_id, str) else None
    except ValueError:
        run_id = None
    if run_id is None or str(run_id) != raw_run_id:
        raise InvalidInput("run.id must be a canonical UUID")

    task_title = value["task_title"]
    if not isinstance(task_title, str) or not task_title.strip():
        raise InvalidInput("run.task_title must be a non-empty string")
    for field in ("task_description", "instructions"):
        if not isinstance(value[field], str):
            raise InvalidInput(f"run.{field} must be a string")
    for field in ("task_title", "task_description", "instructions"):
        try:
            value[field].encode("utf-8")
        except UnicodeEncodeError:
            raise InvalidInput(f"run.{field} must be valid Unicode text") from None

    return RunContext(
        id=run_id,
        task_title=task_title,
        task_description=value["task_description"],
        instructions=value["instructions"],
    )


def _parse_behavior(value: Any) -> Behavior:
    if not isinstance(value, dict):
        raise InvalidInput("behavior must be an object")
    _validate_fields(value, "behavior", {"delay_ms", "failure"})

    delay_ms = value["delay_ms"]
    if type(delay_ms) is not int or not 0 <= delay_ms <= 10_000:
        raise InvalidInput("behavior.delay_ms must be an integer from 0 through 10000")

    raw_failure = value["failure"]
    try:
        failure = FailureMode(raw_failure) if isinstance(raw_failure, str) else None
    except ValueError:
        failure = None
    if failure is None:
        raise InvalidInput(
            "behavior.failure must be one of: none, before_events, after_first_event"
        )
    return Behavior(delay_seconds=delay_ms / 1000, failure=failure)


def _read_request(stream: BinaryIO) -> WorkloadRequest:
    try:
        raw_input = stream.read().decode("utf-8")
    except UnicodeDecodeError:
        raise InvalidInput("stdin must be valid UTF-8 JSON") from None

    try:
        document = json.loads(raw_input, object_pairs_hook=_object_without_duplicates)
    except DuplicateField as error:
        raise InvalidInput(f"input contains duplicate field: {error.field}") from None
    except json.JSONDecodeError:
        raise InvalidInput("stdin must contain one JSON object") from None

    if not isinstance(document, dict):
        raise InvalidInput("stdin must contain one JSON object")
    _validate_fields(document, "input", {"protocol_version", "run", "behavior"})

    protocol_version = document["protocol_version"]
    if type(protocol_version) is not int or protocol_version != PROTOCOL_VERSION:
        raise InvalidInput("unsupported protocol_version: expected 1")
    return WorkloadRequest(
        run=_parse_run(document["run"]),
        behavior=_parse_behavior(document["behavior"]),
    )


def _events(request: WorkloadRequest) -> tuple[dict[str, Any], ...]:
    run = request.run
    content = f"Fake container workload completed: {run.task_title}"
    input_tokens = sum(
        len(value.split()) for value in (run.task_title, run.task_description, run.instructions)
    )
    return (
        _event(
            run.id,
            "agent.message.delta",
            {"delta": "Fake container workload completed: "},
        ),
        _event(run.id, "agent.message.delta", {"delta": run.task_title}),
        _event(run.id, "agent.message.completed", {"content": content}),
        _event(
            run.id,
            "usage.updated",
            {"input_tokens": input_tokens, "output_tokens": len(content.split())},
        ),
    )


def _run(
    request: WorkloadRequest, stdout: BinaryIO, stderr: BinaryIO, *, write_output: bool = False
) -> int:
    run_id = request.run.id
    behavior = request.behavior
    if behavior.failure is FailureMode.BEFORE_EVENTS:
        _write_json_line(
            stderr,
            _error(
                "injected_failure",
                "injected failure before emitting events",
                run_id,
            ),
        )
        return INJECTED_FAILURE_EXIT

    if write_output:
        name = f"circular-result-{run_id}.txt"
        descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write(f"Fake container workload completed: {request.run.task_title}\n")
    for index, event in enumerate(_events(request)):
        time.sleep(behavior.delay_seconds)
        _write_json_line(stdout, event)
        if behavior.failure is FailureMode.AFTER_FIRST_EVENT and index == 0:
            _write_json_line(
                stderr,
                _error(
                    "injected_failure",
                    "injected failure after first event",
                    run_id,
                ),
            )
            return INJECTED_FAILURE_EXIT
    return 0


def main(
    stdin: BinaryIO | None = None,
    stdout: BinaryIO | None = None,
    stderr: BinaryIO | None = None,
    *,
    write_output: bool = False,
) -> int:
    input_stream = sys.stdin.buffer if stdin is None else stdin
    output_stream = sys.stdout.buffer if stdout is None else stdout
    error_stream = sys.stderr.buffer if stderr is None else stderr
    try:
        request = _read_request(input_stream)
    except InvalidInput as error:
        _write_json_line(error_stream, _error("invalid_input", str(error)))
        return INVALID_INPUT_EXIT
    return _run(request, output_stream, error_stream, write_output=write_output)
