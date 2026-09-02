from __future__ import annotations

import asyncio
import json
import math
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol
from uuid import UUID

from circular.domain import Workspace
from circular.git import RepositoryCache, WorktreeManager
from circular.runners.paths import ExecutionDirectories
from circular.runtimes import ContainerHandle, ContainerSpec, Runtime


@dataclass(frozen=True, slots=True)
class ProvisionedWorkspace:
    """Durable Workspace state paired with its original live runtime handle."""

    workspace: Workspace
    handle: ContainerHandle


class WorkspaceProvisioningCompensationError(RuntimeError):
    """An uncommitted runtime allocation could not be safely released."""

    def __init__(self, original_error: BaseException) -> None:
        super().__init__("uncommitted runtime allocation could not be safely discarded")
        self.original_error = original_error


class ContainerIdentityPersistenceError(RuntimeError):
    """The owned durable-identity write ended without a committed result."""

    def __init__(self, cancellation: asyncio.CancelledError) -> None:
        super().__init__("container identity persistence was cancelled")
        self.__cause__ = cancellation


@dataclass(frozen=True, slots=True)
class _ContainerRecordOutcome:
    error: Exception | None = None


@dataclass(frozen=True, slots=True)
class WorkspaceProvisioningContext:
    """Stable database inputs needed to allocate one Run-owned Workspace."""

    run_id: UUID
    workspace_id: UUID
    repository_id: UUID
    clone_url: str
    base_ref: str
    backend: str
    task_title: str
    task_description: str
    instructions: str


class WorkspaceProvisioningPersistence(Protocol):
    """Transactional persistence operations used by ``WorkspaceProvisioner``."""

    async def load_context(self, run_id: UUID) -> WorkspaceProvisioningContext: ...

    async def create_pending(self, workspace: Workspace) -> Workspace: ...

    async def record_container(self, workspace_id: UUID, container_id: str) -> Workspace: ...

    async def mark_ready_and_running(self, workspace_id: UUID, backend: str) -> Workspace: ...

    async def mark_failed(
        self,
        run_id: UUID,
        error: Exception,
        *,
        container_id: str | None,
    ) -> None: ...


class ContainerSpecFactory(Protocol):
    def create(
        self,
        context: WorkspaceProvisioningContext,
        docker_host_worktree: Path,
    ) -> ContainerSpec: ...


