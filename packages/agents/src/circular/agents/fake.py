import asyncio
from collections.abc import AsyncIterator
from uuid import UUID

from circular.agents.backend import BackendCapabilities, BackendContext, BackendSession
from circular.events import EventEnvelope, EventType


class FakeAgentBackend:
    """Deterministic adapter for tests and end-to-end control-plane development."""

    name = "fake"

    def __init__(self, delay_seconds: float = 0.0) -> None:
        self._delay_seconds = delay_seconds
        self._contexts: dict[str, BackendContext] = {}
        self._cancelled: set[str] = set()

    @property
    def capabilities(self) -> BackendCapabilities:
        return BackendCapabilities(streaming=True, token_usage=True)

    async def start(self, context: BackendContext) -> BackendSession:
        session = BackendSession(id=f"fake:{context.run_id}", run_id=context.run_id)
        self._contexts[session.id] = context
        return session

    async def resume(self, session_id: str, context: BackendContext) -> BackendSession:
        session = BackendSession(id=session_id, run_id=context.run_id)
        self._contexts[session.id] = context
        return session

    async def send(self, session: BackendSession, message: str) -> None:
        context = self._contexts[session.id]
        self._contexts[session.id] = BackendContext(
            run_id=context.run_id,
            task_title=context.task_title,
            task_description=f"{context.task_description}\n{message}",
            instructions=context.instructions,
            workspace_path=context.workspace_path,
            config=context.config,
        )

    async def approve(self, session: BackendSession, approval_id: UUID, approved: bool) -> None:
        del session, approval_id, approved

    async def cancel(self, session: BackendSession) -> None:
        self._cancelled.add(session.id)

    async def events(self, session: BackendSession) -> AsyncIterator[EventEnvelope]:
        context = self._contexts[session.id]
        text = f"Fake backend completed: {context.task_title}"
        for token in (text[: len(text) // 2], text[len(text) // 2 :]):
            if session.id in self._cancelled:
                return
            if self._delay_seconds:
                await asyncio.sleep(self._delay_seconds)
            yield EventEnvelope(
                run_id=session.run_id,
                type=EventType.AGENT_MESSAGE_DELTA,
                data={"delta": token},
                source=self.name,
                raw={"kind": "text_delta", "text": token},
            )
        yield EventEnvelope(
            run_id=session.run_id,
            type=EventType.AGENT_MESSAGE_COMPLETED,
            data={"content": text},
            source=self.name,
        )
        yield EventEnvelope(
            run_id=session.run_id,
            type=EventType.USAGE_UPDATED,
            data={"input_tokens": 1, "output_tokens": len(text.split())},
            source=self.name,
        )
