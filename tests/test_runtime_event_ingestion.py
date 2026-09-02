import asyncio
import json
from collections.abc import AsyncIterator
from contextlib import AbstractAsyncContextManager
from dataclasses import dataclass
from types import SimpleNamespace, TracebackType
from typing import Any
from uuid import UUID

import pytest
from circular.domain import RunStatus
from circular.events import EventType
from circular.runners import (
    BackendProcessError,
    BackendProtocolError,
    BackendReportedError,
    BackendStoppedError,
    EventPersistenceError,
    FakeBackendEventStream,
    RunExecutor,
    RunNotReadyForRuntimeError,
    RuntimeCompletionError,
    RuntimeEventIngestor,
    RuntimeOutputError,
)
from circular.runtimes import (
    ContainerHandle,
    OutputStream,
    RuntimeOutput,
    RuntimeResult,
)

RUN_ID = UUID("00000000-0000-4000-8000-000000000168")
HANDLE = ContainerHandle(id="fake-container", resource_id="fake-resource")


@dataclass
class StubRuntime:
    chunks: tuple[RuntimeOutput, ...]
    result: RuntimeResult = RuntimeResult.exited(0)

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        for chunk in self.chunks:
            yield chunk

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        assert handle == HANDLE
        return self.result


class GatedRuntime(StubRuntime):
    def __init__(self, chunks: tuple[RuntimeOutput, ...]) -> None:
        super().__init__(chunks)
        self.first_consumed = asyncio.Event()
        self.release = asyncio.Event()

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        yield self.chunks[0]
        self.first_consumed.set()
        await self.release.wait()
        for chunk in self.chunks[1:]:
            yield chunk


class ClosingRuntime(StubRuntime):
    def __init__(self, chunks: tuple[RuntimeOutput, ...]) -> None:
        super().__init__(chunks)
        self.closed = asyncio.Event()

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        try:
            async for chunk in super().output(handle):
                yield chunk
        finally:
            self.closed.set()


class FailingOutputRuntime(StubRuntime):
    def __init__(self, chunk: RuntimeOutput) -> None:
        super().__init__((chunk,))
        self.wait_calls = 0

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        yield self.chunks[0]
        raise OSError("output pump failed")

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        self.wait_calls += 1
        return await super().wait(handle)


class SynchronousOutputFailureRuntime(StubRuntime):
    def __init__(self) -> None:
        super().__init__(())
        self.wait_calls = 0

    def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        raise OSError("output acquisition failed")

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        self.wait_calls += 1
        return await super().wait(handle)


class MissingOutputIteratorRuntime(StubRuntime):
    def __init__(self) -> None:
        super().__init__(())
        self.wait_calls = 0

    def output(self, handle: ContainerHandle) -> Any:
        assert handle == HANDLE
        return None

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        self.wait_calls += 1
        return await super().wait(handle)


class CloseFailingOutput:
    def __init__(
        self,
        chunks: tuple[RuntimeOutput, ...],
        *,
        iteration_error: Exception | None = None,
    ) -> None:
        self._chunks = iter(chunks)
        self._iteration_error = iteration_error
        self.close_calls = 0

    def __aiter__(self) -> "CloseFailingOutput":
        return self

    async def __anext__(self) -> RuntimeOutput:
        try:
            return next(self._chunks)
        except StopIteration:
            if self._iteration_error is not None:
                raise self._iteration_error from None
            raise StopAsyncIteration from None

    async def aclose(self) -> None:
        self.close_calls += 1
        raise OSError("output close failed")


class CloseFailingRuntime(StubRuntime):
    def __init__(
        self,
        chunks: tuple[RuntimeOutput, ...],
        *,
        iteration_error: Exception | None = None,
    ) -> None:
        super().__init__(chunks)
        self.iterator = CloseFailingOutput(chunks, iteration_error=iteration_error)
        self.wait_calls = 0

    def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        return self.iterator

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        self.wait_calls += 1
        return await super().wait(handle)


class CancellableOutputRuntime(StubRuntime):
    def __init__(self) -> None:
        super().__init__(())
        self.output_started = asyncio.Event()
        self.output_closed = asyncio.Event()
        self.release = asyncio.Event()
        self.wait_calls = 0

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        self.output_started.set()
        try:
            await self.release.wait()
            yield RuntimeOutput(OutputStream.STDOUT, b"")
        finally:
            self.output_closed.set()

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        self.wait_calls += 1
        return await super().wait(handle)


