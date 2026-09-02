import asyncio
import os
from collections.abc import AsyncIterator
from dataclasses import dataclass, replace
from uuid import UUID, uuid4

import pytest
from circular.domain import Artifact, RunStatus, Workspace, WorkspaceStatus
from circular.orchestration import (
    InvalidWorkspaceInitialContainer,
    InvalidWorkspaceInitialStatus,
    InvalidWorkspaceTransition,
)
from circular.storage import (
    AgentRecord,
    ArtifactStore,
    ProjectRecord,
    RunEventReader,
    RunRecord,
    RunStore,
    TaskRecord,
    WorkspaceAlreadyExistsError,
    WorkspaceContainerIdConflictError,
    WorkspaceNotFoundError,
    WorkspaceStore,
    create_engine,
    create_session_factory,
)
from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

database_url = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(not database_url, reason="TEST_DATABASE_URL is not set")
TEST_PROJECT_PREFIX = "__circular_test_isq_164_workspace_artifact_stores__"


@dataclass(frozen=True)
class StoreFixture:
    sessions: async_sessionmaker[AsyncSession]
    run_id: UUID
    other_run_id: UUID


async def remove_stale_fixtures(session: AsyncSession) -> None:
    project_ids = select(ProjectRecord.id).where(ProjectRecord.name.startswith(TEST_PROJECT_PREFIX))
    task_ids = select(TaskRecord.id).where(TaskRecord.project_id.in_(project_ids))
    await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(task_ids)))
    await session.execute(delete(ProjectRecord).where(ProjectRecord.id.in_(project_ids)))


@pytest.fixture
async def store_fixture() -> AsyncIterator[StoreFixture]:
    assert database_url is not None
    engine = create_engine(database_url)
    sessions = create_session_factory(engine)
    project_id: UUID | None = None
    try:
        async with sessions.begin() as session:
            await remove_stale_fixtures(session)
            project = ProjectRecord(name=f"{TEST_PROJECT_PREFIX}{uuid4()}")
            session.add(project)
            await session.flush()
            project_id = project.id
            agent = AgentRecord(project_id=project.id, name="Test engineer", backend="fake")
            tasks = [
                TaskRecord(project_id=project.id, title="Persist execution resources"),
                TaskRecord(project_id=project.id, title="List execution resources"),
            ]
            session.add_all([agent, *tasks])
            await session.flush()
            runs = [
                RunRecord(task_id=task.id, agent_id=agent.id, backend="fake", attempt=1)
                for task in tasks
            ]
            session.add_all(runs)
            await session.flush()

        yield StoreFixture(
            sessions=sessions,
            run_id=runs[0].id,
            other_run_id=runs[1].id,
        )
    finally:
        if project_id is not None:
            async with sessions.begin() as session:
                task_ids = select(TaskRecord.id).where(TaskRecord.project_id == project_id)
                await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(task_ids)))
                await session.execute(delete(ProjectRecord).where(ProjectRecord.id == project_id))
        await engine.dispose()


async def test_create_workspace_makes_it_loadable_and_records_provisioning(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/run-one",
    )

    async with store_fixture.sessions.begin() as session:
        created = await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert created == workspace
    assert loaded == workspace
    assert [(event.sequence, event.type, event.source, event.data) for event in events] == [
        (
            1,
            "workspace.provisioning",
            "test-worker",
            {"status": "pending", "workspace_id": str(workspace.id)},
        )
    ]


async def test_duplicate_workspace_is_typed_and_keeps_the_transaction_usable(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    existing = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/original",
    )
    duplicate = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/duplicate",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, existing, source="test-worker")

    async with store_fixture.sessions.begin() as session:
        with pytest.raises(WorkspaceAlreadyExistsError) as exc_info:
            await store.create(session, duplicate, source="test-worker")
        loaded = await store.load_for_run(session, store_fixture.run_id)

    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert exc_info.value.run_id == store_fixture.run_id
    assert loaded == existing
    assert [event.type for event in events] == ["workspace.provisioning"]


