import os

import pytest
from circular.agents import FakeAgentBackend
from circular.domain import RunStatus
from circular.runners import RunExecutor
from circular.storage import (
    AgentRecord,
    EventRecord,
    ProjectRecord,
    RunRecord,
    RunStore,
    TaskRecord,
    create_engine,
    create_session_factory,
)
from sqlalchemy import select

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")


async def test_claim_execute_and_persist_events_against_postgres() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    try:
        async with sessions.begin() as session:
            project = ProjectRecord(name="Integration test")
            session.add(project)
            await session.flush()
            agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
            task = TaskRecord(project_id=project.id, title="Exercise the worker")
            session.add_all([agent, task])
            await session.flush()
            run = RunRecord(task_id=task.id, agent_id=agent.id, backend="fake", attempt=1)
            session.add(run)
            await session.flush()
            run_id = run.id

        async with sessions.begin() as session:
            claimed = await store.claim_next(session, "integration-worker")
            assert claimed is not None
            assert claimed.id == run_id
            assert claimed.status == RunStatus.PROVISIONING.value

        executor = RunExecutor(sessions, store, {"fake": FakeAgentBackend()})
        await executor.execute(run_id)

        async with sessions() as session:
            persisted = await session.get(RunRecord, run_id)
            events = list(
                await session.scalars(
                    select(EventRecord)
                    .where(EventRecord.run_id == run_id)
                    .order_by(EventRecord.sequence)
                )
            )
        assert persisted is not None
        assert persisted.status == RunStatus.SUCCEEDED.value
        assert [event.sequence for event in events] == list(range(1, len(events) + 1))
        assert events[0].type == "run.started"
        assert events[-1].type == "run.completed"
    finally:
        await engine.dispose()
