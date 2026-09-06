import os
import subprocess
import sys

import pytest


@pytest.mark.parametrize(
    ("database_url", "success"),
    [
        ("postgresql+psycopg://circular:circular@127.0.0.1:1/unreachable", True),
        ("not-a-database-url", False),
    ],
)
def test_execution_bridge_preflight_never_connects_or_allocates(tmp_path, database_url, success):
    result = subprocess.run(
        [sys.executable, "-m", "circular.worker.execute_run", "--check"],
        cwd=tmp_path,
        env={
            **os.environ,
            "DATABASE_URL": database_url,
            "CIRCULAR_REPOSITORY_CACHE_ROOT": str(tmp_path / "repositories"),
            "CIRCULAR_WORKTREE_ROOT": str(tmp_path / "worktrees"),
            "CIRCULAR_DOCKER_WORKTREE_ROOT": str(tmp_path / "worktrees"),
            "CIRCULAR_ARTIFACT_ROOT": str(tmp_path / "artifacts"),
        },
        capture_output=True,
        timeout=10,
    )
    assert (result.returncode == 0) is success, result.stderr
    assert list(tmp_path.iterdir()) == []