async def test_transition_workspace_persists_status_and_corresponding_event(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/transitioned",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions.begin() as session:
        transitioned = await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.READY,
            source="test-worker",
        )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert transitioned.status is WorkspaceStatus.READY
    assert loaded.status is WorkspaceStatus.READY
    assert [(event.sequence, event.type, event.data) for event in events] == [
        (
            1,
            "workspace.provisioning",
            {"status": "pending", "workspace_id": str(workspace.id)},
        ),
        (
            2,
            "workspace.ready",
            {"status": "ready", "workspace_id": str(workspace.id)},
        ),
    ]


async def test_ready_transition_records_container_id_and_release_retains_it(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/containerized",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")
        ready = await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.READY,
            source="test-worker",
            container_id="container-123",
        )
        released = await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.RELEASED,
            source="test-worker",
        )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert ready.container_id == "container-123"
    assert released.container_id == "container-123"
    assert loaded.container_id == "container-123"
    assert [(event.type, event.data.get("container_id")) for event in events] == [
        ("workspace.provisioning", None),
        ("workspace.ready", "container-123"),
        ("workspace.released", "container-123"),
    ]


async def test_pending_workspace_records_container_id_idempotently(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/pending-container",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")
        first = await store.record_container(
            session,
            workspace.id,
            "container-pending-123",
            source="test-worker",
        )
        repeated = await store.record_container(
            session,
            workspace.id,
            "container-pending-123",
            source="test-worker",
        )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert first == repeated == loaded
    assert loaded.status is WorkspaceStatus.PENDING
    assert loaded.container_id == "container-pending-123"
    assert [(event.type, event.data) for event in events] == [
        (
            "workspace.provisioning",
            {"status": "pending", "workspace_id": str(workspace.id)},
        ),
        (
            "workspace.provisioning",
            {
                "status": "pending",
                "stage": "container_started",
                "workspace_id": str(workspace.id),
                "container_id": "container-pending-123",
            },
        ),
    ]


async def test_pending_workspace_rejects_replacing_recorded_container(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/pending-container-conflict",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")
        await store.record_container(
            session,
            workspace.id,
            "container-original",
            source="test-worker",
        )

    async with store_fixture.sessions.begin() as session:
        with pytest.raises(WorkspaceContainerIdConflictError):
            await store.record_container(
                session,
                workspace.id,
                "container-replacement",
                source="test-worker",
            )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert loaded.container_id == "container-original"
    assert [event.type for event in events] == [
        "workspace.provisioning",
        "workspace.provisioning",
    ]


async def test_create_rejects_a_workspace_that_bypasses_the_initial_state(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/already-ready",
        status=WorkspaceStatus.READY,
    )

    with pytest.raises(InvalidWorkspaceInitialStatus) as exc_info:
        async with store_fixture.sessions.begin() as session:
            await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions() as session:
        with pytest.raises(WorkspaceNotFoundError):
            await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert exc_info.value.status is WorkspaceStatus.READY
    assert events == ()


async def test_create_rejects_an_initial_container_without_persisting_anything(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/premature-container",
        container_id="premature-container",
    )

    with pytest.raises(InvalidWorkspaceInitialContainer) as exc_info:
        async with store_fixture.sessions.begin() as session:
            await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions() as session:
        with pytest.raises(WorkspaceNotFoundError):
            await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert exc_info.value.container_id == "premature-container"
    assert events == ()