class CancellableWaitRuntime(StubRuntime):
    def __init__(self) -> None:
        super().__init__(())
        self.wait_started = asyncio.Event()
        self.release = asyncio.Event()

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        assert handle == HANDLE
        self.wait_started.set()
        await self.release.wait()
        return self.result


class FailingWaitRuntime(StubRuntime):
    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        assert handle == HANDLE
        raise OSError("completion inspection failed")


class CountingRuntime(StubRuntime):
    def __init__(self, chunks: tuple[RuntimeOutput, ...]) -> None:
        super().__init__(chunks)
        self.output_calls = 0

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        self.output_calls += 1
        async for chunk in super().output(handle):
            yield chunk


class RecordingTransaction(AbstractAsyncContextManager["RecordingTransaction"]):
    def __init__(self, sessions: "RecordingSessions") -> None:
        self._sessions = sessions
        self.pending: list[Any] = []

    async def __aenter__(self) -> "RecordingTransaction":
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: TracebackType | None,
    ) -> bool | None:
        del exc_value, traceback
        if exc_type is None:
            self._sessions.committed.extend(self.pending)
        return None

    async def get(self, model: type, run_id: UUID) -> Any:
        del model
        assert run_id == RUN_ID
        return self._sessions.run


class RecordingSessions:
    def __init__(self) -> None:
        self.committed: list[Any] = []
        self.transactions = 0
        self.run = SimpleNamespace(status=RunStatus.RUNNING.value)

    def begin(self) -> RecordingTransaction:
        self.transactions += 1
        return RecordingTransaction(self)


class RecordingStore:
    async def append_event(self, session: RecordingTransaction, event: Any) -> None:
        session.pending.append(event)


class FailingStore(RecordingStore):
    def __init__(self, fail_on_call: int) -> None:
        self._fail_on_call = fail_on_call
        self._calls = 0

    async def append_event(self, session: RecordingTransaction, event: Any) -> None:
        self._calls += 1
        if self._calls == self._fail_on_call:
            raise OSError("database unavailable")
        await super().append_event(session, event)


