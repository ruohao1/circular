from collections.abc import AsyncIterator
from dataclasses import dataclass
from typing import Any, Protocol, runtime_checkable
from uuid import UUID

from circular.events import EventEnvelope


@dataclass(frozen=True, slots=True)
class BackendCapabilities:
    streaming: bool = False
    interactive_input: bool = False
    resume: bool = False
    approvals: bool = False
    structured_tool_events: bool = False
    diff_events: bool = False
    token_usage: bool = False
    cost_usage: bool = False
    native_subagents: bool = False


@dataclass(frozen=True, slots=True)
class BackendContext:
    run_id: UUID
    task_title: str
    task_description: str
    instructions: str
    workspace_path: str | None = None
    config: dict[str, Any] | None = None


@dataclass(frozen=True, slots=True)
class BackendSession:
    id: str
    run_id: UUID


@runtime_checkable
class AgentBackend(Protocol):
    name: str

    @property
    def capabilities(self) -> BackendCapabilities: ...

    async def start(self, context: BackendContext) -> BackendSession: ...

    async def resume(self, session_id: str, context: BackendContext) -> BackendSession: ...

    async def send(self, session: BackendSession, message: str) -> None: ...

    async def approve(self, session: BackendSession, approval_id: UUID, approved: bool) -> None: ...

    async def cancel(self, session: BackendSession) -> None: ...

    def events(self, session: BackendSession) -> AsyncIterator[EventEnvelope]: ...
