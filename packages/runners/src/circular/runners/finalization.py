from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Protocol
from uuid import NAMESPACE_URL, UUID, uuid5

from circular.domain import Artifact, RunStatus, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.git import GitDiff, GitDiffCollector
from circular.runners.paths import ExecutionDirectories
from circular.storage import ArtifactContentStore, ArtifactStore
from circular.storage.models import RunRecord, WorkspaceRecord
from circular.storage.repositories import RunStore
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

_DIFF_ARTIFACT_NAME = "git-diff.patch"


def diff_artifact_id_for_run(run_id: UUID) -> UUID:
    return uuid5(NAMESPACE_URL, f"io.circular.artifact:{run_id}:git-diff")


@dataclass(frozen=True, slots=True)
class RunFinalizationContext:
    run_id: UUID
    worktree_path: Path


class InvalidRunFinalizationState(ValueError):
    """A Run or Workspace is not in the state required for final capture."""


class RunFinalizationPersistence(Protocol):
    async def load_context(self, run_id: UUID) -> RunFinalizationContext: ...

    async def persist_diff(self, artifact: Artifact, diff: GitDiff) -> None: ...


class SqlRunFinalizationPersistence:
    """PostgreSQL adapter for finalization context and atomic artifact events."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        run_store: RunStore,
        artifact_store: ArtifactStore,
        *,
        source: str = "worker",
    ) -> None:
        if not isinstance(source, str) or not source:
            raise ValueError("finalization event source must be a non-empty string")
        self._sessions = sessions
        self._run_store = run_store
        self._artifact_store = artifact_store
        self._source = source

    async def load_context(self, run_id: UUID) -> RunFinalizationContext:
        async with self._sessions() as session:
            row = (
                await session.execute(
                    select(RunRecord, WorkspaceRecord)
                    .outerjoin(WorkspaceRecord, WorkspaceRecord.run_id == RunRecord.id)
                    .where(RunRecord.id == run_id)
                )
            ).one_or_none()
        if row is None:
            raise InvalidRunFinalizationState(f"run {run_id} is unavailable for finalization")
        run, workspace = row
        if RunStatus(run.status) not in {
            RunStatus.FINALIZING,
            RunStatus.SUCCEEDED,
            RunStatus.FAILED,
            RunStatus.CANCELLED,
        }:
            raise InvalidRunFinalizationState(
                f"run {run_id} must be finalizing before artifact capture"
            )
        if workspace is None or WorkspaceStatus(workspace.status) is WorkspaceStatus.RELEASED:
            raise InvalidRunFinalizationState(
                f"run {run_id} requires a ready Workspace for artifact capture"
            )
        return RunFinalizationContext(run_id=run.id, worktree_path=Path(workspace.worktree_path))

    async def persist_diff(self, artifact: Artifact, diff: GitDiff) -> None:
        event_data = {
            "artifact_id": str(artifact.id),
            "uri": artifact.uri,
            **artifact.metadata,
        }
        async with self._sessions.begin() as session:
            run = await self._run_store.lock_for_execution(session, artifact.run_id)
            if RunStatus(run.status) not in {
                RunStatus.FINALIZING,
                RunStatus.SUCCEEDED,
                RunStatus.FAILED,
                RunStatus.CANCELLED,
            }:
                raise InvalidRunFinalizationState("Run is no longer finalizable")
            existing = await self._artifact_store.list_for_run(session, artifact.run_id)
            if any(item.id == artifact.id for item in existing):
                return
            await self._artifact_store.append(session, artifact, source=self._source)
            await self._run_store.append_event(
                session,
                EventEnvelope(
                    run_id=artifact.run_id,
                    type=EventType.GIT_DIFF_UPDATED,
                    source=self._source,
                    data=event_data,
                ),
            )


class RunFinalizer:
    """Capture and durably publish one Run's final Git diff."""

    def __init__(
        self,
        persistence: RunFinalizationPersistence,
        diff_collector: GitDiffCollector,
        content_store: ArtifactContentStore,
        directories: ExecutionDirectories,
    ) -> None:
        self._persistence = persistence
        self._diff_collector = diff_collector
        self._content_store = content_store
        self._directories = directories

    async def finalize(self, run_id: UUID) -> Artifact:
        context = await self._persistence.load_context(run_id)
        if context.run_id != run_id:
            raise InvalidRunFinalizationState("finalization context belongs to another Run")
        expected_path = self._directories.run_paths(run_id).worktree
        if context.worktree_path.resolve(strict=False) != expected_path:
            raise InvalidRunFinalizationState(
                f"run {run_id} Workspace is outside its managed worktree path"
            )

        diff = await self._diff_collector.capture(expected_path)
        stored = await self._content_store.write(run_id, _DIFF_ARTIFACT_NAME, diff.content)
        artifact = Artifact(
            id=diff_artifact_id_for_run(run_id),
            run_id=run_id,
            kind="diff",
            uri=stored.uri,
            metadata={
                "media_type": "text/x-diff",
                "size_bytes": stored.size_bytes,
                "sha256": stored.sha256,
                "changed_files": diff.changed_files,
                "contains_binary": diff.contains_binary,
                "empty": diff.empty,
            },
        )
        await self._persistence.persist_diff(artifact, diff)
        return artifact
