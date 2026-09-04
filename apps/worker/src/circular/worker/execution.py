"""Production execution composition, also used by the isolated integration tests."""

import os

from circular.git import LocalGitDiffCollector, LocalRepositoryCache, LocalWorktreeManager
from circular.runners import (
    ExecutionDirectories,
    RunExecutor,
    RunFinalizer,
    SqlRunFinalizationPersistence,
    SqlWorkspaceProvisioningPersistence,
    WorkspaceProvisioner,
)
from circular.runners.cleanup import RunResourceCleaner
from circular.runners.provisioning import ContainerSpecFactory
from circular.runners.supervisor import RunSupervisor
from circular.runtimes import DockerRuntime
from circular.storage import (
    ArtifactStore,
    LocalArtifactContentStore,
    RunStore,
    WorkspaceStore,
    create_session_factory,
)
from sqlalchemy.ext.asyncio import AsyncEngine


def build_supervisor(
    engine: AsyncEngine,
    directories: ExecutionDirectories,
    worker_id: str,
    *,
    spec_factory: ContainerSpecFactory,
    poll_seconds: float = 0.25,
) -> RunSupervisor:
    sessions = create_session_factory(engine)
    sessions.configure(info={"worker_id": worker_id})
    store = RunStore()
    uid, gid = (os.getuid(), os.getgid()) if os.getuid() else (65532, 65532)
    runtime = DockerRuntime(directories.docker_worktree_root, container_user=f"{uid}:{gid}")
    worktrees = LocalWorktreeManager(directories, owner=(uid, gid) if os.getuid() == 0 else None)
    provisioner = WorkspaceProvisioner(
        persistence=SqlWorkspaceProvisioningPersistence(sessions, store, WorkspaceStore()),
        repository_cache=LocalRepositoryCache(directories),
        worktrees=worktrees,
        runtime=runtime,
        directories=directories,
        spec_factory=spec_factory,
    )
    finalizer = RunFinalizer(
        SqlRunFinalizationPersistence(sessions, store, ArtifactStore()),
        LocalGitDiffCollector(directories.repository_cache_root),
        LocalArtifactContentStore(directories.artifact_root),
        directories,
    )
    return RunSupervisor(
        sessions,
        store,
        provisioner,
        RunExecutor(sessions, store, {}, finalizer),
        runtime,
        RunResourceCleaner(sessions, runtime, worktrees, directories, finalizer),
        worker_id,
        poll_seconds=poll_seconds,
    )
