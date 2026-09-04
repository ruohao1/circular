import asyncio
import logging
import signal
from contextlib import suppress

from circular.runners import FakeWorkloadSpecFactory
from circular.storage import create_engine
from circular.worker.config import Settings
from circular.worker.execution import build_supervisor

logger = logging.getLogger(__name__)


async def worker_loop(settings: Settings, stop: asyncio.Event | None = None) -> None:
    engine = create_engine(settings.database_url)
    supervisor = build_supervisor(
        engine,
        settings.execution_directories,
        settings.worker_id,
        spec_factory=FakeWorkloadSpecFactory(
            image=settings.runner_image,
            cpu_limit=settings.runner_cpu_limit,
            memory_limit_mb=settings.runner_memory_limit_mb,
            delay_ms=round(settings.fake_delay_seconds * 1000),
        ),
    )
    sessions, store = supervisor.sessions, supervisor.store
    stop = stop or asyncio.Event()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(signum, stop.set)
    try:
        while not stop.is_set():
            async with sessions.begin() as session:
                claimed = await store.recover_expired(session, settings.worker_id)
                recovery = claimed is not None
                if claimed is None:
                    claimed = await store.claim_next(session, settings.worker_id)
                run_id = claimed.id if claimed is not None else None
            if run_id is None:
                with suppress(TimeoutError):
                    await asyncio.wait_for(stop.wait(), timeout=settings.poll_interval_seconds)
                continue
            await supervisor.run(run_id, stop, recovery=recovery)
    finally:
        await engine.dispose()


def run() -> None:
    logging.basicConfig(level=logging.INFO)
    asyncio.run(worker_loop(Settings()))
