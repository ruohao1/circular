import io
import json
import subprocess
import sys
import time
from typing import Any

import pytest
from circular.fake_agent_workload.cli import main as run_workload

RUN_ID = "00000000-0000-4000-8000-000000000170"


def _valid_input(**behavior_overrides: Any) -> dict[str, Any]:
    behavior = {"delay_ms": 0, "failure": "none"}
    behavior.update(behavior_overrides)
    return {
        "protocol_version": 1,
        "run": {
            "id": RUN_ID,
            "task_title": "Add health endpoint",
            "task_description": "Return a stable response.",
            "instructions": "Work carefully.",
        },
        "behavior": behavior,
    }


def _run_workload(document: object) -> subprocess.CompletedProcess[str]:
    return _run_raw_workload(json.dumps(document))


def _run_raw_workload(raw_input: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, "-m", "circular.fake_agent_workload"],
        input=raw_input,
        capture_output=True,
        check=False,
        text=True,
    )


def _input_with_unknown_surrogate_field() -> str:
    document = _valid_input()
    document["\ud800"] = "do-not-print"
    return json.dumps(document)


def _input_with_duplicate_surrogate_field() -> str:
    valid_input = json.dumps(_valid_input())
    return '{"\\ud800": 0, "\\ud800": 1, ' + valid_input[1:]


def test_success_emits_the_exact_versioned_backend_event_stream() -> None:
    result = _run_workload(_valid_input())

    assert result.returncode == 0
    assert result.stderr == ""
    assert result.stdout.splitlines() == [
        '{"data":{"delta":"Fake container workload completed: "},'
        f'"protocol_version":1,"run_id":"{RUN_ID}",'
        '"source":"fake-container-workload","type":"agent.message.delta"}',
        '{"data":{"delta":"Add health endpoint"},'
        f'"protocol_version":1,"run_id":"{RUN_ID}",'
        '"source":"fake-container-workload","type":"agent.message.delta"}',
        '{"data":{"content":"Fake container workload completed: Add health endpoint"},'
        f'"protocol_version":1,"run_id":"{RUN_ID}",'
        '"source":"fake-container-workload","type":"agent.message.completed"}',
        '{"data":{"input_tokens":9,"output_tokens":7},'
        f'"protocol_version":1,"run_id":"{RUN_ID}",'
        '"source":"fake-container-workload","type":"usage.updated"}',
    ]


def test_configured_delay_preserves_the_stream_and_delays_each_event() -> None:
    expected = _run_workload(_valid_input())

    started_at = time.monotonic()
    delayed = _run_workload(_valid_input(delay_ms=15))
    elapsed = time.monotonic() - started_at

    assert (delayed.returncode, delayed.stdout, delayed.stderr) == (
        expected.returncode,
        expected.stdout,
        expected.stderr,
    )
    assert elapsed >= 0.05


def test_injected_failure_before_events_has_an_exact_error_stream() -> None:
    result = _run_workload(_valid_input(failure="before_events"))

    assert result.returncode == 20
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"injected_failure",'
        '"message":"injected failure before emitting events"},'
        f'"protocol_version":1,"run_id":"{RUN_ID}"}}\n'
    )


def test_injected_failure_after_first_event_preserves_the_partial_stream() -> None:
    result = _run_workload(_valid_input(failure="after_first_event"))

    assert result.returncode == 20
    assert result.stdout == (
        '{"data":{"delta":"Fake container workload completed: "},'
        f'"protocol_version":1,"run_id":"{RUN_ID}",'
        '"source":"fake-container-workload","type":"agent.message.delta"}\n'
    )
    assert result.stderr == (
        '{"error":{"code":"injected_failure",'
        '"message":"injected failure after first event"},'
        f'"protocol_version":1,"run_id":"{RUN_ID}"}}\n'
    )


def test_malformed_input_reports_one_non_secret_versioned_error() -> None:
    result = _run_raw_workload('{"protocol_version":1,"secret":"do-not-print"')

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"stdin must contain one JSON object"},"protocol_version":1}\n'
    )


def test_platform_credentials_and_database_configuration_are_not_accepted() -> None:
    document = _valid_input()
    document["database_url"] = "postgresql://user:do-not-print@example.test/control"
    document["platform_credentials"] = {"token": "do-not-print"}

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"input contains unsupported fields: database_url, platform_credentials"},'
        '"protocol_version":1}\n'
    )


def test_unsupported_protocol_version_is_rejected() -> None:
    document = _valid_input()
    document["protocol_version"] = 2

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"unsupported protocol_version: expected 1"},"protocol_version":1}\n'
    )


def test_input_must_be_one_object() -> None:
    result = _run_workload([_valid_input()])

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"stdin must contain one JSON object"},"protocol_version":1}\n'
    )


def test_duplicate_json_fields_are_rejected() -> None:
    raw_input = json.dumps(_valid_input()).replace(
        '"protocol_version": 1',
        '"protocol_version": 1, "protocol_version": 1',
        1,
    )

    result = _run_raw_workload(raw_input)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"input contains duplicate field: protocol_version"},'
        '"protocol_version":1}\n'
    )


