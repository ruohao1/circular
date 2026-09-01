from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol


@dataclass(frozen=True, slots=True)
class ContainerSpec:
    image: str
    worktree: Path
    command: tuple[str, ...]
    environment: dict[str, str] = field(default_factory=dict)
    network_enabled: bool = False
    cpu_limit: float | None = None
    memory_limit_mb: int | None = None


@dataclass(frozen=True, slots=True)
class ContainerHandle:
    id: str


class Runtime(Protocol):
    async def start(self, spec: ContainerSpec) -> ContainerHandle: ...

    async def stop(self, handle: ContainerHandle) -> None: ...
