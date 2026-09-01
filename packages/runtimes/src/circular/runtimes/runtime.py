from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Protocol, runtime_checkable


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
    """Opaque reference to an execution owned by a runtime adapter."""

    id: str


class OutputStream(StrEnum):
    STDOUT = "stdout"
    STDERR = "stderr"


@dataclass(frozen=True, slots=True)
class RuntimeOutput:
    """One output chunk in the order observed by the runtime adapter."""

    stream: OutputStream
    data: bytes


class CompletionReason(StrEnum):
    EXITED = "exited"
    STOPPED = "stopped"


@dataclass(frozen=True, slots=True)
class RuntimeResult:
    """Stable, adapter-independent result returned after execution completes."""

    reason: CompletionReason
    exit_code: int | None

    def __post_init__(self) -> None:
        if self.reason is CompletionReason.EXITED and self.exit_code is None:
            raise ValueError("an exited runtime result requires an exit code")
        if self.reason is CompletionReason.STOPPED and self.exit_code is not None:
            raise ValueError("a stopped runtime result cannot have an exit code")

    @classmethod
    def exited(cls, exit_code: int) -> "RuntimeResult":
        return cls(reason=CompletionReason.EXITED, exit_code=exit_code)

    @classmethod
    def stopped(cls) -> "RuntimeResult":
        return cls(reason=CompletionReason.STOPPED, exit_code=None)


@runtime_checkable
class Runtime(Protocol):
    """Execution seam implemented by local and container runtime adapters.

    ``output`` yields stdout and stderr chunks in the order the adapter observes
    them. ``wait`` returns the same result after completion, regardless of how
    often it is called. ``stop`` requests cancellation and must be safe to call
    repeatedly or after the execution has already completed.
    """

    async def start(self, spec: ContainerSpec) -> ContainerHandle: ...

    def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]: ...

    async def wait(self, handle: ContainerHandle) -> RuntimeResult: ...

    async def stop(self, handle: ContainerHandle) -> None: ...
