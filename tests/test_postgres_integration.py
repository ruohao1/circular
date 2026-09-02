import os
from datetime import UTC, datetime
from pathlib import Path
from uuid import UUID

import pytest
from circular.agents import FakeAgentBackend
from circular.domain import RunStatus
from circular.git import ProvisionedWorktree
from circular.runners import (
    ExecutionDirectories,
    FakeWorkloadSpecFactory,
    InvalidRunExecutionState,
    RunExecutor,
    SqlWorkspaceProvisioningPersistence,
    WorkspaceProvisioner,
)
from circular.runtimes import ContainerHandle, ContainerSpec
from circular.storage import (
    AgentRecord,
    EventRecord,
    ProjectRecord,
    RepositoryRecord,
    RunRecord,
    RunStore,
    TaskRecord,
    WorkspaceStore,
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


class IntegrationCache:
    def __init__(self, path: Path) -> None:
        self.path = path

    async def checkout(self, repository_id: UUID, clone_url: str) -> Path:
        return self.path


class IntegrationWorktrees:
    def __init__(self, worktree: ProvisionedWorktree) -> None:
        self.worktree = worktree

    async def provision(
        self,
        run_id: UUID,
        repository_path: Path,
        base_ref: str,
    ) -> ProvisionedWorktree:
        return self.worktree

    async def release(self, worktree: ProvisionedWorktree) -> None:
        raise AssertionError("cleanup is outside this integration slice")


class IntegrationRuntime:
    async def start(self, spec: ContainerSpec) -> ContainerHandle:
        return ContainerHandle("integration-container")

    def output(self, handle: ContainerHandle):
        raise AssertionError("event ingestion is outside this integration slice")

    async def wait(self, handle: ContainerHandle):
        raise AssertionError("runtime completion is outside this integration slice")

    async def stop(self, handle: ContainerHandle) -> None:
        raise AssertionError("cleanup is outside this integration slice")


async def test_claim_provision_execute_and_persist_events_against_postgres(
    tmp_path: Path,
) -> None:
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
            repository = RepositoryRecord(
                project_id=project.id,
                name="circular",
                clone_url="https://example.test/circular.git",
                default_branch="main",
            )
            session.add_all([agent, repository])
            await session.flush()
            task = TaskRecord(
                project_id=project.id,
                repository_id=repository.id,
                title="Exercise the worker",
            )
            session.add(task)
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
        with pytest.raises(InvalidRunExecutionState, match="must be running"):
            await executor.execute(run_id)

        directories = ExecutionDirectories(
            repository_cache_root=tmp_path / "repositories",
            worktree_root=tmp_path / "worktrees",
            artifact_root=tmp_path / "artifacts",
            docker_worktree_root=tmp_path / "docker-worktrees",
        )
        repository_path = directories.repository_cache_path(repository.id)
        worktree_path = directories.run_paths(run_id).worktree
        provisioner = WorkspaceProvisioner(
            persistence=SqlWorkspaceProvisioningPersistence(
                sessions,
                store,
                WorkspaceStore(),
                source="integration-worker",
            ),
            repository_cache=IntegrationCache(repository_path),
            worktrees=IntegrationWorktrees(
                ProvisionedWorktree(
                    run_id=run_id,
                    repository_path=repository_path,
                    path=worktree_path,
                    branch=f"circular/run/{run_id}",
                )
            ),
            runtime=IntegrationRuntime(),
            directories=directories,
            spec_factory=FakeWorkloadSpecFactory(
                image="circular-runner:test",
                cpu_limit=1,
                memory_limit_mb=512,
            ),
        )
        workspace = await provisioner.provision(run_id)
        assert workspace.status.value == "ready"

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
        assert [event.type for event in events[:4]] == [
            "workspace.provisioning",
            "workspace.provisioning",
            "workspace.ready",
            "run.started",
        ]
        assert events[-1].type == "run.completed"
    finally:
        try:
            async with sessions.begin() as session:
                await remove_test_fixture(session)
        finally:
            await engine.dispose()
