import json
from collections.abc import AsyncIterator, Iterator
from typing import Any
from uuid import UUID

from circular.events import EventEnvelope, EventType
from circular.runtimes import ContainerHandle, OutputStream, Runtime, RuntimeOutput, RuntimeResult
from circular.storage.repositories import RunStore
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

_PROTOCOL_VERSION = 1
_SOURCE = "fake-container-workload"
_DEFAULT_MAX_LINE_BYTES = 1024 * 1024


class EventIngestionError(RuntimeError):
    """Base class for safe, explicit container event-ingestion failures."""


class BackendProtocolError(EventIngestionError):
    """The container emitted bytes that do not satisfy its declared protocol."""

    def __init__(
        self,
        reason: str,
        *,
        stream: OutputStream | None = None,
        line_number: int | None = None,
    ) -> None:
        self.reason = reason
        self.stream = stream
        self.line_number = line_number
        super().__init__(self._message())

    def locate(self, stream: OutputStream, line_number: int) -> None:
        if self.stream is None:
            self.stream = stream
            self.line_number = line_number
            self.args = (self._message(),)

    def _message(self) -> str:
        if self.stream is None:
            return self.reason
        return f"{self.reason} at {self.stream.value} line {self.line_number}"


class BackendReportedError(EventIngestionError):
    """The backend emitted a valid protocol error record."""

    def __init__(self, run_id: UUID, code: str, message: str, raw: dict[str, Any]) -> None:
        super().__init__(f"fake backend reported {code}")
        self.run_id = run_id
        self.code = code
        self.backend_message = message
        self.raw = raw


class BackendProcessError(EventIngestionError):
    """The backend process exited unsuccessfully without a protocol error."""

    def __init__(self, run_id: UUID, exit_code: int) -> None:
        super().__init__(f"fake backend exited with code {exit_code}")
        self.run_id = run_id
        self.exit_code = exit_code


class BackendStoppedError(EventIngestionError):
    """The runtime stopped the backend before normal process completion."""

    def __init__(self, run_id: UUID) -> None:
        super().__init__("fake backend stopped before completing")
        self.run_id = run_id


class EventPersistenceError(EventIngestionError):
    """A normalized event could not be committed to the source of truth."""

    def __init__(self, run_id: UUID, event_type: EventType) -> None:
        super().__init__(f"could not persist {event_type.value} for run {run_id}")
        self.run_id = run_id
        self.event_type = event_type


class RuntimeOutputError(EventIngestionError):
    """The runtime failed while the backend output stream was being consumed."""

    def __init__(self, run_id: UUID) -> None:
        super().__init__(f"could not read fake backend output for run {run_id}")
        self.run_id = run_id


class RuntimeCompletionError(EventIngestionError):
    """The runtime could not provide the backend's terminal process result."""

    def __init__(self, run_id: UUID) -> None:
        super().__init__(f"could not determine fake backend completion for run {run_id}")
        self.run_id = run_id


class _DuplicateField(ValueError):
    pass


class _NonstandardJsonConstant(ValueError):
    pass


def _object_without_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    document: dict[str, Any] = {}
    for field, value in pairs:
        if field in document:
            raise _DuplicateField
        document[field] = value
    return document


def _reject_nonstandard_constant(value: str) -> None:
    del value
    raise _NonstandardJsonConstant


def _require_valid_unicode(value: Any) -> None:
    if isinstance(value, str):
        value.encode("utf-8")
        return
    if isinstance(value, dict):
        for field, child in value.items():
            _require_valid_unicode(field)
            _require_valid_unicode(child)
        return
    if isinstance(value, list):
        for child in value:
            _require_valid_unicode(child)


