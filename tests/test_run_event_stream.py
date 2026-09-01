import asyncio
import json
from collections.abc import Mapping
from datetime import UTC, datetime
from typing import Any
from uuid import UUID, uuid4

from circular.api.dependencies import get_run_event_reader
from circular.api.main import app
from circular.storage import EventRecord


async def _get(
    path: str,
    headers: Mapping[str, str] | None = None,
    *,
    disconnect_after: bytes | None = None,
) -> tuple[int, bytes]:
    messages: list[dict[str, Any]] = []
    disconnected = asyncio.Event()
    request_pending = True

    async def receive() -> dict[str, Any]:
        nonlocal request_pending
        if request_pending:
            request_pending = False
            return {"type": "http.request", "body": b"", "more_body": False}
        await disconnected.wait()
        return {"type": "http.disconnect"}

    async def send(message: dict[str, Any]) -> None:
        messages.append(message)
        body = message.get("body", b"")
        if (
            message["type"] == "http.response.body"
            and body
            and (disconnect_after is None or disconnect_after in body)
        ):
            disconnected.set()

    scope = {
        "type": "http",
        "asgi": {"version": "3.0", "spec_version": "2.4"},
        "http_version": "1.1",
        "method": "GET",
        "scheme": "http",
        "path": path,
        "raw_path": path.encode(),
        "query_string": b"",
        "headers": [
            (name.lower().encode(), value.encode()) for name, value in (headers or {}).items()
        ],
        "client": ("test", 123),
        "server": ("test", 80),
    }

    await asyncio.wait_for(app(scope, receive, send), timeout=2)
    status = next(
        message["status"] for message in messages if message["type"] == "http.response.start"
    )
    body = b"".join(
        message.get("body", b"") for message in messages if message["type"] == "http.response.body"
    )
    return status, body


async def test_run_event_stream_rejects_a_non_numeric_replay_cursor() -> None:
    status, body = await _get(
        f"/api/v1/runs/{uuid4()}/events/stream",
        {"Last-Event-ID": "not-a-sequence"},
    )

    assert status == 400
    assert json.loads(body) == {
        "detail": "Last-Event-ID must be a non-negative integer",
    }


async def test_run_event_stream_rejects_a_negative_replay_cursor() -> None:
    status, body = await _get(
        f"/api/v1/runs/{uuid4()}/events/stream",
        {"Last-Event-ID": "-1"},
    )

    assert status == 400
    assert json.loads(body) == {
        "detail": "Last-Event-ID must be a non-negative integer",
    }


async def test_run_event_stream_replays_persisted_events_after_the_cursor_in_order() -> None:
    run_id = uuid4()
    reader = InMemoryRunEventReader(
        run_id,
        (
            _event(run_id, 1, "run.started"),
            _event(run_id, 2, "agent.message.delta"),
            _event(run_id, 3, "tool.execution.started"),
            _event(run_id, 4, "tool.execution.completed"),
        ),
    )

    async def override_reader() -> InMemoryRunEventReader:
        return reader

    app.dependency_overrides[get_run_event_reader] = override_reader
    try:
        status, body = await _get(
            f"/api/v1/runs/{run_id}/events/stream",
            {"Last-Event-ID": "2"},
        )
    finally:
        app.dependency_overrides.pop(get_run_event_reader)

    events = _sse_events(body)
    assert status == 200
    assert [event["id"] for event in events] == [3, 4]
    assert [event["event"] for event in events] == [
        "tool.execution.started",
        "tool.execution.completed",
    ]
    assert [event["data"]["sequence"] for event in events] == [3, 4]


async def test_run_event_stream_polls_for_events_that_appear_after_a_heartbeat() -> None:
    run_id = uuid4()
    reader = AppearingRunEventReader(run_id, (_event(run_id, 1, "run.started"),))

    async def override_reader() -> AppearingRunEventReader:
        return reader

    app.dependency_overrides[get_run_event_reader] = override_reader
    try:
        status, body = await _get(
            f"/api/v1/runs/{run_id}/events/stream",
            disconnect_after=b"id:",
        )
    finally:
        app.dependency_overrides.pop(get_run_event_reader)

    assert status == 200
    assert body.startswith(b": keep-alive\n\n")
    assert [event["id"] for event in _sse_events(body)] == [1]


async def test_run_event_stream_returns_not_found_before_starting_a_stream() -> None:
    known_run_id = uuid4()
    missing_run_id = uuid4()
    reader = InMemoryRunEventReader(known_run_id, ())

    async def override_reader() -> InMemoryRunEventReader:
        return reader

    app.dependency_overrides[get_run_event_reader] = override_reader
    try:
        status, body = await _get(f"/api/v1/runs/{missing_run_id}/events/stream")
    finally:
        app.dependency_overrides.pop(get_run_event_reader)

    assert status == 404
    assert json.loads(body) == {"detail": "run not found"}


async def test_run_event_stream_stops_polling_after_the_client_disconnects() -> None:
    run_id = uuid4()
    reader = InMemoryRunEventReader(run_id, ())

    async def override_reader() -> InMemoryRunEventReader:
        return reader

    app.dependency_overrides[get_run_event_reader] = override_reader
    try:
        status, body = await _get(f"/api/v1/runs/{run_id}/events/stream")
    finally:
        app.dependency_overrides.pop(get_run_event_reader)

    assert status == 200
    assert body == b": keep-alive\n\n"
    assert reader.read_count == 1


class InMemoryRunEventReader:
    def __init__(self, run_id: UUID, events: tuple[EventRecord, ...]) -> None:
        self._run_id = run_id
        self._events = events
        self.read_count = 0

    async def run_exists(self, run_id: UUID) -> bool:
        return run_id == self._run_id

    async def read_after(
        self, run_id: UUID, after: int, *, limit: int = 200
    ) -> tuple[EventRecord, ...]:
        self.read_count += 1
        if run_id != self._run_id:
            return ()
        return tuple(event for event in self._events if event.sequence > after)[:limit]


class AppearingRunEventReader(InMemoryRunEventReader):
    async def read_after(
        self, run_id: UUID, after: int, *, limit: int = 200
    ) -> tuple[EventRecord, ...]:
        if self.read_count == 0:
            self.read_count += 1
            return ()
        return await super().read_after(run_id, after, limit=limit)


def _event(run_id: UUID, sequence: int, event_type: str) -> EventRecord:
    occurred_at = datetime(2026, 9, 1, 12, sequence, tzinfo=UTC)
    return EventRecord(
        position=sequence,
        id=uuid4(),
        run_id=run_id,
        sequence=sequence,
        type=event_type,
        source="test",
        data={"sequence": sequence},
        raw=None,
        occurred_at=occurred_at,
        recorded_at=occurred_at,
    )


def _sse_events(body: bytes) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    for block in body.decode().strip().split("\n\n"):
        fields = dict(line.split(": ", 1) for line in block.splitlines())
        if "id" not in fields:
            continue
        events.append(
            {
                "id": int(fields["id"]),
                "event": fields["event"],
                "data": json.loads(fields["data"]),
            }
        )
    return events
