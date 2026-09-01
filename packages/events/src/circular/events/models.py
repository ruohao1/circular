from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any
from uuid import UUID, uuid4


class EventType(StrEnum):
    RUN_STARTED = "run.started"
    RUN_COMPLETED = "run.completed"
    RUN_FAILED = "run.failed"
    RUN_CANCELLED = "run.cancelled"
    WORKSPACE_PROVISIONING = "workspace.provisioning"
    WORKSPACE_READY = "workspace.ready"
    WORKSPACE_RELEASED = "workspace.released"
    WORKSPACE_FAILED = "workspace.failed"
    AGENT_MESSAGE_DELTA = "agent.message.delta"
    AGENT_MESSAGE_COMPLETED = "agent.message.completed"
    TOOL_EXECUTION_STARTED = "tool.execution.started"
    TOOL_EXECUTION_OUTPUT = "tool.execution.output"
    TOOL_EXECUTION_COMPLETED = "tool.execution.completed"
    FILE_CHANGED = "file.changed"
    GIT_DIFF_UPDATED = "git.diff.updated"
    GIT_COMMIT_CREATED = "git.commit.created"
    APPROVAL_REQUESTED = "approval.requested"
    APPROVAL_RESOLVED = "approval.resolved"
    DELEGATION_REQUESTED = "delegation.requested"
    DELEGATION_STARTED = "delegation.started"
    DELEGATION_COMPLETED = "delegation.completed"
    USAGE_UPDATED = "usage.updated"
    ARTIFACT_CREATED = "artifact.created"


@dataclass(frozen=True, slots=True)
class EventEnvelope:
    run_id: UUID
    type: EventType
    data: dict[str, Any]
    source: str
    raw: dict[str, Any] | None = None
    occurred_at: datetime = field(default_factory=lambda: datetime.now(UTC))
    id: UUID = field(default_factory=uuid4)
