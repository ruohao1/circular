import asyncio
import logging
from contextlib import suppress
from uuid import UUID

from circular.domain import RunStatus
from circular.runners.cleanup import RunResourceCleaner
from circular.runners.executor import RunExecutor
from circular.runners.provisioning import WorkspaceProvisioner, _await_task_despite_cancellation
from circular.runtimes import DockerRuntime
from circular.storage import RunStore
from circular.storage.models import RunRecord
from circular.storage.repositories import RunLeaseLostError
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

logger = logging.getLogger(__name__)


class RunSupervisor:
    """Own one claimed execution, cancellation observation, lease, and finally cleanup."""

    def __init__(
        self,
        sessions: async_sessionmaker[AsyncSession],
        store: RunStore,
        provisioner: WorkspaceProvisioner,
        executor: RunExecutor,
        runtime: DockerRuntime,
        cleaner: RunResourceCleaner,
        worker_id: str,
        *,
        lease_seconds: float = 60,
        poll_seconds: float = 0.25,
    ) -> None:
        self.sessions = sessions
        self.store = store
        self.provisioner = provisioner
        self.executor = executor
        self.runtime = runtime
        self.cleaner = cleaner
        self.worker_id = worker_id
        self.lease_seconds = lease_seconds
        self.poll_seconds = poll_seconds

    async def run(self, run_id: UUID, stop: asyncio.Event, *, recovery: bool = False) -> None:
        async def execute() -> None:
            if recovery:
                return
            provisioned = await self.provisioner.provision(run_id)
            await self.executor.execute_runtime(run_id, self.runtime, provisioned.handle)

        execution = asyncio.create_task(execute())
        monitor = asyncio.create_task(self._watch(run_id, execution, stop))
        execution_error = RuntimeError("execution ended without a terminal outcome")
        try:
            await execution
        except asyncio.CancelledError:
            # API cancellation already owns the terminal decision; shutdown is a
            # failed attempt, so no abandoned Run remains active after cleanup.
            execution_error = RuntimeError("worker execution stopped")
        except Exception as error:
            execution_error = error
            logger.exception("run execution failed", extra={"run_id": str(run_id)})
        finally:
            execution.cancel()
            with suppress(asyncio.CancelledError, Exception):
                await execution

            async def settle() -> bool:
                # Retry a failed terminal write before cleanup. If persistence is
                # still unavailable, cleanup/claim guards leave recovery ownership.
                await self.executor._record_failure(run_id, execution_error)
                return await self.cleaner.cleanup(run_id)

            cleanup = asyncio.create_task(settle())
            try:
                released = await _await_task_despite_cancellation(cleanup)
                if released:
                    async with self.sessions.begin() as session:
                        await self.store.release_claim(session, run_id)
            except Exception:
                logger.exception("run cleanup failed", extra={"run_id": str(run_id)})
            finally:
                monitor.cancel()
                with suppress(asyncio.CancelledError, Exception):
                    await monitor

    async def _watch(
        self, run_id: UUID, execution: asyncio.Task[None], stop: asyncio.Event
    ) -> None:
        try:
            while True:
                async with self.sessions.begin() as session:
                    await self.store.heartbeat(
                        session, run_id, self.worker_id, lease_seconds=self.lease_seconds
                    )
                    run = await session.get(RunRecord, run_id)
                    if run is None:
                        raise RunLeaseLostError("Run disappeared")
                    cancelled = run.status == RunStatus.CANCELLED.value
                if not execution.done() and (cancelled or stop.is_set()):
                    execution.cancel()
                await asyncio.sleep(self.poll_seconds)
        except asyncio.CancelledError:
            raise
        except Exception:
            execution.cancel()
            logger.exception("run lease observation failed", extra={"run_id": str(run_id)})
