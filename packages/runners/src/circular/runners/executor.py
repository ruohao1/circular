from uuid import UUID

from circular.agents import AgentBackend, BackendContext
from circular.domain import RunStatus, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.runners.event_ingestion import (
    BackendProtocolError,
    BackendReportedError,
    RuntimeEventIngestor,
)
from circular.runtimes import ContainerHandle, Runtime
from circular.storage.models import AgentRecord, RunRecord, TaskRecord, WorkspaceRecord
from circular.storage.repositories import RunStore
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

_MAX_PERSISTED_ERROR_LENGTH = 4000


def _safe_error_projection(error: Exception) -> str:
    try:
        message = str(error)
    except Exception:
        message = type(error).__name__
    message = message.replace("\x00", "\N{REPLACEMENT CHARACTER}")
    return message.encode("utf-8", errors="replace").decode("utf-8")[:_MAX_PERSISTED_ERROR_LENGTH]


class InvalidRunExecutionState(ValueError):
    """A caller attempted execution without a ready, provisioned Workspace."""


class RunNotReadyForRuntimeError(InvalidRunExecutionState):
    def __init__(self, run_id: UUID, status: RunStatus) -> None:
        super().__init__(f"run {run_id} must be running before runtime output is consumed")
        self.run_id = run_id
        self.status = status


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
        try:
            context, backend_name = await self._load_context(run_id)
        except InvalidRunExecutionState:
            raise
        except Exception as error:
            await self._record_failure(run_id, error)
            raise
        try:
            backend = self._backends[backend_name]
            backend_session = await backend.start(context)
            async for event in backend.events(backend_session):
                async with self._sessions.begin() as session:
                    await self._store.append_event(session, event)

            await self._complete(run_id)
        except Exception as error:
            await self._record_failure(run_id, error)
            raise

    async def execute_runtime(
        self,
        run_id: UUID,
        runtime: Runtime,
        handle: ContainerHandle,
    ) -> None:
        """Finish an already-running Run from its started container output."""
        try:
            await self._require_running(run_id)
        except InvalidRunExecutionState:
            raise
        except Exception as error:
            await self._record_failure(run_id, error)
            raise
        try:
            await RuntimeEventIngestor(self._sessions, self._store).ingest(
                run_id,
                runtime,
                handle,
            )
            await self._complete(run_id)
        except Exception as error:
            await self._record_failure(run_id, error)
            raise

    async def _require_running(self, run_id: UUID) -> None:
        async with self._sessions.begin() as session:
            run = await session.get(RunRecord, run_id)
            if run is None:
                raise InvalidRunExecutionState(f"run {run_id} is not available for execution")
            status = RunStatus(run.status)
            if status is not RunStatus.RUNNING:
                raise RunNotReadyForRuntimeError(run_id, status)

    async def _complete(self, run_id: UUID) -> None:
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

    async def _record_failure(self, run_id: UUID, error: Exception) -> None:
        try:
            await self._fail(run_id, error)
        except Exception as persistence_error:
            try:
                error.add_note(
                    f"failed to persist Run execution failure "
                    f"({type(persistence_error).__name__})"
                )
            except Exception:
                return

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
            error_projection = _safe_error_projection(error)
            await self._store.transition(
                session,
                run_id,
                RunStatus.FAILED,
                error=error_projection,
            )
            await self._store.append_event(
                session,
                EventEnvelope(
                    run_id=run_id,
                    type=EventType.RUN_FAILED,
                    source="worker",
                    data={"error": error_projection},
                    raw=(
                        error.raw
                        if isinstance(error, (BackendProtocolError, BackendReportedError))
                        else None
                    ),
                ),
            )
