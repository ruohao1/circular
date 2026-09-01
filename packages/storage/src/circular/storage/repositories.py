from datetime import UTC, datetime
from uuid import UUID

from circular.domain import Artifact, RunStatus, Workspace, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.orchestration import RunLifecycle, WorkspaceLifecycle
from circular.storage.models import ArtifactRecord, EventRecord, RunRecord, WorkspaceRecord
from sqlalchemy import func, select
from sqlalchemy.dialects.postgresql import insert
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker


class RunNotFoundError(LookupError):
    pass


class WorkspaceNotFoundError(LookupError):
    pass


class WorkspaceAlreadyExistsError(ValueError):
    def __init__(self, run_id: UUID) -> None:
        super().__init__(f"run {run_id} already owns a workspace")
        self.run_id = run_id


class WorkspaceContainerIdConflictError(ValueError):
    def __init__(self, workspace_id: UUID, existing: str, requested: str) -> None:
        super().__init__(
            f"workspace {workspace_id} already records container {existing}; "
            f"cannot replace it with {requested}"
        )
        self.workspace_id = workspace_id
        self.existing = existing
        self.requested = requested


async def _lock_run(session: AsyncSession, run_id: UUID) -> RunRecord:
    """Acquire the first row lock for any write owned by a Run."""
    run = await session.scalar(select(RunRecord).where(RunRecord.id == run_id).with_for_update())
    if run is None:
        raise RunNotFoundError(str(run_id))
    return run


async def _append_event_for_locked_run(
    session: AsyncSession,
    run: RunRecord,
    envelope: EventEnvelope,
) -> EventRecord:
    """Allocate a sequence after `_lock_run` has serialized this Run's writers."""
    if run.id != envelope.run_id:
        raise ValueError("event run does not match the locked run")

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


class RunEventReader:
    """Read ordered Run events without exposing session lifetime to callers."""

    def __init__(self, sessions: async_sessionmaker[AsyncSession]) -> None:
        self._sessions = sessions

    async def run_exists(self, run_id: UUID) -> bool:
        async with self._sessions() as session:
            return await session.get(RunRecord, run_id) is not None

    async def read_after(
        self,
        run_id: UUID,
        after: int,
        *,
        limit: int = 200,
    ) -> tuple[EventRecord, ...]:
        statement = (
            select(EventRecord)
            .where(EventRecord.run_id == run_id, EventRecord.sequence > after)
            .order_by(EventRecord.sequence)
            .limit(limit)
        )
        async with self._sessions() as session:
            return tuple(await session.scalars(statement))


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
        run = await _lock_run(session, run_id)

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
        run = await _lock_run(session, envelope.run_id)
        return await _append_event_for_locked_run(session, run, envelope)


