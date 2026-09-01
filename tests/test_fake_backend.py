from uuid import uuid4

from circular.agents import AgentBackend, BackendContext, FakeAgentBackend
from circular.events import EventType


async def test_fake_backend_satisfies_contract_and_streams_normalized_events() -> None:
    backend = FakeAgentBackend()
    assert isinstance(backend, AgentBackend)
    assert backend.capabilities.streaming
    assert backend.capabilities.token_usage

    context = BackendContext(
        run_id=uuid4(),
        task_title="Add an endpoint",
        task_description="Create the endpoint and tests",
        instructions="Act as a backend engineer",
    )
    session = await backend.start(context)
    events = [event async for event in backend.events(session)]

    assert [event.type for event in events] == [
        EventType.AGENT_MESSAGE_DELTA,
        EventType.AGENT_MESSAGE_DELTA,
        EventType.AGENT_MESSAGE_COMPLETED,
        EventType.USAGE_UPDATED,
    ]
    assert all(event.run_id == context.run_id for event in events)
    assert events[0].raw is not None
