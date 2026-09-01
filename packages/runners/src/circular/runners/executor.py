from uuid import UUID

from circular.agents import AgentBackend, BackendContext
from circular.domain import RunStatus
from circular.events import EventEnvelope, EventType
from circular.storage.models import AgentRecord, RunRecord, TaskRecord
from circular.storage.repositories import RunStore
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker


class RunExecutor:
    """Coordinates one claimed Run; backend reasoning remains behind AgentBackend."""

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
            backend = self._backends[backend_name]
            async with self._sessions.begin() as session:
                await self._store.transition(session, run_id, RunStatus.RUNNING)
                await self._store.append_event(
                    session,
                    EventEnvelope(
                        run_id=run_id,
                        type=EventType.RUN_STARTED,
                        source="worker",
                        data={"backend": backend_name},
                    ),
                )

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
                    select(RunRecord, TaskRecord, AgentRecord)
                    .join(TaskRecord, TaskRecord.id == RunRecord.task_id)
                    .join(AgentRecord, AgentRecord.id == RunRecord.agent_id)
                    .where(RunRecord.id == run_id)
                )
            ).one()
            run, task, agent = row
            return (
                BackendContext(
                    run_id=run.id,
                    task_title=task.title,
                    task_description=task.description,
                    instructions=agent.instructions,
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
