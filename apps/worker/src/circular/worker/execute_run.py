"""Temporary one-Run execution bridge for the incremental Go worker migration.

This module never claims queued Runs. The Go worker commits a claim first, then
hands its identity here. Every resource operation still uses the production
supervisor's Run-row ownership and lease fencing.
"""

import argparse
import asyncio
import logging
import signal
from uuid import UUID

from circular.domain import RunStatus
from circular.runners import FakeWorkloadSpecFactory
from circular.storage import RunStore, create_engine, create_session_factory
from circular.worker.config import Settings
from circular.worker.execution import build_supervisor


async def execute_claim(
    settings: Settings,
    run_id: UUID,
    stop: asyncio.Event,
    *,
    recovery: bool = False,
) -> None:
    engine = create_engine(settings.database_url)
    try:
        sessions = create_session_factory(engine)
        sessions.configure(info={"worker_id": settings.worker_id})
        async with sessions.begin() as session:
            run = await RunStore().lock_for_execution(session, run_id)
            status = RunStatus(run.status)
            if status is RunStatus.CANCELLED:
                # Cancellation between claim and process startup must allocate
                # nothing, but any previously owned resources still need cleanup.
                recovery = True
            elif recovery:
                if status not in {RunStatus.FAILED, RunStatus.SUCCEEDED}:
                    raise ValueError("recovery requires a terminal Run")
            elif status is not RunStatus.PROVISIONING:
                raise ValueError("execution requires a claimed provisioning Run")

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
        if stop.is_set():
            # Do not start provisioning if shutdown was requested during setup.
            # The supervisor settles the terminal outcome before cleanup.
            recovery = True
        await supervisor.run(run_id, stop, recovery=recovery)
    finally:
        await engine.dispose()


async def _execute_with_signals(settings: Settings, run_id: UUID, recovery: bool) -> None:
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(signum, stop.set)
    try:
        await execute_claim(settings, run_id, stop, recovery=recovery)
    finally:
        for signum in (signal.SIGTERM, signal.SIGINT):
            loop.remove_signal_handler(signum)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--run-id", type=UUID)
    parser.add_argument("--worker-id")
    parser.add_argument("--recovery", action="store_true")
    args = parser.parse_args()
    if args.check:
        # Creating an engine validates the URL/driver without opening a database
        # connection. Catch a missing/incompatible driver before Go claims work.
        engine = create_engine(Settings().database_url)
        asyncio.run(engine.dispose())
        return
    if args.run_id is None or not args.worker_id:
        parser.error("--run-id and --worker-id are required unless --check is used")
    # Use the validation alias so a pre-existing environment value cannot change
    # the durable owner identity explicitly passed by the Go worker.
    settings = Settings(CIRCULAR_WORKER_ID=args.worker_id)
    logging.basicConfig(level=logging.INFO)
    asyncio.run(_execute_with_signals(settings, args.run_id, args.recovery))


if __name__ == "__main__":
    main()