class LifecycleStore(RecordingStore):
    async def transition(
        self,
        session: RecordingTransaction,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> None:
        session.pending.append(("transition", run_id, target, error))


class FailureRecordingStore(LifecycleStore):
    async def transition(
        self,
        session: RecordingTransaction,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> None:
        del session, run_id, target, error
        raise OSError("do-not-print")


class CompletionFailingStore(LifecycleStore):
    def __init__(self, failure: Exception) -> None:
        self._failure = failure
        self._failed = False

    async def transition(
        self,
        session: RecordingTransaction,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> None:
        if target is RunStatus.FINALIZING and not self._failed:
            self._failed = True
            raise self._failure
        await super().transition(session, run_id, target, error=error)


class UnprintableError(OSError):
    def __str__(self) -> str:
        raise ValueError("cannot render error")


class MalformedNotesFailureStore(LifecycleStore):
    async def transition(
        self,
        session: RecordingTransaction,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> None:
        del session, run_id, error
        if target is RunStatus.FINALIZING:
            execution_error = OSError("original execution failure")
            execution_error.__notes__ = "malformed"  # type: ignore[assignment]
            raise execution_error
        raise RuntimeError("secondary failure recording failure")


def _event_line(event_type: str, data: object) -> bytes:
    return (
        json.dumps(
            {
                "protocol_version": 1,
                "run_id": str(RUN_ID),
                "source": "fake-container-workload",
                "type": event_type,
                "data": data,
            },
            separators=(",", ":"),
        ).encode()
        + b"\n"
    )


def _raw_line(document: object) -> bytes:
    return json.dumps(document, separators=(",", ":")).encode() + b"\n"


def _valid_event_document() -> dict[str, Any]:
    return {
        "protocol_version": 1,
        "run_id": str(RUN_ID),
        "source": "fake-container-workload",
        "type": "agent.message.delta",
        "data": {"delta": "valid"},
    }


async def test_event_stream_reassembles_utf8_json_lines_and_preserves_the_raw_event() -> None:
    raw_event = {
        "protocol_version": 1,
        "run_id": str(RUN_ID),
        "source": "fake-container-workload",
        "type": "agent.message.delta",
        "data": {"delta": "Café 🧪"},
    }
    encoded = (json.dumps(raw_event, ensure_ascii=False) + "\n").encode()
    split_at = encoded.index("🧪".encode()) + 1
    runtime = StubRuntime(
        (
            RuntimeOutput(OutputStream.STDOUT, encoded[:split_at]),
            RuntimeOutput(OutputStream.STDOUT, encoded[split_at:]),
        )
    )

    events = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert len(events) == 1
    assert events[0].run_id == RUN_ID
    assert events[0].type is EventType.AGENT_MESSAGE_DELTA
    assert events[0].source == "fake-container-workload"
    assert events[0].data == {"delta": "Café 🧪"}
    assert events[0].raw == raw_event


async def test_event_stream_normalizes_every_supported_event_in_wire_order() -> None:
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                b"".join(
                    (
                        _event_line("agent.message.delta", {"delta": "first"}),
                        _event_line("agent.message.delta", {"delta": "second"}),
                        _event_line("agent.message.completed", {"content": "firstsecond"}),
                        _event_line(
                            "usage.updated",
                            {"input_tokens": 3, "output_tokens": 2},
                        ),
                    )
                ),
            ),
        )
    )

    events = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert [event.type for event in events] == [
        EventType.AGENT_MESSAGE_DELTA,
        EventType.AGENT_MESSAGE_DELTA,
        EventType.AGENT_MESSAGE_COMPLETED,
        EventType.USAGE_UPDATED,
    ]
    assert [event.data for event in events] == [
        {"delta": "first"},
        {"delta": "second"},
        {"content": "firstsecond"},
        {"input_tokens": 3, "output_tokens": 2},
    ]
    assert [event.raw for event in events] == [
        json.loads(_event_line("agent.message.delta", {"delta": "first"})),
        json.loads(_event_line("agent.message.delta", {"delta": "second"})),
        json.loads(_event_line("agent.message.completed", {"content": "firstsecond"})),
        json.loads(_event_line("usage.updated", {"input_tokens": 3, "output_tokens": 2})),
    ]


async def test_event_stream_rejects_duplicate_json_fields_without_echoing_values() -> None:
    duplicate = (
        b'{"protocol_version":1,"run_id":"'
        + str(RUN_ID).encode()
        + b'","source":"fake-container-workload","type":"agent.message.delta",'
        b'"data":{"delta":"first","delta":"do-not-print"}}\n'
    )
    runtime = StubRuntime((RuntimeOutput(OutputStream.STDOUT, duplicate),))

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert "duplicate" in str(exc_info.value)
    assert "do-not-print" not in str(exc_info.value)
    assert exc_info.value.raw is None


async def test_event_stream_yields_prior_events_then_raises_a_typed_backend_error() -> None:
    raw_error = {
        "protocol_version": 1,
        "run_id": str(RUN_ID),
        "error": {
            "code": "injected_failure",
            "message": "injected failure after first event",
        },
    }
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "persist me"}),
            ),
            RuntimeOutput(
                OutputStream.STDERR,
                (json.dumps(raw_error, separators=(",", ":")) + "\n").encode(),
            ),
        ),
        result=RuntimeResult.exited(20),
    )
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()

    first = await anext(events)
    with pytest.raises(BackendReportedError) as exc_info:
        await anext(events)

    assert first.data == {"delta": "persist me"}
    assert exc_info.value.code == "injected_failure"
    assert exc_info.value.raw == raw_error


async def test_event_stream_turns_an_unexplained_nonzero_exit_into_a_typed_failure() -> None:
    runtime = StubRuntime((), result=RuntimeResult.exited(23))

    with pytest.raises(BackendProcessError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert exc_info.value.exit_code == 23
    assert str(exc_info.value) == "fake backend exited with code 23"


async def test_event_stream_does_not_treat_a_stopped_process_as_success() -> None:
    runtime = StubRuntime((), result=RuntimeResult.stopped())

    with pytest.raises(BackendStoppedError):
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]