class WorkspaceStore:
    """Persist Workspace lifecycle facts inside a caller-owned transaction.

    Writes lock the owning Run first and flush for immediate use, but this store never
    commits or rolls back.
    """

    _transition_events = {
        WorkspaceStatus.READY: EventType.WORKSPACE_READY,
        WorkspaceStatus.RELEASED: EventType.WORKSPACE_RELEASED,
        WorkspaceStatus.FAILED: EventType.WORKSPACE_FAILED,
    }

    async def create(
        self,
        session: AsyncSession,
        workspace: Workspace,
        *,
        source: str,
    ) -> Workspace:
        WorkspaceLifecycle.validate_initial(
            workspace.status,
            container_id=workspace.container_id,
        )
        run = await _lock_run(session, workspace.run_id)
        statement = (
            insert(WorkspaceRecord)
            .values(
                id=workspace.id,
                run_id=workspace.run_id,
                worktree_path=workspace.worktree_path,
                container_id=workspace.container_id,
                status=workspace.status.value,
            )
            .on_conflict_do_nothing(index_elements=[WorkspaceRecord.run_id])
            .returning(WorkspaceRecord)
        )
        record = (await session.scalars(statement)).one_or_none()
        if record is None:
            raise WorkspaceAlreadyExistsError(workspace.run_id)
        await _append_event_for_locked_run(
            session,
            run,
            EventEnvelope(
                run_id=workspace.run_id,
                type=EventType.WORKSPACE_PROVISIONING,
                source=source,
                data={"status": WorkspaceStatus.PENDING.value, "workspace_id": str(workspace.id)},
            ),
        )
        return self._to_domain(record)

    async def load(self, session: AsyncSession, workspace_id: UUID) -> Workspace:
        record = await session.get(WorkspaceRecord, workspace_id)
        if record is None:
            raise WorkspaceNotFoundError(str(workspace_id))
        return self._to_domain(record)

    async def load_for_run(self, session: AsyncSession, run_id: UUID) -> Workspace:
        record = await session.scalar(
            select(WorkspaceRecord).where(WorkspaceRecord.run_id == run_id)
        )
        if record is None:
            raise WorkspaceNotFoundError(str(run_id))
        return self._to_domain(record)

    async def list(
        self,
        session: AsyncSession,
        *,
        status: WorkspaceStatus | None = None,
    ) -> tuple[Workspace, ...]:
        """Return Workspaces oldest first, breaking timestamp ties by identifier."""
        statement = select(WorkspaceRecord)
        if status is not None:
            statement = statement.where(WorkspaceRecord.status == status.value)
        records = await session.scalars(
            statement.order_by(WorkspaceRecord.created_at, WorkspaceRecord.id)
        )
        return tuple(self._to_domain(record) for record in records)

    async def transition(
        self,
        session: AsyncSession,
        workspace_id: UUID,
        target: WorkspaceStatus,
        *,
        source: str,
        container_id: str | None = None,
    ) -> Workspace:
        run_id = await session.scalar(
            select(WorkspaceRecord.run_id).where(WorkspaceRecord.id == workspace_id)
        )
        if run_id is None:
            raise WorkspaceNotFoundError(str(workspace_id))
        run = await _lock_run(session, run_id)
        record = await session.scalar(
            select(WorkspaceRecord).where(WorkspaceRecord.id == workspace_id).with_for_update()
        )
        if record is None:
            raise WorkspaceNotFoundError(str(workspace_id))

        WorkspaceLifecycle.validate(WorkspaceStatus(record.status), target)
        if container_id is not None:
            if target not in {WorkspaceStatus.READY, WorkspaceStatus.FAILED}:
                raise ValueError(
                    "container_id can only be recorded when a workspace becomes ready or failed"
                )
            if record.container_id is not None and record.container_id != container_id:
                raise WorkspaceContainerIdConflictError(
                    record.id,
                    record.container_id,
                    container_id,
                )
            record.container_id = container_id
        record.status = target.value
        event_data = {"status": target.value, "workspace_id": str(record.id)}
        if record.container_id is not None:
            event_data["container_id"] = record.container_id
        await _append_event_for_locked_run(
            session,
            run,
            EventEnvelope(
                run_id=record.run_id,
                type=self._transition_events[target],
                source=source,
                data=event_data,
            ),
        )
        return self._to_domain(record)

    @staticmethod
    def _to_domain(record: WorkspaceRecord) -> Workspace:
        return Workspace(
            id=record.id,
            run_id=record.run_id,
            worktree_path=record.worktree_path,
            container_id=record.container_id,
            status=WorkspaceStatus(record.status),
        )


class ArtifactStore:
    """Append and read Run artifacts inside a caller-owned transaction.

    Writes lock the owning Run first and flush for immediate use, but this store never
    commits or rolls back.
    """

    async def append(
        self,
        session: AsyncSession,
        artifact: Artifact,
        *,
        source: str,
    ) -> Artifact:
        run = await _lock_run(session, artifact.run_id)
        record = ArtifactRecord(
            id=artifact.id,
            run_id=artifact.run_id,
            kind=artifact.kind,
            uri=artifact.uri,
            artifact_metadata=dict(artifact.metadata),
        )
        session.add(record)
        await session.flush()
        await _append_event_for_locked_run(
            session,
            run,
            EventEnvelope(
                run_id=artifact.run_id,
                type=EventType.ARTIFACT_CREATED,
                source=source,
                data={
                    "artifact_id": str(artifact.id),
                    "kind": artifact.kind,
                    "uri": artifact.uri,
                },
            ),
        )
        return self._to_domain(record)

    async def list_for_run(
        self,
        session: AsyncSession,
        run_id: UUID,
    ) -> tuple[Artifact, ...]:
        """Return a Run's Artifacts oldest first, breaking timestamp ties by identifier."""
        records = await session.scalars(
            select(ArtifactRecord)
            .where(ArtifactRecord.run_id == run_id)
            .order_by(ArtifactRecord.created_at, ArtifactRecord.id)
        )
        return tuple(self._to_domain(record) for record in records)

    @staticmethod
    def _to_domain(record: ArtifactRecord) -> Artifact:
        return Artifact(
            id=record.id,
            run_id=record.run_id,
            kind=record.kind,
            uri=record.uri,
            metadata=dict(record.artifact_metadata),
        )
