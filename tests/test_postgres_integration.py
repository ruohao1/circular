import os
from datetime import UTC, datetime

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
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")
TEST_PROJECT_NAME = "__circular_test_run_execution__"


async def remove_test_fixture(session: AsyncSession) -> None:
    project_ids = select(ProjectRecord.id).where(ProjectRecord.name == TEST_PROJECT_NAME)
    task_ids = select(TaskRecord.id).where(TaskRecord.project_id.in_(project_ids))
    await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(task_ids)))
    await session.execute(delete(ProjectRecord).where(ProjectRecord.id.in_(project_ids)))


async def test_claim_execute_and_persist_events_against_postgres() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    try:
        async with sessions.begin() as session:
            await remove_test_fixture(session)
            project = ProjectRecord(name=TEST_PROJECT_NAME)
            session.add(project)
            await session.flush()
            agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
            task = TaskRecord(project_id=project.id, title="Exercise the worker")
            session.add_all([agent, task])
            await session.flush()
            run = RunRecord(
                task_id=task.id,
                agent_id=agent.id,
                backend="fake",
                attempt=1,
                created_at=datetime(1800, 1, 1, tzinfo=UTC),
            )
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
        try:
            async with sessions.begin() as session:
                await remove_test_fixture(session)
        finally:
            await engine.dispose()