class FakeBackendEventStream:
    """Translate the fake container workload's JSON Lines into normalized events."""

    def __init__(
        self,
        run_id: UUID,
        runtime: Runtime,
        handle: ContainerHandle,
        *,
        max_line_bytes: int = _DEFAULT_MAX_LINE_BYTES,
    ) -> None:
        if type(max_line_bytes) is not int or max_line_bytes <= 0:
            raise ValueError("max_line_bytes must be a positive integer")
        self._run_id = run_id
        self._runtime = runtime
        self._handle = handle
        self._max_line_bytes = max_line_bytes

    async def events(self) -> AsyncIterator[EventEnvelope]:
        decoder = _EventDecoder(self._run_id, self._max_line_bytes)
        failure: EventIngestionError | None = None
        output_failure: Exception | None = None
        output = self._runtime.output(self._handle)
        try:
            async for chunk in output:
                if failure is not None:
                    continue
                try:
                    for event in decoder.feed(chunk):
                        yield event
                except EventIngestionError as error:
                    failure = error
        except Exception as error:
            output_failure = error
        finally:
            close = getattr(output, "aclose", None)
            if close is not None:
                await close()
        if failure is None and output_failure is None:
            try:
                decoder.finish()
            except EventIngestionError as error:
                failure = error
        try:
            result = await self._runtime.wait(self._handle)
        except Exception as wait_error:
            if failure is not None:
                raise failure from wait_error
            if output_failure is not None:
                raise RuntimeOutputError(self._run_id) from output_failure
            raise RuntimeCompletionError(self._run_id) from wait_error
        if failure is not None:
            if output_failure is not None:
                raise failure from output_failure
            raise failure
        if output_failure is not None:
            raise RuntimeOutputError(self._run_id) from output_failure
        self._validate_result(result)

    def _validate_result(self, result: RuntimeResult) -> None:
        if result.exit_code is None:
            raise BackendStoppedError(self._run_id)
        if result.exit_code != 0:
            raise BackendProcessError(self._run_id, result.exit_code)