async def test_ingestor_commits_each_event_before_reading_the_next_chunk() -> None:
    runtime = GatedRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "visible now"}),
            ),
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.completed", {"content": "visible now"}),
            ),
        )
    )
    sessions = RecordingSessions()
    ingestor = RuntimeEventIngestor(sessions, RecordingStore())

    ingest_task = asyncio.create_task(ingestor.ingest(RUN_ID, runtime, HANDLE))
    await asyncio.wait_for(runtime.first_consumed.wait(), timeout=1)

    assert [event.data for event in sessions.committed] == [{"delta": "visible now"}]
    assert not ingest_task.done()

    runtime.release.set()
    await asyncio.wait_for(ingest_task, timeout=1)

    assert [event.data for event in sessions.committed] == [
        {"delta": "visible now"},
        {"content": "visible now"},
    ]
    assert sessions.transactions == 2


async def test_ingestor_keeps_prior_commits_and_types_the_first_persistence_failure() -> None:
    runtime = ClosingRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "committed"})
                + _event_line("agent.message.completed", {"content": "not committed"}),
            ),
        )
    )
    sessions = RecordingSessions()
    ingestor = RuntimeEventIngestor(sessions, FailingStore(fail_on_call=2))

    with pytest.raises(EventPersistenceError) as exc_info:
        await ingestor.ingest(RUN_ID, runtime, HANDLE)

    assert [event.data for event in sessions.committed] == [{"delta": "committed"}]
    assert exc_info.value.run_id == RUN_ID
    assert exc_info.value.event_type is EventType.AGENT_MESSAGE_COMPLETED
    assert isinstance(exc_info.value.__cause__, OSError)
    assert runtime.closed.is_set()


async def test_event_stream_waits_for_completion_after_output_iteration_fails() -> None:
    runtime = FailingOutputRuntime(
        RuntimeOutput(
            OutputStream.STDOUT,
            _event_line("agent.message.delta", {"delta": "before failure"}),
        )
    )
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()

    first = await anext(events)
    with pytest.raises(RuntimeOutputError) as exc_info:
        await anext(events)

    assert first.data == {"delta": "before failure"}
    assert isinstance(exc_info.value.__cause__, OSError)
    assert runtime.wait_calls == 1


async def test_event_stream_waits_after_synchronous_output_acquisition_fails() -> None:
    runtime = SynchronousOutputFailureRuntime()

    with pytest.raises(RuntimeOutputError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert isinstance(exc_info.value.__cause__, OSError)
    assert runtime.wait_calls == 1


async def test_event_stream_types_a_missing_output_iterator_and_waits() -> None:
    runtime = MissingOutputIteratorRuntime()

    with pytest.raises(RuntimeOutputError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert isinstance(exc_info.value.__cause__, TypeError)
    assert runtime.wait_calls == 1


async def test_event_stream_types_an_output_iterator_close_failure_and_waits() -> None:
    runtime = CloseFailingRuntime(())

    with pytest.raises(RuntimeOutputError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert isinstance(exc_info.value.__cause__, OSError)
    assert str(exc_info.value.__cause__) == "output close failed"
    assert runtime.iterator.close_calls == 1
    assert runtime.wait_calls == 1


async def test_iterator_close_failure_does_not_mask_an_incomplete_protocol_record() -> None:
    runtime = CloseFailingRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "unterminated"}).rstrip(b"\n"),
            ),
        )
    )

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert "incomplete JSON line" in str(exc_info.value)
    assert isinstance(exc_info.value.__cause__, OSError)
    assert str(exc_info.value.__cause__) == "output close failed"
    assert runtime.wait_calls == 1


async def test_iterator_close_failure_does_not_mask_an_output_iteration_failure() -> None:
    runtime = CloseFailingRuntime((), iteration_error=OSError("output pump failed first"))

    with pytest.raises(RuntimeOutputError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert isinstance(exc_info.value.__cause__, OSError)
    assert str(exc_info.value.__cause__) == "output pump failed first"
    assert runtime.iterator.close_calls == 1
    assert runtime.wait_calls == 1


async def test_iterator_close_failure_does_not_mask_an_event_persistence_failure() -> None:
    runtime = CloseFailingRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "cannot commit"}),
            ),
        )
    )
    ingestor = RuntimeEventIngestor(RecordingSessions(), FailingStore(fail_on_call=1))

    with pytest.raises(EventPersistenceError) as exc_info:
        await ingestor.ingest(RUN_ID, runtime, HANDLE)

    assert isinstance(exc_info.value.__cause__, OSError)
    assert str(exc_info.value.__cause__) == "database unavailable"
    assert runtime.iterator.close_calls == 1
    assert runtime.wait_calls == 0