async def test_list_workspaces_is_deterministic_and_can_filter_by_status(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    first_in_order = Workspace(
        id=UUID(int=1),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/first",
    )
    second_in_order = Workspace(
        id=UUID(int=2),
        run_id=store_fixture.other_run_id,
        worktree_path="/worktrees/second",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, second_in_order, source="test-worker")
        await store.create(session, first_in_order, source="test-worker")
        await store.transition(
            session,
            second_in_order.id,
            WorkspaceStatus.READY,
            source="test-worker",
        )

    async with store_fixture.sessions() as session:
        all_workspaces = await store.list(session)
        pending_workspaces = await store.list(session, status=WorkspaceStatus.PENDING)

    assert all_workspaces == (
        first_in_order,
        replace(second_in_order, status=WorkspaceStatus.READY),
    )
    assert pending_workspaces == (first_in_order,)


async def test_append_artifacts_lists_domain_entities_for_the_run_in_stable_order(
    store_fixture: StoreFixture,
) -> None:
    store = ArtifactStore()
    first_in_order = Artifact(
        id=UUID(int=11),
        run_id=store_fixture.run_id,
        kind="diff",
        uri="artifact://diff.patch",
        metadata={"files": 2},
    )
    second_in_order = Artifact(
        id=UUID(int=12),
        run_id=store_fixture.run_id,
        kind="report",
        uri="artifact://report.json",
        metadata={"passed": True},
    )
    other_run_artifact = Artifact(
        id=UUID(int=10),
        run_id=store_fixture.other_run_id,
        kind="log",
        uri="artifact://other.log",
    )

    async with store_fixture.sessions.begin() as session:
        await store.append(session, second_in_order, source="test-worker")
        await store.append(session, other_run_artifact, source="test-worker")
        await store.append(session, first_in_order, source="test-worker")

    async with store_fixture.sessions() as session:
        artifacts = await store.list_for_run(session, store_fixture.run_id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert artifacts == (first_in_order, second_in_order)
    assert [(event.sequence, event.type, event.data) for event in events] == [
        (
            1,
            "artifact.created",
            {
                "artifact_id": str(second_in_order.id),
                "kind": "report",
                "uri": "artifact://report.json",
            },
        ),
        (
            2,
            "artifact.created",
            {
                "artifact_id": str(first_in_order.id),
                "kind": "diff",
                "uri": "artifact://diff.patch",
            },
        ),
    ]


async def test_caller_rollback_reverts_workspace_transition_and_event(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/rollback",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions() as session:
        transaction = await session.begin()
        await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.READY,
            source="test-worker",
            container_id="rolled-back-container",
        )
        await transaction.rollback()

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert loaded.status is WorkspaceStatus.PENDING
    assert loaded.container_id is None
    assert [event.type for event in events] == ["workspace.provisioning"]


async def test_caller_rollback_reverts_workspace_creation_and_provisioning_event(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/rolled-back-creation",
    )

    async with store_fixture.sessions() as session:
        transaction = await session.begin()
        await store.create(session, workspace, source="test-worker")
        await transaction.rollback()

    async with store_fixture.sessions() as session:
        with pytest.raises(WorkspaceNotFoundError):
            await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert events == ()


async def test_caller_rollback_reverts_artifact_and_created_event(
    store_fixture: StoreFixture,
) -> None:
    store = ArtifactStore()
    artifact = Artifact(
        id=uuid4(),
        run_id=store_fixture.run_id,
        kind="log",
        uri="artifact://rolled-back.log",
    )

    async with store_fixture.sessions() as session:
        transaction = await session.begin()
        await store.append(session, artifact, source="test-worker")
        await transaction.rollback()

    async with store_fixture.sessions() as session:
        artifacts = await store.list_for_run(session, store_fixture.run_id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert artifacts == ()
    assert events == ()


async def test_invalid_workspace_transition_changes_neither_state_nor_events(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/invalid-transition",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")

    async with store_fixture.sessions.begin() as session:
        with pytest.raises(InvalidWorkspaceTransition):
            await store.transition(
                session,
                workspace.id,
                WorkspaceStatus.RELEASED,
                source="test-worker",
            )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)

    assert loaded.status is WorkspaceStatus.PENDING
    assert [event.type for event in events] == ["workspace.provisioning"]


async def test_failed_transition_can_record_container_id_and_release_retains_it(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/failed",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")
        failed = await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.FAILED,
            source="test-worker",
            container_id="failed-container",
        )
        released = await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.RELEASED,
            source="test-worker",
        )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert failed.container_id == "failed-container"
    assert released.container_id == "failed-container"
    assert loaded.container_id == "failed-container"
    assert [event.type for event in events] == [
        "workspace.provisioning",
        "workspace.failed",
        "workspace.released",
    ]