class RuntimeEventIngestor:
    """Persist fake-backend runtime events as independently visible facts."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        store: RunStore,
    ) -> None:
        self._sessions = sessions
        self._store = store

    async def ingest(self, run_id: UUID, runtime: Runtime, handle: ContainerHandle) -> None:
        stream = FakeBackendEventStream(run_id, runtime, handle)
        events = stream.events()
        try:
            async for event in events:
                try:
                    async with self._sessions.begin() as session:
                        await self._store.append_event(session, event)
                except Exception as error:
                    raise EventPersistenceError(run_id, event.type) from error
        finally:
            await events.aclose()


class _EventDecoder:
    def __init__(self, run_id: UUID, max_line_bytes: int) -> None:
        self._run_id = run_id
        self._max_line_bytes = max_line_bytes
        self._buffers = {
            OutputStream.STDOUT: bytearray(),
            OutputStream.STDERR: bytearray(),
        }
        self._line_numbers = {
            OutputStream.STDOUT: 0,
            OutputStream.STDERR: 0,
        }
        self._current_line_numbers = {
            OutputStream.STDOUT: 1,
            OutputStream.STDERR: 1,
        }

    def feed(self, chunk: RuntimeOutput) -> Iterator[EventEnvelope]:
        try:
            yield from self._feed(chunk)
        except BackendProtocolError as error:
            error.locate(chunk.stream, self._current_line_numbers[chunk.stream])
            raise

    def _feed(self, chunk: RuntimeOutput) -> Iterator[EventEnvelope]:
        buffer = self._buffers[chunk.stream]
        start = 0
        while start < len(chunk.data):
            self._current_line_numbers[chunk.stream] = self._line_numbers[chunk.stream] + 1
            newline = chunk.data.find(b"\n", start)
            end = len(chunk.data) if newline < 0 else newline
            segment_length = end - start
            if len(buffer) + segment_length > self._max_line_bytes:
                raise BackendProtocolError("fake backend JSON line exceeded the line limit")
            buffer.extend(chunk.data[start:end])
            if newline < 0:
                return
            line = bytes(buffer)
            buffer.clear()
            start = newline + 1
            self._line_numbers[chunk.stream] += 1
            if chunk.stream is OutputStream.STDOUT:
                yield self._decode_event(line)
            else:
                self._raise_error(line)

    def finish(self) -> None:
        for stream in (OutputStream.STDOUT, OutputStream.STDERR):
            if self._buffers[stream]:
                raise BackendProtocolError(
                    "fake backend ended with an incomplete JSON line",
                    stream=stream,
                    line_number=self._line_numbers[stream] + 1,
                )

    def _decode_event(self, line: bytes) -> EventEnvelope:
        document = self._decode_object(line, "event")
        expected_fields = {"protocol_version", "run_id", "source", "type", "data"}
        if set(document) != expected_fields:
            raise BackendProtocolError("fake backend event fields do not match protocol version 1")
        self._validate_version(document)
        self._validate_run_id(document)
        if document["source"] != _SOURCE:
            raise BackendProtocolError("fake backend event has an unsupported source")
        try:
            event_type = EventType(document["type"])
        except (TypeError, ValueError):
            raise BackendProtocolError("fake backend event has an unsupported type") from None
        if event_type not in {
            EventType.AGENT_MESSAGE_DELTA,
            EventType.AGENT_MESSAGE_COMPLETED,
            EventType.USAGE_UPDATED,
        }:
            raise BackendProtocolError("fake backend event has an unsupported type")
        normalized = self._normalized_data(event_type, document["data"])
        return EventEnvelope(
            run_id=self._run_id,
            type=event_type,
            source=_SOURCE,
            data=normalized,
            raw=document,
        )

    @staticmethod
    def _decode_object(line: bytes, record_name: str) -> dict[str, Any]:
        try:
            document = json.loads(
                line.decode("utf-8"),
                object_pairs_hook=_object_without_duplicates,
                parse_constant=_reject_nonstandard_constant,
            )
        except _DuplicateField:
            raise BackendProtocolError(
                f"fake backend {record_name} contains a duplicate JSON field"
            ) from None
        except _NonstandardJsonConstant:
            raise BackendProtocolError("fake backend line is not valid JSON") from None
        except (UnicodeDecodeError, json.JSONDecodeError, RecursionError, ValueError):
            raise BackendProtocolError("fake backend emitted an invalid UTF-8 JSON line") from None
        if not isinstance(document, dict):
            raise BackendProtocolError(f"fake backend {record_name} must be a JSON object")
        try:
            _require_valid_unicode(document)
        except (RecursionError, UnicodeEncodeError):
            raise BackendProtocolError(
                f"fake backend {record_name} contains text that is not valid Unicode"
            ) from None
        return document

    @staticmethod
    def _validate_version(document: dict[str, Any]) -> None:
        if (
            type(document["protocol_version"]) is not int
            or document["protocol_version"] != _PROTOCOL_VERSION
        ):
            raise BackendProtocolError("fake backend event uses an unsupported protocol version")

    def _validate_run_id(self, document: dict[str, Any]) -> None:
        if document["run_id"] != str(self._run_id):
            raise BackendProtocolError("fake backend event does not match the executing run")

    def _raise_error(self, line: bytes) -> None:
        document = self._decode_object(line, "error")
        if set(document) not in (
            {"protocol_version", "error"},
            {"protocol_version", "run_id", "error"},
        ):
            raise BackendProtocolError("fake backend error fields do not match protocol version 1")
        self._validate_version(document)
        error = document["error"]
        if not isinstance(error, dict) or set(error) != {"code", "message"}:
            raise BackendProtocolError("fake backend error has invalid data")
        if not isinstance(error["message"], str):
            raise BackendProtocolError("fake backend error has unsupported data")
        if error["code"] == "invalid_input":
            if set(document) != {"protocol_version", "error"}:
                raise BackendProtocolError(
                    "fake backend invalid-input error fields do not match protocol version 1"
                )
        elif error["code"] == "injected_failure":
            if set(document) != {"protocol_version", "run_id", "error"}:
                raise BackendProtocolError(
                    "fake backend injected-failure fields do not match protocol version 1"
                )
            self._validate_run_id(document)
        else:
            raise BackendProtocolError("fake backend error has unsupported data")
        raise BackendReportedError(
            run_id=self._run_id,
            code=error["code"],
            message=error["message"],
            raw=document,
        )

    @staticmethod
    def _normalized_data(event_type: EventType, data: Any) -> dict[str, Any]:
        if not isinstance(data, dict):
            raise BackendProtocolError("fake backend event data must be an object")
        if event_type is EventType.AGENT_MESSAGE_DELTA:
            if set(data) != {"delta"} or not isinstance(data["delta"], str):
                raise BackendProtocolError("fake backend message delta has invalid data")
            return {"delta": data["delta"]}
        if event_type is EventType.AGENT_MESSAGE_COMPLETED:
            if set(data) != {"content"} or not isinstance(data["content"], str):
                raise BackendProtocolError("fake backend completed message has invalid data")
            return {"content": data["content"]}
        if set(data) != {"input_tokens", "output_tokens"} or any(
            type(data[field]) is not int or data[field] < 0
            for field in ("input_tokens", "output_tokens")
        ):
            raise BackendProtocolError("fake backend usage update has invalid data")
        return {
            "input_tokens": data["input_tokens"],
            "output_tokens": data["output_tokens"],
        }