async def test_cancellation_during_output_propagates_without_waiting() -> None:
    runtime = CancellableOutputRuntime()
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()
    next_event = asyncio.create_task(anext(events))
    await asyncio.wait_for(runtime.output_started.wait(), timeout=1)

    next_event.cancel()

    with pytest.raises(asyncio.CancelledError):
        await next_event
    assert runtime.output_closed.is_set()
    assert runtime.wait_calls == 0


async def test_cancellation_during_runtime_wait_propagates() -> None:
    runtime = CancellableWaitRuntime()
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()
    next_event = asyncio.create_task(anext(events))
    await asyncio.wait_for(runtime.wait_started.wait(), timeout=1)

    next_event.cancel()

    with pytest.raises(asyncio.CancelledError):
        await next_event


async def test_event_stream_types_a_runtime_completion_failure() -> None:
    runtime = FailingWaitRuntime(())

    with pytest.raises(RuntimeCompletionError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert isinstance(exc_info.value.__cause__, OSError)


@pytest.mark.parametrize(
    ("raw_value", "expected_message"),
    [
        (b"NaN", "valid JSON"),
        (b'"\\ud800"', "valid Unicode"),
    ],
)
async def test_event_stream_rejects_nonstandard_json_and_lone_surrogates(
    raw_value: bytes,
    expected_message: str,
) -> None:
    line = (
        b'{"protocol_version":1,"run_id":"'
        + str(RUN_ID).encode()
        + b'","source":"fake-container-workload","type":"agent.message.delta",'
        b'"data":{"delta":' + raw_value + b"}}\n"
    )
    runtime = StubRuntime((RuntimeOutput(OutputStream.STDOUT, line),))

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert expected_message in str(exc_info.value)
    assert exc_info.value.raw is None


async def test_runtime_executor_rejects_numeric_overflow_without_unsafe_raw() -> None:
    overflowing_number = (
        b'{"protocol_version":1,"run_id":"'
        + str(RUN_ID).encode()
        + b'","source":"fake-container-workload","type":"agent.message.delta",'
        b'"data":{"delta":1e400}}\n'
    )
    runtime = StubRuntime((RuntimeOutput(OutputStream.STDOUT, overflowing_number),))
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, LifecycleStore(), {})

    with pytest.raises(BackendProtocolError) as exc_info:
        await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert exc_info.value.raw is None
    assert sessions.committed[1].type is EventType.RUN_FAILED
    assert sessions.committed[1].raw is None


async def test_event_stream_bounds_partial_lines_independently_of_chunk_boundaries() -> None:
    runtime = StubRuntime(
        (
            RuntimeOutput(OutputStream.STDOUT, b"{" + b" " * 15),
            RuntimeOutput(OutputStream.STDOUT, b" " * 17),
        )
    )

    with pytest.raises(BackendProtocolError, match="line limit"):
        _ = [
            event
            async for event in FakeBackendEventStream(
                RUN_ID,
                runtime,
                HANDLE,
                max_line_bytes=32,
            ).events()
        ]


async def test_event_stream_accepts_the_versioned_invalid_input_error_shape() -> None:
    raw_error = {
        "protocol_version": 1,
        "error": {
            "code": "invalid_input",
            "message": "stdin must contain one JSON object",
        },
    }
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDERR,
                (json.dumps(raw_error, separators=(",", ":")) + "\n").encode(),
            ),
        ),
        result=RuntimeResult.exited(2),
    )

    with pytest.raises(BackendReportedError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert exc_info.value.code == "invalid_input"
    assert exc_info.value.raw == raw_error


async def test_event_stream_retains_valid_rejected_stderr_raw() -> None:
    raw_error = {
        "protocol_version": 2,
        "error": {
            "code": "invalid_input",
            "message": "unsupported input",
        },
    }
    runtime = StubRuntime(
        (RuntimeOutput(OutputStream.STDERR, _raw_line(raw_error)),),
        result=RuntimeResult.exited(2),
    )

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert exc_info.value.raw == raw_error


async def test_runtime_executor_ingests_then_completes_an_already_running_run() -> None:
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("usage.updated", {"input_tokens": 3, "output_tokens": 2}),
            ),
        )
    )
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, LifecycleStore(), {})

    await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert sessions.committed[0].type is EventType.USAGE_UPDATED
    assert sessions.committed[1:3] == [
        ("transition", RUN_ID, RunStatus.FINALIZING, None),
        ("transition", RUN_ID, RunStatus.SUCCEEDED, None),
    ]
    assert sessions.committed[3].type is EventType.RUN_COMPLETED


