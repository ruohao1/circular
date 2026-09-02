from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import Protocol, runtime_checkable
from uuid import UUID


@dataclass(frozen=True, slots=True)
class ContainerSpec:
    """Backend-neutral request for one Run-owned container execution.

    ``stdin`` is written exactly once and then closed. Runtime adapters validate
    their own isolation policy before accepting the remaining configuration.
    """

    run_id: UUID
    image: str
    worktree: Path
    command: tuple[str, ...]
    stdin: bytes
    cpu_limit: float
    memory_limit_mb: int
    environment: dict[str, str] = field(default_factory=dict)
    network_enabled: bool = False


@dataclass(frozen=True, slots=True)
class ContainerHandle:
    """Adapter-issued reference to one live execution allocation.

    ``id`` is the adapter-local routing identity used for live operations.
    ``resource_id`` is the immutable backend identity safe to persist for later
    ownership checks and cleanup. Callers must retain the complete handle for
    live operations rather than reconstructing one from durable state.
    """

    id: str
    resource_id: str


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
    often it is called. ``stop`` must terminate the execution before returning;
    adapters may attempt a bounded graceful stop before forcing termination. Once
    it returns, ``output`` reaches EOF and ``wait`` returns a stable result. It is
    safe to call repeatedly or after natural completion, whose result wins a race
    with cancellation. ``discard`` is the narrow compensation boundary for an
    allocation that could not be durably handed off: it permanently releases that
    exact resource before returning and is safe to call repeatedly. General
    Workspace cleanup remains a separate responsibility.
    """

    async def start(self, spec: ContainerSpec) -> ContainerHandle: ...

    def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]: ...

    async def wait(self, handle: ContainerHandle) -> RuntimeResult: ...

    async def stop(self, handle: ContainerHandle) -> None: ...

    async def discard(self, handle: ContainerHandle) -> None: ...
