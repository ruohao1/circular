import os

import pytest
from circular.events import EventEnvelope, EventType
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

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")


async def test_run_event_reader_returns_a_bounded_page_after_the_cursor_in_order() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    reader = RunEventReader(sessions)
    try:
        async with sessions.begin() as session:
            project = ProjectRecord(name="Event reader integration test")
            session.add(project)
            await session.flush()
            agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
            task = TaskRecord(project_id=project.id, title="Read ordered events")
            session.add_all([agent, task])
            await session.flush()
            run = RunRecord(task_id=task.id, agent_id=agent.id, backend="fake", attempt=1)
            session.add(run)
            await session.flush()
            run_id = run.id
            for event_type in (
                EventType.RUN_STARTED,
                EventType.AGENT_MESSAGE_DELTA,
                EventType.TOOL_EXECUTION_STARTED,
                EventType.TOOL_EXECUTION_COMPLETED,
            ):
                await store.append_event(
                    session,
                    EventEnvelope(
                        run_id=run_id,
                        type=event_type,
                        source="integration-test",
                        data={},
                    ),
                )

        first_page = await reader.read_after(run_id, 2, limit=1)
        second_page = await reader.read_after(run_id, first_page[-1].sequence, limit=1)

        assert await reader.run_exists(run_id)
        assert [(event.sequence, event.type) for event in first_page] == [
            (3, "tool.execution.started")
        ]
        assert [(event.sequence, event.type) for event in second_page] == [
            (4, "tool.execution.completed")
        ]
    finally:
        await engine.dispose()
