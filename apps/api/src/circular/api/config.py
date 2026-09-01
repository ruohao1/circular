from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    database_url: str = "postgresql+psycopg://circular:circular@localhost:5432/circular"
    cors_origins: tuple[str, ...] = ("http://localhost:5173",)
    sse_poll_interval_seconds: float = 0.5


@lru_cache
def get_settings() -> Settings:
    return Settings()
