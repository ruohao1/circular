"""Disposable API + worker processes for Playwright's real milestone scenarios."""

import asyncio
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

from circular.storage import (
    ProjectRecord,
    RunRecord,
    TaskRecord,
    create_engine,
    create_session_factory,
)
from sqlalchemy import delete, select


async def remove_fixtures(database_url: str, prefix: str) -> None:
    engine = create_engine(database_url)
    try:
        async with create_session_factory(engine).begin() as session:
            projects = select(ProjectRecord.id).where(ProjectRecord.name.startswith(prefix))
            tasks = select(TaskRecord.id).where(TaskRecord.project_id.in_(projects))
            await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(tasks)))
            await session.execute(delete(ProjectRecord).where(ProjectRecord.id.in_(projects)))
    finally:
        await engine.dispose()


def main() -> None:
    database_url = os.environ["TEST_DATABASE_URL"]
    prefix = os.environ["CIRCULAR_E2E_PREFIX"]
    if not prefix.startswith("__circular_ui_test_") or len(prefix) < 25:
        raise ValueError("a unique test prefix is required")
    root = Path(tempfile.mkdtemp(prefix="circular-ui-e2e-"))
    environment = {
        **os.environ,
        "DATABASE_URL": database_url,
        "CIRCULAR_REPOSITORY_CACHE_ROOT": str(root / "repositories"),
        "CIRCULAR_WORKTREE_ROOT": str(root / "worktrees"),
        "CIRCULAR_DOCKER_WORKTREE_ROOT": str(root / "worktrees"),
        "CIRCULAR_ARTIFACT_ROOT": str(root / "artifacts"),
        "CIRCULAR_RUNNER_IMAGE": "circular-isq162-runner:test",
        "CIRCULAR_POLL_INTERVAL_SECONDS": "0.1",
        "CORS_ORIGINS": '["http://127.0.0.1:15173"]',
    }
    subprocess.run(
        [sys.executable, "-m", "alembic", "upgrade", "head"], env=environment, check=True
    )
    subprocess.run(
        [
            "docker",
            "build",
            "-f",
            "infra/fake-agent-workload.Dockerfile",
            "-t",
            environment["CIRCULAR_RUNNER_IMAGE"],
            ".",
        ],
        check=True,
    )
    api = subprocess.Popen(
        [
            sys.executable,
            "-m",
            "uvicorn",
            "circular.api.main:app",
            "--host",
            "127.0.0.1",
            "--port",
            "18000",
        ],
        env=environment,
    )
    worker = subprocess.Popen(
        [os.environ["CIRCULAR_E2E_GO_WORKER"]]
        if os.environ.get("CIRCULAR_E2E_GO_WORKER")
        else [sys.executable, "-c", "from circular.worker.main import run; run()"],
        env={**environment, "CIRCULAR_EXECUTOR_PYTHON": sys.executable},
    )
    stopping = False

    def stop(*_):
        nonlocal stopping
        stopping = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        while not stopping and api.poll() is None and worker.poll() is None:
            time.sleep(0.2)
    finally:
        worker.terminate()
        worker.wait(timeout=90)
        api.terminate()
        api.wait(timeout=15)
        asyncio.run(remove_fixtures(database_url, prefix))
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
