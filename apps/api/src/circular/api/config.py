from functools import lru_cache
from pathlib import Path

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    database_url: str = "postgresql+psycopg://circular:circular@localhost:5432/circular"
    cors_origins: tuple[str, ...] = ("http://localhost:5173",)
    sse_poll_interval_seconds: float = 0.5
    artifact_root: Path = Field(
        default_factory=lambda: Path.cwd() / ".circular/artifacts",
        validation_alias="CIRCULAR_ARTIFACT_ROOT",
    )


@lru_cache
def get_settings() -> Settings:
    return Settings()
