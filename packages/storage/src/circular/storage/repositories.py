from datetime import UTC, datetime
from uuid import UUID

from circular.domain import RunStatus
from circular.events import EventEnvelope
from circular.orchestration import RunLifecycle
from circular.storage.models import EventRecord, RunRecord
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession


class RunNotFoundError(LookupError):
    pass


class RunStore:
    """Transactional interface for claiming, transitioning, and observing Runs."""

    async def claim_next(self, session: AsyncSession, worker_id: str) -> RunRecord | None:
        statement = self.claim_statement()
        run = await session.scalar(statement)
        if run is None:
            return None

        RunLifecycle.validate(RunStatus(run.status), RunStatus.PROVISIONING)
        run.status = RunStatus.PROVISIONING.value
        run.worker_id = worker_id
        run.claimed_at = datetime.now(UTC)
        await session.flush()
        return run

    @staticmethod
    def claim_statement():
        return (
            select(RunRecord)
            .where(RunRecord.status == RunStatus.QUEUED.value)
            .order_by(RunRecord.created_at, RunRecord.id)
            .with_for_update(skip_locked=True)
            .limit(1)
        )

    async def transition(
        self,
        session: AsyncSession,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> RunRecord:
        run = await session.scalar(
            select(RunRecord).where(RunRecord.id == run_id).with_for_update()
        )
        if run is None:
            raise RunNotFoundError(str(run_id))

        RunLifecycle.validate(RunStatus(run.status), target)
        run.status = target.value
        now = datetime.now(UTC)
        if target is RunStatus.RUNNING and run.started_at is None:
            run.started_at = now
        if RunLifecycle.is_terminal(target):
            run.finished_at = now
        run.error = error
        await session.flush()
        return run

    async def append_event(self, session: AsyncSession, envelope: EventEnvelope) -> EventRecord:
        # Locking the owning Run serializes sequence allocation without a second queue system.
        run = await session.scalar(
            select(RunRecord.id).where(RunRecord.id == envelope.run_id).with_for_update()
        )
        if run is None:
            raise RunNotFoundError(str(envelope.run_id))
        last_sequence = await session.scalar(
            select(func.coalesce(func.max(EventRecord.sequence), 0)).where(
                EventRecord.run_id == envelope.run_id
            )
        )
        record = EventRecord(
            id=envelope.id,
            run_id=envelope.run_id,
            sequence=int(last_sequence or 0) + 1,
            type=envelope.type.value,
            source=envelope.source,
            data=envelope.data,
            raw=envelope.raw,
            occurred_at=envelope.occurred_at,
        )
        session.add(record)
        await session.flush()
        return record
