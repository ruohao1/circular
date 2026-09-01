from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any
from uuid import UUID, uuid4


def utc_now() -> datetime:
    return datetime.now(UTC)


class RunStatus(StrEnum):
    QUEUED = "queued"
    PROVISIONING = "provisioning"
    RUNNING = "running"
    WAITING_FOR_APPROVAL = "waiting_for_approval"
    WAITING_FOR_INPUT = "waiting_for_input"
    FINALIZING = "finalizing"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"


class TaskStatus(StrEnum):
    OPEN = "open"
    IN_PROGRESS = "in_progress"
    COMPLETED = "completed"
    CANCELLED = "cancelled"


class WorkspaceStatus(StrEnum):
    PENDING = "pending"
    READY = "ready"
    RELEASED = "released"
    FAILED = "failed"


class ApprovalStatus(StrEnum):
    PENDING = "pending"
    APPROVED = "approved"
    REJECTED = "rejected"
    CANCELLED = "cancelled"


class DelegationStatus(StrEnum):
    REQUESTED = "requested"
    STARTED = "started"
    COMPLETED = "completed"
    REJECTED = "rejected"


@dataclass(frozen=True, slots=True)
class Project:
    name: str
    description: str | None = None
    id: UUID = field(default_factory=uuid4)
    created_at: datetime = field(default_factory=utc_now)


@dataclass(frozen=True, slots=True)
class Repository:
    project_id: UUID
    name: str
    clone_url: str
    default_branch: str = "main"
    external_refs: dict[str, Any] = field(default_factory=dict)
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Agent:
    project_id: UUID
    name: str
    backend: str
    instructions: str = ""
    backend_config: dict[str, Any] = field(default_factory=dict)
    enabled: bool = True
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Task:
    project_id: UUID
    title: str
    description: str = ""
    repository_id: UUID | None = None
    status: TaskStatus = TaskStatus.OPEN
    external_refs: dict[str, Any] = field(default_factory=dict)
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Run:
    task_id: UUID
    agent_id: UUID
    backend: str
    status: RunStatus = RunStatus.QUEUED
    attempt: int = 1
    parent_run_id: UUID | None = None
    external_refs: dict[str, Any] = field(default_factory=dict)
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Workspace:
    run_id: UUID
    worktree_path: str
    status: WorkspaceStatus = WorkspaceStatus.PENDING
    container_id: str | None = None
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Approval:
    run_id: UUID
    action: str
    reason: str
    status: ApprovalStatus = ApprovalStatus.PENDING
    requested_payload: dict[str, Any] = field(default_factory=dict)
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Artifact:
    run_id: UUID
    kind: str
    uri: str
    metadata: dict[str, Any] = field(default_factory=dict)
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Delegation:
    parent_run_id: UUID
    target_agent_id: UUID
    objective: str
    depth: int
    status: DelegationStatus = DelegationStatus.REQUESTED
    child_task_id: UUID | None = None
    child_run_id: UUID | None = None
    id: UUID = field(default_factory=uuid4)


@dataclass(frozen=True, slots=True)
class Integration:
    project_id: UUID
    provider: str
    config: dict[str, Any] = field(default_factory=dict)
    enabled: bool = True
    id: UUID = field(default_factory=uuid4)
