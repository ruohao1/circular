from contextlib import AbstractAsyncContextManager
from dataclasses import dataclass
from typing import Any
from uuid import UUID

import pytest
from circular.domain import RunStatus
from circular.runners import InvalidRunExecutionState, RunExecutor

RUN_ID = UUID("00000000-0000-4000-8000-000000000169")


class AsyncContext(AbstractAsyncContextManager[Any]):
    def __init__(self, value: Any) -> None:
        self.value = value

    async def __aenter__(self) -> Any:
        return self.value

    async def __aexit__(self, *exc_info: object) -> None:
        return None


class OperationalLoadSession:
    def __init__(self, error: Exception) -> None:
        self.error = error

    async def execute(self, statement: object) -> object:
        raise self.error


class EmptyResult:
    def one_or_none(self) -> None:
        return None


class PreconditionLoadSession:
    async def execute(self, statement: object) -> EmptyResult:
        return EmptyResult()


@dataclass
class RunState:
    status: str = RunStatus.RUNNING.value
    error: str | None = None


class FailureSession:
    def __init__(self, run: RunState, error: Exception | None = None) -> None:
        self.run = run
        self.error = error

    async def get(self, record_type: object, run_id: UUID) -> RunState:
        assert run_id == RUN_ID
        if self.error is not None:
            raise self.error
        return self.run


class RecordingSessions:
    def __init__(
        self,
        load_session: object,
        run: RunState,
        *,
        failure_error: Exception | None = None,
    ) -> None:
        self.load_session = load_session
        self.failure_session = FailureSession(run, failure_error)
        self.failure_transactions = 0

    def __call__(self) -> AsyncContext:
        return AsyncContext(self.load_session)

    def begin(self) -> AsyncContext:
        self.failure_transactions += 1
        return AsyncContext(self.failure_session)


class RecordingRunStore:
    def __init__(self, run: RunState) -> None:
        self.run = run
        self.events: list[object] = []

    async def transition(
        self,
        session: FailureSession,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> RunState:
        assert target is RunStatus.FAILED
        self.run.status = target.value
        self.run.error = error
        return self.run

    async def append_event(self, session: FailureSession, event: object) -> object:
        self.events.append(event)
        return event


async def test_operational_context_load_failure_moves_running_run_to_failed() -> None:
    run = RunState()
    primary = RuntimeError("context query failed")
    sessions = RecordingSessions(OperationalLoadSession(primary), run)
    store = RecordingRunStore(run)
    executor = RunExecutor(sessions, store, {})  # type: ignore[arg-type]

    with pytest.raises(RuntimeError, match="context query failed") as exc_info:
        await executor.execute(RUN_ID)

    assert exc_info.value is primary
    assert run.status == RunStatus.FAILED.value
    assert run.error == "context query failed"
    assert sessions.failure_transactions == 1
    assert len(store.events) == 1


async def test_invalid_execution_precondition_does_not_mutate_run() -> None:
    run = RunState()
    sessions = RecordingSessions(PreconditionLoadSession(), run)
    store = RecordingRunStore(run)
    executor = RunExecutor(sessions, store, {})  # type: ignore[arg-type]

    with pytest.raises(InvalidRunExecutionState, match="not available"):
        await executor.execute(RUN_ID)

    assert run.status == RunStatus.RUNNING.value
    assert sessions.failure_transactions == 0
    assert store.events == []


async def test_context_load_failure_persistence_does_not_mask_the_load_error() -> None:
    run = RunState()
    primary = RuntimeError("context query failed")
    secondary = RuntimeError("password=must-not-be-logged")
    sessions = RecordingSessions(
        OperationalLoadSession(primary),
        run,
        failure_error=secondary,
    )
    executor = RunExecutor(sessions, RecordingRunStore(run), {})  # type: ignore[arg-type]

    with pytest.raises(RuntimeError, match="context query failed") as exc_info:
        await executor.execute(RUN_ID)

    assert exc_info.value is primary
    assert getattr(primary, "__notes__", ()) == [
        "failed to persist Run execution failure (RuntimeError)"
    ]
    assert "password" not in repr(getattr(primary, "__notes__", ()))
