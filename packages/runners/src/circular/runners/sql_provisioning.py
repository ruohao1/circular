from __future__ import annotations

from uuid import NAMESPACE_URL, UUID, uuid5

from circular.domain import RunStatus, Workspace, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.runners.provisioning import WorkspaceProvisioningContext
from circular.storage.models import AgentRecord, RepositoryRecord, RunRecord, TaskRecord
from circular.storage.repositories import (
    RunNotFoundError,
    RunStore,
    WorkspaceAlreadyExistsError,
    WorkspaceNotFoundError,
    WorkspaceStore,
)
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker


class InvalidRunProvisioningStatus(ValueError):
    def __init__(self, run_id: UUID, status: RunStatus) -> None:
        super().__init__(f"run {run_id} cannot provision from {status.value}")
        self.run_id = run_id
        self.status = status


class MissingRunRepository(ValueError):
    def __init__(self, run_id: UUID) -> None:
        super().__init__(f"run {run_id} task has no Repository to provision")
        self.run_id = run_id


class WorkspaceProvisioningConflict(ValueError):
    """Existing durable Workspace state cannot be safely treated as this attempt."""


def workspace_id_for_run(run_id: UUID) -> UUID:
    """Return the stable identifier for the one Workspace owned by a Run."""
    return uuid5(NAMESPACE_URL, f"io.circular.workspace:{run_id}")


class SqlWorkspaceProvisioningPersistence:
    """SQLAlchemy adapter for provisioning state and event transaction boundaries."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        run_store: RunStore,
        workspace_store: WorkspaceStore,
        *,
        source: str = "worker",
    ) -> None:
        if not isinstance(source, str) or not source:
            raise ValueError("provisioning event source must be a non-empty string")
        self._sessions = sessions
        self._run_store = run_store
        self._workspace_store = workspace_store
        self._source = source

    async def load_context(self, run_id: UUID) -> WorkspaceProvisioningContext:
        statement = (
            select(RunRecord, TaskRecord, AgentRecord, RepositoryRecord)
            .join(TaskRecord, TaskRecord.id == RunRecord.task_id)
            .join(AgentRecord, AgentRecord.id == RunRecord.agent_id)
            .outerjoin(RepositoryRecord, RepositoryRecord.id == TaskRecord.repository_id)
            .where(RunRecord.id == run_id)
        )
        async with self._sessions() as session:
            row = (await session.execute(statement)).one_or_none()
        if row is None:
            raise RunNotFoundError(str(run_id))

        run, task, agent, repository = row
        status = RunStatus(run.status)
        if status is not RunStatus.PROVISIONING:
            raise InvalidRunProvisioningStatus(run_id, status)
        if repository is None:
            raise MissingRunRepository(run_id)
        return WorkspaceProvisioningContext(
            run_id=run.id,
            workspace_id=workspace_id_for_run(run.id),
            repository_id=repository.id,
            clone_url=repository.clone_url,
            base_ref=repository.default_branch,
            backend=run.backend,
            task_title=task.title,
            task_description=task.description,
            instructions=agent.instructions,
            backend_config=agent.backend_config,
        )

    async def create_pending(self, workspace: Workspace) -> Workspace:
        async with self._sessions.begin() as session:
            await self._run_store.require_status(
                session,
                workspace.run_id,
                RunStatus.PROVISIONING,
            )
            try:
                return await self._workspace_store.create(
                    session,
                    workspace,
                    source=self._source,
                )
            except WorkspaceAlreadyExistsError:
                existing = await self._workspace_store.load_for_run(session, workspace.run_id)
                if (
                    existing.id != workspace.id
                    or existing.run_id != workspace.run_id
                    or existing.worktree_path != workspace.worktree_path
                    or existing.status is not WorkspaceStatus.PENDING
                    or existing.container_id is not None
                ):
                    raise WorkspaceProvisioningConflict(
                        f"run {workspace.run_id} already has incompatible Workspace state"
                    ) from None
                return existing

    async def record_container(self, workspace_id: UUID, container_id: str) -> Workspace:
        async with self._sessions.begin() as session:
            return await self._workspace_store.record_container(
                session,
                workspace_id,
                container_id,
                source=self._source,
            )

    async def mark_ready_and_running(self, workspace_id: UUID, backend: str) -> Workspace:
        async with self._sessions.begin() as session:
            workspace = await self._workspace_store.load(session, workspace_id)
            if workspace.container_id is None:
                raise WorkspaceProvisioningConflict(
                    f"workspace {workspace_id} cannot become ready without a container"
                )
            await self._run_store.require_status(
                session,
                workspace.run_id,
                RunStatus.PROVISIONING,
            )
            workspace = await self._workspace_store.transition(
                session,
                workspace_id,
                WorkspaceStatus.READY,
                source=self._source,
            )
            await self._run_store.transition(session, workspace.run_id, RunStatus.RUNNING)
            await self._run_store.append_event(
                session,
                EventEnvelope(
                    run_id=workspace.run_id,
                    type=EventType.RUN_STARTED,
                    source=self._source,
                    data={"backend": backend},
                ),
            )
            return workspace

    async def mark_failed(
        self,
        run_id: UUID,
        error: Exception,
        *,
        container_id: str | None,
    ) -> None:
        safe_error = str(error)[:4000]
        async with self._sessions.begin() as session:
            await self._run_store.require_status(
                session,
                run_id,
                RunStatus.PROVISIONING,
            )
            await self._run_store.transition(
                session,
                run_id,
                RunStatus.FAILED,
                error=safe_error,
            )
            try:
                workspace = await self._workspace_store.load_for_run(session, run_id)
            except WorkspaceNotFoundError:
                workspace = None

            if workspace is not None and workspace.status in {
                WorkspaceStatus.PENDING,
                WorkspaceStatus.READY,
            }:
                if container_id is not None and workspace.status is WorkspaceStatus.PENDING:
                    workspace = await self._workspace_store.record_container(
                        session,
                        workspace.id,
                        container_id,
                        source=self._source,
                    )
                transition_container_id = None
                if workspace.container_id is None:
                    transition_container_id = container_id
                await self._workspace_store.transition(
                    session,
                    workspace.id,
                    WorkspaceStatus.FAILED,
                    source=self._source,
                    container_id=transition_container_id,
                )

            await self._run_store.append_event(
                session,
                EventEnvelope(
                    run_id=run_id,
                    type=EventType.RUN_FAILED,
                    source=self._source,
                    data={"error": safe_error},
                ),
            )
