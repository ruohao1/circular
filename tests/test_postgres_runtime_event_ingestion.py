import asyncio
import json
import os
from collections.abc import AsyncIterator
from contextlib import suppress
from uuid import UUID, uuid4

import pytest
from circular.events import EventEnvelope, EventType
from circular.runners import RuntimeEventIngestor
from circular.runtimes import ContainerHandle, OutputStream, RuntimeOutput, RuntimeResult
from circular.storage import (
    AgentRecord,
    ProjectRecord,
    RunEventReader,
    RunRecord,
    RunStore,
    TaskRecord,
    create_engine,
    create_session_factory,
)
from sqlalchemy import delete, select

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")
TEST_PROJECT_PREFIX = "__circular_test_isq_168_runtime_event_ingestion__"
HANDLE = ContainerHandle(id="fake-container", resource_id="fake-resource")


class GatedRuntime:
    def __init__(self, chunks: tuple[RuntimeOutput, ...]) -> None:
        self._chunks = chunks
        self.first_committed = asyncio.Event()
        self.release = asyncio.Event()

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        assert handle == HANDLE
        yield self._chunks[0]
        self.first_committed.set()
        await self.release.wait()
        for chunk in self._chunks[1:]:
            yield chunk

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        assert handle == HANDLE
        return RuntimeResult.exited(0)


def _line(run_id: UUID, event_type: str, data: dict[str, object]) -> bytes:
    return (
        json.dumps(
            {
                "protocol_version": 1,
                "run_id": str(run_id),
                "source": "fake-container-workload",
                "type": event_type,
                "data": data,
            },
            separators=(",", ":"),
        ).encode()
        + b"\n"
    )


async def test_ingested_events_are_incrementally_visible_and_concurrently_contiguous() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    project_id: UUID | None = None
    runtime: GatedRuntime | None = None
    ingest_task: asyncio.Task[None] | None = None
    try:
        async with sessions.begin() as session:
            project = ProjectRecord(name=f"{TEST_PROJECT_PREFIX}{uuid4()}")
            session.add(project)
            await session.flush()
            project_id = project.id
            agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
            task = TaskRecord(project_id=project.id, title="Ingest runtime events")
            session.add_all([agent, task])
            await session.flush()
            run = RunRecord(task_id=task.id, agent_id=agent.id, backend="fake", attempt=1)
            session.add(run)
            await session.flush()
            run_id = run.id

        first_raw = {
            "protocol_version": 1,
            "run_id": str(run_id),
            "source": "fake-container-workload",
            "type": "agent.message.delta",
            "data": {"delta": "visible before exit"},
        }
        runtime = GatedRuntime(
            (
                RuntimeOutput(
                    OutputStream.STDOUT,
                    _line(run_id, "agent.message.delta", first_raw["data"]),
                ),
                RuntimeOutput(
                    OutputStream.STDOUT,
                    _line(
                        run_id,
                        "agent.message.completed",
                        {"content": "visible before exit"},
                    ),
                ),
            )
        )
        ingest_task = asyncio.create_task(
            RuntimeEventIngestor(sessions, store).ingest(run_id, runtime, HANDLE)
        )
        await asyncio.wait_for(runtime.first_committed.wait(), timeout=10)

        reader = RunEventReader(sessions)
        visible_while_running = await reader.read_after(run_id, 0)
        assert [(event.sequence, event.type) for event in visible_while_running] == [
            (1, "agent.message.delta")
        ]
        assert visible_while_running[0].data == {"delta": "visible before exit"}
        assert visible_while_running[0].raw == first_raw

        async with sessions.begin() as session:
            await store.append_event(
                session,
                EventEnvelope(
                    run_id=run_id,
                    type=EventType.RUN_STARTED,
                    source="concurrent-test",
                    data={},
                ),
            )
        runtime.release.set()
        await asyncio.wait_for(ingest_task, timeout=10)

        all_events = await reader.read_after(run_id, 0)
        assert [event.sequence for event in all_events] == [1, 2, 3]
        assert [event.type for event in all_events] == [
            "agent.message.delta",
            "run.started",
            "agent.message.completed",
        ]
        assert all_events[2].raw == {
            "protocol_version": 1,
            "run_id": str(run_id),
            "source": "fake-container-workload",
            "type": "agent.message.completed",
            "data": {"content": "visible before exit"},
        }
    finally:
        if runtime is not None:
            runtime.release.set()
        if ingest_task is not None and not ingest_task.done():
            with suppress(Exception):
                await asyncio.wait_for(ingest_task, timeout=10)
        if project_id is not None:
            async with sessions.begin() as session:
                task_ids = select(TaskRecord.id).where(TaskRecord.project_id == project_id)
                await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(task_ids)))
                await session.execute(delete(ProjectRecord).where(ProjectRecord.id == project_id))
        await engine.dispose()
