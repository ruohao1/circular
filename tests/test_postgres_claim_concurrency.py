import asyncio
import os
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from uuid import UUID

import pytest
from circular.domain import RunStatus
from circular.storage import (
    AgentRecord,
    ProjectRecord,
    RunRecord,
    RunStore,
    TaskRecord,
    create_engine,
    create_session_factory,
)
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")
TEST_PROJECT_NAME = "__circular_test_isq_180_concurrent_claiming__"


@dataclass(frozen=True)
class ClaimedRun:
    id: UUID
    status: str
    worker_id: str | None


@dataclass(frozen=True)
class QueueFixture:
    project_id: UUID
    run_ids: frozenset[UUID]


async def seed_queued_runs(sessions: async_sessionmaker[AsyncSession], count: int) -> QueueFixture:
    async with sessions.begin() as session:
        stale_fixture_task_ids = select(TaskRecord.id).where(
            TaskRecord.project_id.in_(
                select(ProjectRecord.id).where(ProjectRecord.name == TEST_PROJECT_NAME)
            )
        )
        await session.execute(
            delete(RunRecord).where(RunRecord.task_id.in_(stale_fixture_task_ids))
        )
        await session.execute(delete(ProjectRecord).where(ProjectRecord.name == TEST_PROJECT_NAME))

        project = ProjectRecord(name=TEST_PROJECT_NAME)
        session.add(project)
        await session.flush()

        agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
        tasks = [
            TaskRecord(project_id=project.id, title=f"Claim queued run {index}")
            for index in range(count)
        ]
        session.add_all([agent, *tasks])
        await session.flush()

        runs = [
            RunRecord(
                task_id=task.id,
                agent_id=agent.id,
                backend="fake",
                attempt=1,
                # Test fixtures sort ahead of ordinary queued work without changing any
                # records that may already exist in the integration-test database.
                created_at=datetime(1900, 1, 1, tzinfo=UTC) + timedelta(microseconds=index),
            )
            for index, task in enumerate(tasks)
        ]
        session.add_all(runs)
        await session.flush()
        return QueueFixture(project_id=project.id, run_ids=frozenset(run.id for run in runs))


async def remove_queue_fixture(
    sessions: async_sessionmaker[AsyncSession], project_id: UUID
) -> None:
    async with sessions.begin() as session:
        fixture_task_ids = select(TaskRecord.id).where(TaskRecord.project_id == project_id)
        await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(fixture_task_ids)))
        await session.execute(delete(ProjectRecord).where(ProjectRecord.id == project_id))


def snapshot(run: RunRecord) -> ClaimedRun:
    return ClaimedRun(id=run.id, status=run.status, worker_id=run.worker_id)


async def test_concurrent_claimers_own_distinct_queued_runs() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    fixture: QueueFixture | None = None
    try:
        fixture = await seed_queued_runs(sessions, count=2)

        first_claimed = asyncio.Event()
        release_first_claim = asyncio.Event()

        async def claim_and_hold_first_transaction() -> ClaimedRun:
            async with sessions() as session:
                async with session.begin():
                    claimed = await store.claim_next(session, "worker-one")
                    assert claimed is not None
                    result = snapshot(claimed)
                    first_claimed.set()
                    await release_first_claim.wait()
                    return result

        first_claim_task = asyncio.create_task(claim_and_hold_first_transaction())
        try:
            async with asyncio.timeout(10):
                await first_claimed.wait()
                async with sessions.begin() as session:
                    claimed = await store.claim_next(session, "worker-two")
                    assert claimed is not None
                    second_claim = snapshot(claimed)
        finally:
            release_first_claim.set()
            first_claim = await asyncio.wait_for(first_claim_task, timeout=10)

        assert {first_claim.id, second_claim.id} == fixture.run_ids
        assert first_claim.id != second_claim.id
        assert first_claim.status == RunStatus.PROVISIONING.value
        assert second_claim.status == RunStatus.PROVISIONING.value
        assert first_claim.worker_id == "worker-one"
        assert second_claim.worker_id == "worker-two"
    finally:
        if fixture is not None:
            await remove_queue_fixture(sessions, fixture.project_id)
        await engine.dispose()


async def test_rolled_back_claim_can_be_reclaimed_by_another_worker() -> None:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    fixture: QueueFixture | None = None

    try:
        fixture = await seed_queued_runs(sessions, count=1)
        expected_run_id = next(iter(fixture.run_ids))
        async with sessions() as session:
            transaction = await session.begin()
            try:
                claimed = await store.claim_next(session, "worker-before-rollback")
                assert claimed is not None
                rolled_back_claim = snapshot(claimed)
            finally:
                await transaction.rollback()

        async with sessions.begin() as session:
            claimed = await store.claim_next(session, "worker-after-rollback")
            assert claimed is not None
            reclaimed = snapshot(claimed)

        assert rolled_back_claim.id == expected_run_id
        assert rolled_back_claim.status == RunStatus.PROVISIONING.value
        assert rolled_back_claim.worker_id == "worker-before-rollback"
        assert reclaimed.id == expected_run_id
        assert reclaimed.status == RunStatus.PROVISIONING.value
        assert reclaimed.worker_id == "worker-after-rollback"
    finally:
        if fixture is not None:
            await remove_queue_fixture(sessions, fixture.project_id)
        await engine.dispose()
