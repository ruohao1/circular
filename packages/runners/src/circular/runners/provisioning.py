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
from circular.runtimes import ContainerSpec, Runtime


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

    async def provision(self, run_id: UUID) -> Workspace:
        container_id: str | None = None
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
            handle = await self._runtime.start(spec)
            if not isinstance(handle.id, str) or not handle.id or len(handle.id) > 200:
                raise ValueError("runtime returned an invalid container identity")
            container_id = handle.id
            recording = asyncio.create_task(
                self._persistence.record_container(context.workspace_id, container_id)
            )
            try:
                await asyncio.shield(recording)
            except asyncio.CancelledError as cancelled:
                try:
                    await _await_task_despite_cancellation(recording)
                except Exception as persistence_error:
                    cancelled.add_note(
                        "failed to persist started container during cancellation "
                        f"({type(persistence_error).__name__})"
                    )
                raise
            return await self._persistence.mark_ready_and_running(
                context.workspace_id,
                context.backend,
            )
        except Exception as error:
            try:
                await self._persistence.mark_failed(
                    run_id,
                    error,
                    container_id=container_id,
                )
            except Exception as persistence_error:
                error.add_note(
                    f"failed to persist provisioning failure ({type(persistence_error).__name__})"
                )
            raise


async def _await_task_despite_cancellation[T](task: asyncio.Task[T]) -> T:
    """Finish owned identity persistence despite repeated caller cancellation."""
    while not task.done():
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError:
            continue
    return task.result()
