import asyncio
import logging
from contextlib import suppress

from circular.git import LocalRepositoryCache, LocalWorktreeManager
from circular.runners import (
    FakeWorkloadSpecFactory,
    RunExecutor,
    SqlWorkspaceProvisioningPersistence,
    WorkspaceProvisioner,
)
from circular.runtimes import DockerRuntime
from circular.storage import RunStore, WorkspaceStore, create_engine, create_session_factory
from circular.worker.config import Settings

logger = logging.getLogger(__name__)


async def worker_loop(settings: Settings, stop: asyncio.Event | None = None) -> None:
    engine = create_engine(settings.database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    directories = settings.execution_directories
    runtime = DockerRuntime(directories.docker_worktree_root)
    provisioner = WorkspaceProvisioner(
        persistence=SqlWorkspaceProvisioningPersistence(
            sessions,
            store,
            WorkspaceStore(),
        ),
        repository_cache=LocalRepositoryCache(directories),
        worktrees=LocalWorktreeManager(directories),
        runtime=runtime,
        directories=directories,
        spec_factory=FakeWorkloadSpecFactory(
            image=settings.runner_image,
            cpu_limit=settings.runner_cpu_limit,
            memory_limit_mb=settings.runner_memory_limit_mb,
            delay_ms=round(settings.fake_delay_seconds * 1000),
        ),
    )
    executor = RunExecutor(
        sessions,
        store,
        {},
    )
    stop = stop or asyncio.Event()
    try:
        while not stop.is_set():
            async with sessions.begin() as session:
                claimed = await store.claim_next(session, settings.worker_id)
                run_id = claimed.id if claimed is not None else None
            if run_id is None:
                with suppress(TimeoutError):
                    await asyncio.wait_for(stop.wait(), timeout=settings.poll_interval_seconds)
                continue
            try:
                provisioned = await provisioner.provision(run_id)
                workspace = provisioned.workspace
                logger.debug(
                    "workspace ready",
                    extra={
                        "run_id": str(run_id),
                        "container_id": workspace.container_id,
                        "runtime_handle_id": provisioned.handle.id,
                    },
                )
                await executor.execute_runtime(run_id, runtime, provisioned.handle)
            except Exception:
                logger.exception("run execution failed", extra={"run_id": str(run_id)})
    finally:
        await engine.dispose()


def run() -> None:
    logging.basicConfig(level=logging.INFO)
    asyncio.run(worker_loop(Settings()))