async def test_runtime_executor_persists_backend_error_raw_on_run_failure() -> None:
    raw_error = {
        "protocol_version": 1,
        "run_id": str(RUN_ID),
        "error": {
            "code": "injected_failure",
            "message": "injected failure before emitting events",
        },
    }
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDERR,
                (json.dumps(raw_error, separators=(",", ":")) + "\n").encode(),
            ),
        ),
        result=RuntimeResult.exited(20),
    )
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, LifecycleStore(), {})

    with pytest.raises(BackendReportedError):
        await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert sessions.committed[0] == (
        "transition",
        RUN_ID,
        RunStatus.FAILED,
        "fake backend reported injected_failure",
    )
    assert sessions.committed[1].type is EventType.RUN_FAILED
    assert sessions.committed[1].raw == raw_error


async def test_runtime_executor_persists_rejected_protocol_event_raw_on_run_failure() -> None:
    raw_event = {**_valid_event_document(), "source": "unexpected-backend"}
    runtime = StubRuntime((RuntimeOutput(OutputStream.STDOUT, _raw_line(raw_event)),))
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, LifecycleStore(), {})

    with pytest.raises(BackendProtocolError):
        await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert sessions.committed[0][2] is RunStatus.FAILED
    assert sessions.committed[1].type is EventType.RUN_FAILED
    assert sessions.committed[1].raw == raw_event


async def test_runtime_executor_preserves_original_error_when_failure_recording_fails() -> None:
    runtime = StubRuntime((), result=RuntimeResult.exited(23))
    executor = RunExecutor(RecordingSessions(), FailureRecordingStore(), {})

    with pytest.raises(BackendProcessError) as exc_info:
        await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert exc_info.value.exit_code == 23
    assert exc_info.value.__notes__ == ["failed to persist Run execution failure (OSError)"]
    assert "do-not-print" not in str(exc_info.value)


async def test_failure_note_error_does_not_mask_the_original_execution_error() -> None:
    executor = RunExecutor(RecordingSessions(), MalformedNotesFailureStore(), {})

    with pytest.raises(OSError, match="original execution failure") as exc_info:
        await executor.execute_runtime(RUN_ID, StubRuntime(()), HANDLE)

    assert exc_info.value.__notes__ == "malformed"


async def test_runtime_executor_caps_one_error_projection_for_failure_state_and_event() -> None:
    long_message = "\x00\ud800" + "x" * 5001
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, CompletionFailingStore(OSError(long_message)), {})

    with pytest.raises(OSError):
        await executor.execute_runtime(RUN_ID, StubRuntime(()), HANDLE)

    expected = "\N{REPLACEMENT CHARACTER}?" + "x" * 3998
    assert sessions.committed[0] == (
        "transition",
        RUN_ID,
        RunStatus.FAILED,
        expected,
    )
    assert sessions.committed[1].type is EventType.RUN_FAILED
    assert sessions.committed[1].data == {"error": expected}


async def test_runtime_executor_safely_projects_an_unprintable_error() -> None:
    sessions = RecordingSessions()
    executor = RunExecutor(sessions, CompletionFailingStore(UnprintableError()), {})

    with pytest.raises(UnprintableError):
        await executor.execute_runtime(RUN_ID, StubRuntime(()), HANDLE)

    assert sessions.committed[0][3] == "UnprintableError"
    assert sessions.committed[1].data == {"error": "UnprintableError"}