@dataclass(frozen=True, slots=True)
class FakeWorkloadSpecFactory:
    """Build the deterministic version-1 fake workload request for one Run."""

    image: str
    cpu_limit: float
    memory_limit_mb: int
    delay_ms: int = 0

    def __post_init__(self) -> None:
        if not isinstance(self.image, str) or not self.image:
            raise ValueError("runner image must be a non-empty string")
        if (
            isinstance(self.cpu_limit, bool)
            or not isinstance(self.cpu_limit, int | float)
            or not math.isfinite(self.cpu_limit)
            or self.cpu_limit <= 0
        ):
            raise ValueError("runner CPU limit must be finite and positive")
        if (
            isinstance(self.memory_limit_mb, bool)
            or not isinstance(self.memory_limit_mb, int)
            or self.memory_limit_mb <= 0
        ):
            raise ValueError("runner memory limit must be a positive integer")
        if (
            isinstance(self.delay_ms, bool)
            or not isinstance(self.delay_ms, int)
            or not 0 <= self.delay_ms <= 10_000
        ):
            raise ValueError("fake workload delay must be an integer from 0 through 10000")

    def create(
        self,
        context: WorkspaceProvisioningContext,
        docker_host_worktree: Path,
    ) -> ContainerSpec:
        request = {
            "protocol_version": 1,
            "run": {
                "id": str(context.run_id),
                "task_title": context.task_title,
                "task_description": context.task_description,
                "instructions": context.instructions,
            },
            "behavior": {"delay_ms": self.delay_ms, "failure": "none"},
        }
        stdin = (
            json.dumps(request, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode(
                "utf-8"
            )
            + b"\n"
        )
        return ContainerSpec(
            run_id=context.run_id,
            image=self.image,
            worktree=docker_host_worktree,
            command=(),
            stdin=stdin,
            cpu_limit=float(self.cpu_limit),
            memory_limit_mb=self.memory_limit_mb,
        )


class WorkspaceProvisioner:
    """Allocate one claimed Run's checkout, worktree, and container in order."""

    def __init__(
        self,
        *,
        persistence: WorkspaceProvisioningPersistence,
        repository_cache: RepositoryCache,
        worktrees: WorktreeManager,
        runtime: Runtime,
        directories: ExecutionDirectories,
        spec_factory: ContainerSpecFactory,
    ) -> None:
        self._persistence = persistence
        self._repository_cache = repository_cache
        self._worktrees = worktrees
        self._runtime = runtime
        self._directories = directories
        self._spec_factory = spec_factory

    async def provision(self, run_id: UUID) -> ProvisionedWorkspace:
        handle: ContainerHandle | None = None
        resource_id: str | None = None
        identity_recorded = False
        try:
            context = await self._persistence.load_context(run_id)
            if context.run_id != run_id:
                raise ValueError("context does not belong to the requested Run")
            paths = self._directories.run_paths(run_id)
            await self._persistence.create_pending(
                Workspace(
                    id=context.workspace_id,
                    run_id=run_id,
                    worktree_path=str(paths.worktree),
                )
            )

            repository_path = await self._repository_cache.checkout(
                context.repository_id,
                context.clone_url,
            )
            if repository_path != self._directories.repository_cache_path(context.repository_id):
                raise ValueError("cache returned a path outside the managed Repository checkout")
            worktree = await self._worktrees.provision(
                run_id,
                repository_path,
                context.base_ref,
            )
            if worktree.run_id != run_id:
                raise ValueError("worktree does not belong to the requested Run")
            if worktree.repository_path != repository_path:
                raise ValueError("worktree references a different Repository checkout")
            if worktree.path != paths.worktree:
                raise ValueError("worktree manager returned a path not owned by the Run")
            if worktree.branch != f"circular/run/{run_id}":
                raise ValueError("worktree manager returned an unexpected Run branch")

            spec = self._spec_factory.create(context, paths.docker_host_worktree)
            started = await self._runtime.start(spec)
            if not isinstance(started, ContainerHandle):
                raise ValueError("runtime returned an invalid container identity")
            handle = started
            if (
                isinstance(handle.resource_id, str)
                and handle.resource_id
                and len(handle.resource_id) <= 200
            ):
                resource_id = handle.resource_id
            if (
                not isinstance(handle.id, str)
                or not handle.id
                or len(handle.id) > 200
                or resource_id is None
            ):
                raise ValueError("runtime returned an invalid container identity")
            recording = asyncio.create_task(
                self._record_container(context.workspace_id, resource_id)
            )
            try:
                outcome = await asyncio.shield(recording)
            except asyncio.CancelledError as cancelled:
                outcome = await _await_task_despite_cancellation(recording)
                if outcome.error is not None:
                    persistence_error = outcome.error
                    cancelled.add_note(
                        "failed to persist started container during cancellation "
                        f"({type(persistence_error).__name__})"
                    )
                    failure_persisted = await self._try_mark_failed(
                        run_id,
                        persistence_error,
                        resource_id=resource_id,
                        note_target=cancelled,
                        note_context="during cancellation",
                    )
                    if not failure_persisted:
                        await self._discard_uncommitted(
                            handle,
                            note_target=cancelled,
                            note_context="during cancellation",
                        )
                raise
            if outcome.error is not None:
                raise outcome.error
            identity_recorded = True
            workspace = await self._persistence.mark_ready_and_running(
                context.workspace_id,
                context.backend,
            )
            return ProvisionedWorkspace(workspace=workspace, handle=handle)
        except WorkspaceProvisioningCompensationError:
            raise
        except Exception as error:
            failure_persisted = await self._try_mark_failed(
                run_id,
                error,
                resource_id=resource_id,
                note_target=error,
            )
            identity_is_durable = identity_recorded or (
                resource_id is not None and failure_persisted
            )
            if handle is not None and not identity_is_durable:
                await self._discard_uncommitted(handle, note_target=error)
            raise

    async def _record_container(
        self,
        workspace_id: UUID,
        resource_id: str,
    ) -> _ContainerRecordOutcome:
        try:
            await self._persistence.record_container(workspace_id, resource_id)
        except asyncio.CancelledError as cancellation:
            return _ContainerRecordOutcome(error=ContainerIdentityPersistenceError(cancellation))
        except Exception as error:
            return _ContainerRecordOutcome(error=error)
        return _ContainerRecordOutcome()

    async def _try_mark_failed(
        self,
        run_id: UUID,
        error: Exception,
        *,
        resource_id: str | None,
        note_target: BaseException,
        note_context: str = "",
    ) -> bool:
        persistence = asyncio.create_task(
            self._persistence.mark_failed(
                run_id,
                error,
                container_id=resource_id,
            )
        )
        try:
            await _await_task_despite_cancellation(persistence)
        except (asyncio.CancelledError, Exception) as persistence_error:
            suffix = f" {note_context}" if note_context else ""
            note_target.add_note(
                "failed to persist provisioning failure"
                f"{suffix} ({type(persistence_error).__name__})"
            )
            return False
        return True

    async def _discard_uncommitted(
        self,
        handle: ContainerHandle,
        *,
        note_target: BaseException,
        note_context: str = "",
    ) -> None:
        discard = asyncio.create_task(self._runtime.discard(handle))
        try:
            await _await_task_despite_cancellation(discard)
        except (asyncio.CancelledError, Exception) as discard_error:
            suffix = f" {note_context}" if note_context else ""
            note_target.add_note(
                "failed to discard uncommitted runtime allocation"
                f"{suffix} ({type(discard_error).__name__})"
            )
            raise WorkspaceProvisioningCompensationError(note_target) from discard_error


async def _await_task_despite_cancellation[T](task: asyncio.Task[T]) -> T:
    """Finish owned identity persistence despite repeated caller cancellation."""
    while not task.done():
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError:
            continue
    return task.result()
