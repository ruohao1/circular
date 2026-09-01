from datetime import datetime
from typing import Any
from uuid import UUID

from circular.domain import RunStatus, TaskStatus
from pydantic import BaseModel, ConfigDict, Field


class Schema(BaseModel):
    model_config = ConfigDict(from_attributes=True)


class ProjectCreate(Schema):
    name: str = Field(min_length=1, max_length=200)
    description: str | None = None


class ProjectRead(ProjectCreate):
    id: UUID
    created_at: datetime
    updated_at: datetime


class RepositoryCreate(Schema):
    project_id: UUID
    name: str = Field(min_length=1, max_length=200)
    clone_url: str
    default_branch: str = "main"
    external_refs: dict[str, Any] = Field(default_factory=dict)


class RepositoryRead(RepositoryCreate):
    id: UUID
    created_at: datetime
    updated_at: datetime


class AgentCreate(Schema):
    project_id: UUID
    name: str = Field(min_length=1, max_length=200)
    backend: str = "fake"
    instructions: str = ""
    backend_config: dict[str, Any] = Field(default_factory=dict)


class AgentRead(AgentCreate):
    id: UUID
    enabled: bool
    created_at: datetime
    updated_at: datetime


class TaskCreate(Schema):
    project_id: UUID
    repository_id: UUID | None = None
    title: str = Field(min_length=1, max_length=500)
    description: str = ""
    external_refs: dict[str, Any] = Field(default_factory=dict)


class TaskRead(TaskCreate):
    id: UUID
    status: TaskStatus
    created_at: datetime
    updated_at: datetime


class RunCreate(Schema):
    task_id: UUID
    agent_id: UUID
    external_refs: dict[str, Any] = Field(default_factory=dict)


class RunRead(Schema):
    id: UUID
    task_id: UUID
    agent_id: UUID
    parent_run_id: UUID | None
    backend: str
    status: RunStatus
    attempt: int
    worker_id: str | None
    claimed_at: datetime | None
    started_at: datetime | None
    finished_at: datetime | None
    error: str | None
    external_refs: dict[str, Any]
    created_at: datetime
    updated_at: datetime


class EventRead(Schema):
    position: int
    id: UUID
    run_id: UUID
    sequence: int
    type: str
    source: str
    data: dict[str, Any]
    raw: dict[str, Any] | None
    occurred_at: datetime
    recorded_at: datetime