@pytest.mark.parametrize(
    ("raw_input", "message"),
    [
        (
            _input_with_unknown_surrogate_field(),
            r"input contains unsupported fields: \ud800",
        ),
        (
            _input_with_duplicate_surrogate_field(),
            r"input contains duplicate field: \ud800",
        ),
    ],
)
def test_surrogate_field_names_emit_safe_errors_with_strict_utf8_streams(
    monkeypatch: pytest.MonkeyPatch, raw_input: str, message: str
) -> None:
    stdout_bytes = io.BytesIO()
    stderr_bytes = io.BytesIO()
    monkeypatch.setattr(sys, "stdin", io.StringIO(raw_input))
    monkeypatch.setattr(
        sys,
        "stdout",
        io.TextIOWrapper(stdout_bytes, encoding="utf-8", errors="strict", write_through=True),
    )
    monkeypatch.setattr(
        sys,
        "stderr",
        io.TextIOWrapper(stderr_bytes, encoding="utf-8", errors="strict", write_through=True),
    )

    exit_code = run_workload()

    assert exit_code == 2
    assert stdout_bytes.getvalue() == b""
    assert stderr_bytes.getvalue().decode() == (
        f'{{"error":{{"code":"invalid_input","message":"{message}"}},"protocol_version":1}}\n'
    )


def test_run_context_rejects_platform_credentials_without_echoing_them() -> None:
    document = _valid_input()
    document["run"]["platform_credentials"] = {"token": "do-not-print"}

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"run contains unsupported fields: platform_credentials"},'
        '"protocol_version":1}\n'
    )


def test_missing_required_top_level_field_is_rejected() -> None:
    document = _valid_input()
    del document["run"]

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"input is missing required fields: run"},"protocol_version":1}\n'
    )


def test_run_context_must_be_an_object_without_echoing_its_value() -> None:
    document = _valid_input()
    document["run"] = "do-not-print"

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input","message":"run must be an object"},'
        '"protocol_version":1}\n'
    )


def test_run_context_requires_every_contract_field() -> None:
    document = _valid_input()
    del document["run"]["instructions"]

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"run is missing required fields: instructions"},'
        '"protocol_version":1}\n'
    )


def test_run_id_must_be_a_canonical_uuid() -> None:
    document = _valid_input()
    document["run"]["id"] = "../../do-not-print"

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"run.id must be a canonical UUID"},"protocol_version":1}\n'
    )


@pytest.mark.parametrize(
    ("field", "value", "message"),
    [
        ("task_title", "", "run.task_title must be a non-empty string"),
        ("task_description", {"secret": "do-not-print"}, "run.task_description must be a string"),
        ("instructions", None, "run.instructions must be a string"),
    ],
)
def test_run_text_fields_are_strictly_typed(field: str, value: object, message: str) -> None:
    document = _valid_input()
    document["run"][field] = value

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        f'{{"error":{{"code":"invalid_input","message":"{message}"}},"protocol_version":1}}\n'
    )


@pytest.mark.parametrize("field", ["task_title", "task_description", "instructions"])
def test_run_text_fields_reject_lone_surrogates_before_emitting_output(field: str) -> None:
    document = _valid_input()
    document["run"][field] = "\ud800"

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        f'{{"error":{{"code":"invalid_input",'
        f'"message":"run.{field} must be valid Unicode text"}},"protocol_version":1}}\n'
    )


def test_behavior_must_be_an_object_without_echoing_its_value() -> None:
    document = _valid_input()
    document["behavior"] = "do-not-print"

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input","message":"behavior must be an object"},'
        '"protocol_version":1}\n'
    )


def test_behavior_rejects_unsupported_fields() -> None:
    document = _valid_input()
    document["behavior"]["database_url"] = "do-not-print"

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"behavior contains unsupported fields: database_url"},'
        '"protocol_version":1}\n'
    )


def test_behavior_requires_delay_and_failure_fields() -> None:
    document = _valid_input()
    del document["behavior"]["failure"]

    result = _run_workload(document)

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"behavior is missing required fields: failure"},'
        '"protocol_version":1}\n'
    )


@pytest.mark.parametrize("delay_ms", [True, -1, 10_001, 1.5])
def test_delay_must_be_a_bounded_integer(delay_ms: object) -> None:
    result = _run_workload(_valid_input(delay_ms=delay_ms))

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"behavior.delay_ms must be an integer from 0 through 10000"},'
        '"protocol_version":1}\n'
    )


def test_failure_mode_must_be_supported_without_echoing_its_value() -> None:
    result = _run_workload(_valid_input(failure="do-not-print"))

    assert result.returncode == 2
    assert result.stdout == ""
    assert result.stderr == (
        '{"error":{"code":"invalid_input",'
        '"message":"behavior.failure must be one of: none, before_events, '
        'after_first_event"},"protocol_version":1}\n'
    )
