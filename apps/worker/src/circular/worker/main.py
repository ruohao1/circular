import asyncio
import logging
from contextlib import suppress

from circular.agents import FakeAgentBackend
from circular.runners import RunExecutor
from circular.storage import RunStore, create_engine, create_session_factory
from circular.worker.config import Settings

logger = logging.getLogger(__name__)


async def worker_loop(settings: Settings, stop: asyncio.Event | None = None) -> None:
    engine = create_engine(settings.database_url)
    sessions = create_session_factory(engine)
    store = RunStore()
    executor = RunExecutor(
        sessions,
        store,
        {"fake": FakeAgentBackend(settings.fake_delay_seconds)},
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
                await executor.execute(run_id)
            except Exception:
                logger.exception("run execution failed", extra={"run_id": str(run_id)})
    finally:
        await engine.dispose()


def run() -> None:
    logging.basicConfig(level=logging.INFO)
    asyncio.run(worker_loop(Settings()))
