import asyncio
import logging
import socket
from contextlib import suppress

from circular.agents import FakeAgentBackend
from circular.runners import RunExecutor
from circular.storage import RunStore, create_engine, create_session_factory
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

logger = logging.getLogger(__name__)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    database_url: str = Field(
        default="postgresql+psycopg://circular:circular@localhost:5432/circular",
        validation_alias="DATABASE_URL",
    )
    poll_interval_seconds: float = Field(
        default=1.0, validation_alias="CIRCULAR_POLL_INTERVAL_SECONDS"
    )
    fake_delay_seconds: float = Field(default=0.05, validation_alias="CIRCULAR_FAKE_DELAY_SECONDS")
    worker_id: str = Field(
        default_factory=socket.gethostname, validation_alias="CIRCULAR_WORKER_ID"
    )


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