async def test_transition_rejects_replacing_an_existing_container_id(
    store_fixture: StoreFixture,
) -> None:
    store = WorkspaceStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/immutable-container",
    )
    async with store_fixture.sessions.begin() as session:
        await store.create(session, workspace, source="test-worker")
        await store.transition(
            session,
            workspace.id,
            WorkspaceStatus.READY,
            source="test-worker",
            container_id="original-container",
        )

    async with store_fixture.sessions.begin() as session:
        with pytest.raises(WorkspaceContainerIdConflictError) as exc_info:
            await store.transition(
                session,
                workspace.id,
                WorkspaceStatus.FAILED,
                source="test-worker",
                container_id="replacement-container",
            )

    async with store_fixture.sessions() as session:
        loaded = await store.load(session, workspace.id)
    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert exc_info.value.workspace_id == workspace.id
    assert loaded.status is WorkspaceStatus.READY
    assert loaded.container_id == "original-container"
    assert [event.type for event in events] == [
        "workspace.provisioning",
        "workspace.ready",
    ]


async def wait_until_backend_is_blocked(
    sessions: async_sessionmaker[AsyncSession],
    backend_pid: int,
) -> None:
    async with asyncio.timeout(2):
        while True:
            async with sessions() as session:
                blockers = await session.scalar(select(func.pg_blocking_pids(backend_pid)))
            if blockers:
                return
            await asyncio.sleep(0.01)


async def test_workspace_operations_lock_run_before_workspace(
    store_fixture: StoreFixture,
) -> None:
    workspace_store = WorkspaceStore()
    run_store = RunStore()
    workspace = Workspace(
        id=uuid4(),
        run_id=store_fixture.run_id,
        worktree_path="/worktrees/lock-order",
    )
    async with store_fixture.sessions.begin() as session:
        await workspace_store.create(session, workspace, source="test-worker")

    run_locked = asyncio.Event()
    continue_run_owner = asyncio.Event()
    contender_pid: asyncio.Future[int] = asyncio.get_running_loop().create_future()

    async def transition_run_then_workspace() -> Workspace:
        async with store_fixture.sessions.begin() as session:
            await run_store.transition(session, store_fixture.run_id, RunStatus.PROVISIONING)
            run_locked.set()
            await continue_run_owner.wait()
            return await workspace_store.transition(
                session,
                workspace.id,
                WorkspaceStatus.READY,
                source="run-owner",
            )

    async def transition_competing_workspace() -> Workspace:
        async with store_fixture.sessions.begin() as session:
            pid = await session.scalar(select(func.pg_backend_pid()))
            assert pid is not None
            contender_pid.set_result(pid)
            return await workspace_store.transition(
                session,
                workspace.id,
                WorkspaceStatus.FAILED,
                source="contender",
            )

    owner_task = asyncio.create_task(transition_run_then_workspace())
    contender_task: asyncio.Task[Workspace] | None = None
    try:
        async with asyncio.timeout(5):
            await run_locked.wait()
            contender_task = asyncio.create_task(transition_competing_workspace())
            await wait_until_backend_is_blocked(
                store_fixture.sessions,
                await contender_pid,
            )
            continue_run_owner.set()
            owner_workspace, contender_workspace = await asyncio.gather(
                owner_task,
                contender_task,
            )
    finally:
        continue_run_owner.set()
        tasks = [owner_task, *([contender_task] if contender_task is not None else [])]
        for task in tasks:
            if not task.done():
                task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)

    events = await RunEventReader(store_fixture.sessions).read_after(store_fixture.run_id, 0)
    assert owner_workspace.status is WorkspaceStatus.READY
    assert contender_workspace.status is WorkspaceStatus.FAILED
    assert [(event.type, event.source) for event in events] == [
        ("workspace.provisioning", "test-worker"),
        ("workspace.ready", "run-owner"),
        ("workspace.failed", "contender"),
    ]