async def test_runtime_executor_rejects_a_non_running_run_before_consuming_output() -> None:
    runtime = CountingRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "must not persist"}),
            ),
        )
    )
    sessions = RecordingSessions()
    sessions.run.status = RunStatus.PROVISIONING.value
    executor = RunExecutor(sessions, LifecycleStore(), {})

    with pytest.raises(RunNotReadyForRuntimeError) as exc_info:
        await executor.execute_runtime(RUN_ID, runtime, HANDLE)

    assert exc_info.value.status is RunStatus.PROVISIONING
    assert runtime.output_calls == 0
    assert all(
        not hasattr(item, "data") or item.data != {"delta": "must not persist"}
        for item in sessions.committed
    )


async def test_protocol_failure_identifies_its_stream_and_line_without_echoing_values() -> None:
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "first"})
                + _event_line("do-not-print", {}),
            ),
        )
    )
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()

    first = await anext(events)
    with pytest.raises(BackendProtocolError) as exc_info:
        await anext(events)

    assert first.data == {"delta": "first"}
    assert exc_info.value.stream is OutputStream.STDOUT
    assert exc_info.value.line_number == 2
    assert "do-not-print" not in str(exc_info.value)


@pytest.mark.parametrize(
    "document",
    [
        {**_valid_event_document(), "protocol_version": True},
        {**_valid_event_document(), "protocol_version": 2},
        {**_valid_event_document(), "run_id": RUN_ID.hex},
        {**_valid_event_document(), "run_id": "do-not-print"},
        {**_valid_event_document(), "source": "do-not-print"},
        {**_valid_event_document(), "type": "run.started"},
        {**_valid_event_document(), "type": "do-not-print"},
        {**_valid_event_document(), "data": {"delta": 7}},
        {
            **_valid_event_document(),
            "type": "agent.message.completed",
            "data": {"content": "valid", "extra": "do-not-print"},
        },
        {
            **_valid_event_document(),
            "type": "usage.updated",
            "data": {"input_tokens": True, "output_tokens": 2},
        },
        {
            **_valid_event_document(),
            "type": "usage.updated",
            "data": {"input_tokens": -1, "output_tokens": 2},
        },
        {**_valid_event_document(), "extra": "do-not-print"},
        {field: value for field, value in _valid_event_document().items() if field != "data"},
    ],
)
async def test_event_stream_rejects_noncanonical_or_unsupported_event_shapes(
    document: dict[str, Any],
) -> None:
    runtime = StubRuntime((RuntimeOutput(OutputStream.STDOUT, _raw_line(document)),))

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert "do-not-print" not in str(exc_info.value)
    assert exc_info.value.raw == document


async def test_event_stream_rejects_an_unterminated_final_record() -> None:
    runtime = StubRuntime(
        (
            RuntimeOutput(
                OutputStream.STDOUT,
                _event_line("agent.message.delta", {"delta": "unterminated"}).rstrip(b"\n"),
            ),
        )
    )

    with pytest.raises(BackendProtocolError) as exc_info:
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]

    assert exc_info.value.stream is OutputStream.STDOUT
    assert exc_info.value.line_number == 1


async def test_event_stream_keeps_stdout_and_stderr_partial_lines_separate() -> None:
    event_line = _event_line("agent.message.delta", {"delta": "before error"})
    raw_error = {
        "protocol_version": 1,
        "run_id": str(RUN_ID),
        "error": {"code": "injected_failure", "message": "expected failure"},
    }
    error_line = _raw_line(raw_error)
    runtime = StubRuntime(
        (
            RuntimeOutput(OutputStream.STDOUT, event_line[:19]),
            RuntimeOutput(OutputStream.STDERR, error_line[:13]),
            RuntimeOutput(OutputStream.STDOUT, event_line[19:]),
            RuntimeOutput(OutputStream.STDERR, error_line[13:]),
        ),
        result=RuntimeResult.exited(20),
    )
    events = FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()

    first = await anext(events)
    with pytest.raises(BackendReportedError) as exc_info:
        await anext(events)

    assert first.data == {"delta": "before error"}
    assert exc_info.value.raw == raw_error


async def test_protocol_failure_takes_precedence_over_a_nonzero_exit() -> None:
    runtime = StubRuntime(
        (RuntimeOutput(OutputStream.STDOUT, b"not-json\n"),),
        result=RuntimeResult.exited(23),
    )

    with pytest.raises(BackendProtocolError):
        _ = [event async for event in FakeBackendEventStream(RUN_ID, runtime, HANDLE).events()]
