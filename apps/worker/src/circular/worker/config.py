import socket
from pathlib import Path
from uuid import uuid4

from circular.runners import ExecutionDirectories, InvalidExecutionPath
from pydantic import Field, field_validator, model_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


def _local_execution_root() -> Path:
    return (Path.cwd() / ".circular").resolve()


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore", validate_default=True)

    database_url: str = Field(
        default="postgresql+psycopg://circular:circular@localhost:5432/circular",
        validation_alias="DATABASE_URL",
    )
    poll_interval_seconds: float = Field(
        default=1.0,
        gt=0,
        validation_alias="CIRCULAR_POLL_INTERVAL_SECONDS",
    )
    fake_delay_seconds: float = Field(
        default=0.05,
        ge=0,
        le=10,
        validation_alias="CIRCULAR_FAKE_DELAY_SECONDS",
    )
    worker_id: str = Field(
        default_factory=lambda: f"{socket.gethostname()}:{uuid4()}",
        min_length=1,
        validation_alias="CIRCULAR_WORKER_ID",
    )

    repository_cache_root: Path = Field(
        default_factory=lambda: _local_execution_root() / "repositories",
        validation_alias="CIRCULAR_REPOSITORY_CACHE_ROOT",
    )
    worktree_root: Path = Field(
        default_factory=lambda: _local_execution_root() / "worktrees",
        validation_alias="CIRCULAR_WORKTREE_ROOT",
    )
    artifact_root: Path = Field(
        default_factory=lambda: _local_execution_root() / "artifacts",
        validation_alias="CIRCULAR_ARTIFACT_ROOT",
    )
    docker_worktree_root: Path | None = Field(
        default=None,
        validation_alias="CIRCULAR_DOCKER_WORKTREE_ROOT",
    )
    runner_image: str = Field(
        default="circular-runner:dev",
        min_length=1,
        validation_alias="CIRCULAR_RUNNER_IMAGE",
    )
    runner_cpu_limit: float = Field(
        default=1.0,
        gt=0,
        validation_alias="CIRCULAR_RUNNER_CPU_LIMIT",
    )
    runner_memory_limit_mb: int = Field(
        default=2048,
        gt=0,
        validation_alias="CIRCULAR_RUNNER_MEMORY_LIMIT_MB",
    )

    @field_validator("repository_cache_root", "worktree_root", "artifact_root", mode="after")
    @classmethod
    def resolve_worker_root(cls, value: Path) -> Path:
        return value.expanduser().resolve(strict=False)

    @field_validator("docker_worktree_root", mode="after")
    @classmethod
    def validate_docker_root(cls, value: Path | None) -> Path | None:
        if value is None:
            return None
        expanded = value.expanduser()
        if not expanded.is_absolute():
            raise ValueError("Docker-visible worktree root must be an absolute host path")
        return expanded.resolve(strict=False)

    @model_validator(mode="after")
    def validate_execution_directories(self) -> "Settings":
        docker_root = self.docker_worktree_root or self.worktree_root
        try:
            ExecutionDirectories(
                repository_cache_root=self.repository_cache_root,
                worktree_root=self.worktree_root,
                artifact_root=self.artifact_root,
                docker_worktree_root=docker_root,
            )
        except InvalidExecutionPath as error:
            raise ValueError(str(error)) from error
        return self

    @property
    def execution_directories(self) -> ExecutionDirectories:
        return ExecutionDirectories(
            repository_cache_root=self.repository_cache_root,
            worktree_root=self.worktree_root,
            artifact_root=self.artifact_root,
            docker_worktree_root=self.docker_worktree_root or self.worktree_root,
        )
