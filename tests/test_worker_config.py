from pathlib import Path

import pytest
from circular.worker.config import Settings
from pydantic import ValidationError


def test_worker_settings_have_safe_local_defaults(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.chdir(tmp_path)

    settings = Settings(_env_file=None)

    execution_root = tmp_path / ".circular"
    assert settings.repository_cache_root == execution_root / "repositories"
    assert settings.worktree_root == execution_root / "worktrees"
    assert settings.artifact_root == execution_root / "artifacts"
    assert settings.docker_worktree_root is None
    assert settings.execution_directories.docker_worktree_root == settings.worktree_root
    assert settings.runner_image == "circular-runner:dev"
    assert settings.runner_cpu_limit == 1.0
    assert settings.runner_memory_limit_mb == 2048


def test_worker_settings_accept_environment_overrides(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    worker_root = tmp_path / "worker"
    docker_root = tmp_path / "docker-host" / "worktrees"
    monkeypatch.setenv("CIRCULAR_REPOSITORY_CACHE_ROOT", str(worker_root / "repositories"))
    monkeypatch.setenv("CIRCULAR_WORKTREE_ROOT", str(worker_root / "worktrees"))
    monkeypatch.setenv("CIRCULAR_ARTIFACT_ROOT", str(worker_root / "artifacts"))
    monkeypatch.setenv("CIRCULAR_DOCKER_WORKTREE_ROOT", str(docker_root))
    monkeypatch.setenv("CIRCULAR_RUNNER_IMAGE", "registry.example.test/circular-runner:v1")
    monkeypatch.setenv("CIRCULAR_RUNNER_CPU_LIMIT", "2.5")
    monkeypatch.setenv("CIRCULAR_RUNNER_MEMORY_LIMIT_MB", "4096")

    settings = Settings(_env_file=None)

    assert settings.repository_cache_root == worker_root / "repositories"
    assert settings.worktree_root == worker_root / "worktrees"
    assert settings.artifact_root == worker_root / "artifacts"
    assert settings.execution_directories.docker_worktree_root == docker_root
    assert settings.runner_image == "registry.example.test/circular-runner:v1"
    assert settings.runner_cpu_limit == 2.5
    assert settings.runner_memory_limit_mb == 4096


def test_worker_settings_reject_invalid_docker_mapping(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CIRCULAR_DOCKER_WORKTREE_ROOT", "relative/worktrees")

    with pytest.raises(ValidationError, match="absolute host path"):
        Settings(_env_file=None)


@pytest.mark.parametrize(
    ("name", "value"),
    [
        ("CIRCULAR_RUNNER_CPU_LIMIT", "0"),
        ("CIRCULAR_RUNNER_MEMORY_LIMIT_MB", "0"),
    ],
)
def test_worker_settings_reject_non_positive_resource_limits(
    monkeypatch: pytest.MonkeyPatch, name: str, value: str
) -> None:
    monkeypatch.setenv(name, value)

    with pytest.raises(ValidationError):
        Settings(_env_file=None)
