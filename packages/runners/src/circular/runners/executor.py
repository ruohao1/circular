from uuid import UUID

from circular.agents import AgentBackend, BackendContext
from circular.domain import RunStatus, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.storage.models import AgentRecord, RunRecord, TaskRecord, WorkspaceRecord
from circular.storage.repositories import RunStore
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker


class InvalidRunExecutionState(ValueError):
    """A caller attempted execution without a ready, provisioned Workspace."""


class RunExecutor:
    """Execute an already-running Run whose Workspace is durably ready."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        store: RunStore,
        backends: dict[str, AgentBackend],
    ) -> None:
        self._sessions = sessions
        self._store = store
        self._backends = backends

    async def execute(self, run_id: UUID) -> None:
        context, backend_name = await self._load_context(run_id)
        try:
            backend = self._backends[backend_name]
            backend_session = await backend.start(context)
            async for event in backend.events(backend_session):
                async with self._sessions.begin() as session:
                    await self._store.append_event(session, event)

            async with self._sessions.begin() as session:
                await self._store.transition(session, run_id, RunStatus.FINALIZING)
                await self._store.transition(session, run_id, RunStatus.SUCCEEDED)
                await self._store.append_event(
                    session,
                    EventEnvelope(
                        run_id=run_id,
                        type=EventType.RUN_COMPLETED,
                        source="worker",
                        data={},
                    ),
                )
        except Exception as error:
            await self._fail(run_id, error)
            raise

    async def _load_context(self, run_id: UUID) -> tuple[BackendContext, str]:
        async with self._sessions() as session:
            row = (
                await session.execute(
                    select(RunRecord, TaskRecord, AgentRecord, WorkspaceRecord)
                    .join(TaskRecord, TaskRecord.id == RunRecord.task_id)
                    .join(AgentRecord, AgentRecord.id == RunRecord.agent_id)
                    .outerjoin(WorkspaceRecord, WorkspaceRecord.run_id == RunRecord.id)
                    .where(RunRecord.id == run_id)
                )
            ).one_or_none()
            if row is None:
                raise InvalidRunExecutionState(f"run {run_id} is not available for execution")
            run, task, agent, workspace = row
            if RunStatus(run.status) is not RunStatus.RUNNING:
                raise InvalidRunExecutionState(
                    f"run {run_id} must be running before execution, not {run.status}"
                )
            if workspace is None or WorkspaceStatus(workspace.status) is not WorkspaceStatus.READY:
                raise InvalidRunExecutionState(
                    f"run {run_id} requires a ready Workspace before execution"
                )
            return (
                BackendContext(
                    run_id=run.id,
                    task_title=task.title,
                    task_description=task.description,
                    instructions=agent.instructions,
                    workspace_path=workspace.worktree_path,
                    config=agent.backend_config,
                ),
                run.backend,
            )

    async def _fail(self, run_id: UUID, error: Exception) -> None:
        async with self._sessions.begin() as session:
            run = await session.get(RunRecord, run_id)
            if run is None or RunStatus(run.status) in {
                RunStatus.SUCCEEDED,
                RunStatus.FAILED,
                RunStatus.CANCELLED,
            }:
                return
            await self._store.transition(session, run_id, RunStatus.FAILED, error=str(error)[:4000])
            await self._store.append_event(
                session,
                EventEnvelope(
                    run_id=run_id,
                    type=EventType.RUN_FAILED,
                    source="worker",
                    data={"error": str(error)},
                ),
            )
