from __future__ import annotations

import asyncio
import os
import stat
import tarfile
import tempfile
from pathlib import Path
from typing import BinaryIO
from uuid import NAMESPACE_URL, UUID, uuid5

from circular.domain import Artifact, RunStatus, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.git import LocalWorktreeManager, ProvisionedWorktree
from circular.runners.finalization import RunFinalizer
from circular.runners.paths import ExecutionDirectories
from circular.runtimes import DockerRuntime
from circular.storage import ArtifactStore, LocalArtifactContentStore, RunStore, WorkspaceStore
from circular.storage.models import RunRecord, TaskRecord, WorkspaceRecord
from circular.storage.repositories import RunLeaseLostError
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker


def archive_worktree(path: Path, destination: BinaryIO) -> None:
    """Retain output (including ignored files) without following worktree symlinks."""

    def fail_walk(error: OSError) -> None:
        raise error

    with tarfile.open(fileobj=destination, mode="w|", dereference=False) as archive:
        for directory, subdirectories, files in os.walk(path, followlinks=False, onerror=fail_walk):
            subdirectories[:] = sorted(name for name in subdirectories if name != ".git")
            for name in sorted([*subdirectories, *files]):
                if name == ".git":
                    continue
                source = Path(directory) / name
                info = source.lstat()
                if not (
                    stat.S_ISREG(info.st_mode)
                    or stat.S_ISDIR(info.st_mode)
                    or stat.S_ISLNK(info.st_mode)
                ):
                    raise ValueError("worktree contains an unsupported special file")
                archive.add(source, arcname=str(source.relative_to(path)), recursive=False)


class RunResourceCleaner:
    """Stop runtime resources, retain output, and release the exact owned worktree."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        runtime: DockerRuntime,
        worktrees: LocalWorktreeManager,
        directories: ExecutionDirectories,
        finalizer: RunFinalizer,
    ) -> None:
        self.sessions = sessions
        self.runtime = runtime
        self.worktrees = worktrees
        self.directories = directories
        self.finalizer = finalizer

    async def cleanup(self, run_id: UUID) -> bool:
        workspace_id = None
        try:
            async with self.sessions.begin() as session:
                run = await RunStore().lock_for_execution(session, run_id)
                if RunStatus(run.status) not in {
                    RunStatus.SUCCEEDED,
                    RunStatus.FAILED,
                    RunStatus.CANCELLED,
                }:
                    return False
                workspace = await session.scalar(
                    select(WorkspaceRecord).where(WorkspaceRecord.run_id == run_id)
                )
                if workspace is not None:
                    workspace_id = workspace.id
                    if workspace.status == WorkspaceStatus.RELEASED.value:
                        return True
                repository_id = await session.scalar(
                    select(TaskRecord.repository_id)
                    .join(RunRecord, RunRecord.task_id == TaskRecord.id)
                    .where(RunRecord.id == run_id)
                )
                await self.runtime.release(run_id, workspace.container_id if workspace else None)
            if workspace is None:
                return True
            target = self.directories.worktree_root / str(run_id)
            if Path(workspace.worktree_path) != target or target.is_symlink():
                raise ValueError("Workspace does not identify its owned Run directory")
            if repository_id is not None:
                if target.exists():
                    await self._retain_output(run_id, target)
                repository = self.directories.repository_cache_path(repository_id)
                if repository.exists():
                    async with self.sessions.begin() as session:
                        await RunStore().lock_for_execution(session, run_id)
                        await self.worktrees.release(
                            ProvisionedWorktree(
                                run_id, repository, target, f"circular/run/{run_id}"
                            ),
                            discard_changes=True,
                        )
                elif target.exists():
                    raise ValueError("Workspace Repository ownership cannot be verified")
            async with self.sessions.begin() as session:
                store = WorkspaceStore()
                current = await store.load(session, workspace.id)
                if current.status is WorkspaceStatus.PENDING:
                    await store.transition(
                        session, workspace.id, WorkspaceStatus.FAILED, source="worker-cleanup"
                    )
                if current.status is not WorkspaceStatus.RELEASED:
                    await store.transition(
                        session, workspace.id, WorkspaceStatus.RELEASED, source="worker-cleanup"
                    )
            return True
        except RunLeaseLostError:
            # The replacement worker owns reconciliation and its audit events.
            return False
        except Exception as error:
            if workspace_id is not None:
                await self._record_error(run_id, workspace_id, error)
            return False

    async def _retain_output(self, run_id: UUID, target: Path) -> None:
        async with self.sessions() as session:
            artifacts = await ArtifactStore().list_for_run(session, run_id)
        if not any(artifact.kind == "diff" for artifact in artifacts):
            await self.finalizer.finalize(run_id)
        archive_id = uuid5(NAMESPACE_URL, f"io.circular.artifact:{run_id}:worktree")
        if any(artifact.id == archive_id for artifact in artifacts):
            return
        # Disk-backed streaming also keeps the event loop free to renew the lease.
        stored = await asyncio.to_thread(self._publish_archive, run_id, target)
        async with self.sessions.begin() as session:
            await ArtifactStore().append(
                session,
                Artifact(
                    id=archive_id,
                    run_id=run_id,
                    kind="workspace",
                    uri=stored.uri,
                    metadata={
                        "media_type": "application/x-tar",
                        "size_bytes": stored.size_bytes,
                        "sha256": stored.sha256,
                    },
                ),
                source="worker-cleanup",
            )

    def _publish_archive(self, run_id: UUID, target: Path):
        with tempfile.TemporaryFile() as content:
            archive_worktree(target, content)
            content.seek(0)
            return LocalArtifactContentStore(self.directories.artifact_root).write_stream(
                run_id, "worktree.tar", content
            )

    async def _record_error(self, run_id: UUID, workspace_id: UUID, error: Exception) -> None:
        from circular.runners.executor import _safe_error_projection

        async with self.sessions.begin() as session:
            store = WorkspaceStore()
            workspace = await store.load(session, workspace_id)
            if workspace.status in {WorkspaceStatus.PENDING, WorkspaceStatus.READY}:
                await store.transition(
                    session, workspace_id, WorkspaceStatus.FAILED, source="worker-cleanup"
                )
            await RunStore().append_event(
                session,
                EventEnvelope(
                    run_id=run_id,
                    type=EventType.WORKSPACE_FAILED,
                    source="worker-cleanup",
                    data={
                        "stage": "cleanup",
                        "workspace_id": str(workspace_id),
                        "error": _safe_error_projection(error),
                    },
                ),
            )
