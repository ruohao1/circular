from datetime import datetime
from typing import Any
from uuid import UUID, uuid4

from circular.domain import (
    ApprovalStatus,
    DelegationStatus,
    RunStatus,
    TaskStatus,
    WorkspaceStatus,
)
from sqlalchemy import (
    JSON,
    BigInteger,
    Boolean,
    DateTime,
    ForeignKey,
    Index,
    Integer,
    String,
    Text,
    UniqueConstraint,
    Uuid,
    func,
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    pass


class TimestampMixin:
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, server_default=func.now()
    )
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, server_default=func.now(), onupdate=func.now()
    )


class ProjectRecord(TimestampMixin, Base):
    __tablename__ = "projects"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    name: Mapped[str] = mapped_column(String(200), nullable=False)
    description: Mapped[str | None] = mapped_column(Text)


class RepositoryRecord(TimestampMixin, Base):
    __tablename__ = "repositories"
    __table_args__ = (UniqueConstraint("project_id", "name"),)

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    project_id: Mapped[UUID] = mapped_column(
        ForeignKey("projects.id", ondelete="CASCADE"), nullable=False, index=True
    )
    name: Mapped[str] = mapped_column(String(200), nullable=False)
    clone_url: Mapped[str] = mapped_column(Text, nullable=False)
    default_branch: Mapped[str] = mapped_column(String(200), nullable=False, default="main")
    external_refs: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)


class AgentRecord(TimestampMixin, Base):
    __tablename__ = "agents"
    __table_args__ = (UniqueConstraint("project_id", "name"),)

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    project_id: Mapped[UUID] = mapped_column(
        ForeignKey("projects.id", ondelete="CASCADE"), nullable=False, index=True
    )
    name: Mapped[str] = mapped_column(String(200), nullable=False)
    backend: Mapped[str] = mapped_column(String(100), nullable=False)
    instructions: Mapped[str] = mapped_column(Text, nullable=False, default="")
    backend_config: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)
    enabled: Mapped[bool] = mapped_column(Boolean, nullable=False, default=True)


class TaskRecord(TimestampMixin, Base):
    __tablename__ = "tasks"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    project_id: Mapped[UUID] = mapped_column(
        ForeignKey("projects.id", ondelete="CASCADE"), nullable=False, index=True
    )
    repository_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("repositories.id", ondelete="SET NULL"), index=True
    )
    title: Mapped[str] = mapped_column(String(500), nullable=False)
    description: Mapped[str] = mapped_column(Text, nullable=False, default="")
    status: Mapped[str] = mapped_column(
        String(50), nullable=False, default=TaskStatus.OPEN.value, index=True
    )
    external_refs: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)


class RunRecord(TimestampMixin, Base):
    __tablename__ = "runs"
    __table_args__ = (
        UniqueConstraint("task_id", "attempt"),
        Index("ix_runs_queue", "status", "created_at"),
    )

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    task_id: Mapped[UUID] = mapped_column(
        ForeignKey("tasks.id", ondelete="CASCADE"), nullable=False, index=True
    )
    agent_id: Mapped[UUID] = mapped_column(
        ForeignKey("agents.id", ondelete="RESTRICT"), nullable=False, index=True
    )
    parent_run_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("runs.id", ondelete="SET NULL"), index=True
    )
    backend: Mapped[str] = mapped_column(String(100), nullable=False)
    status: Mapped[str] = mapped_column(String(50), nullable=False, default=RunStatus.QUEUED.value)
    attempt: Mapped[int] = mapped_column(Integer, nullable=False, default=1)
    worker_id: Mapped[str | None] = mapped_column(String(200))
    claimed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    lease_expires_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), index=True)
    recovery_attempts: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    error: Mapped[str | None] = mapped_column(Text)
    external_refs: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)


class WorkspaceRecord(TimestampMixin, Base):
    __tablename__ = "workspaces"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    run_id: Mapped[UUID] = mapped_column(
        ForeignKey("runs.id", ondelete="CASCADE"), nullable=False, unique=True
    )
    worktree_path: Mapped[str] = mapped_column(Text, nullable=False)
    container_id: Mapped[str | None] = mapped_column(String(200))
    status: Mapped[str] = mapped_column(
        String(50), nullable=False, default=WorkspaceStatus.PENDING.value
    )


class EventRecord(Base):
    __tablename__ = "events"
    __table_args__ = (
        UniqueConstraint("run_id", "sequence"),
        Index("ix_events_run_sequence", "run_id", "sequence"),
    )

    position: Mapped[int] = mapped_column(BigInteger, primary_key=True, autoincrement=True)
    id: Mapped[UUID] = mapped_column(Uuid, nullable=False, unique=True, default=uuid4)
    run_id: Mapped[UUID] = mapped_column(ForeignKey("runs.id", ondelete="CASCADE"), nullable=False)
    sequence: Mapped[int] = mapped_column(BigInteger, nullable=False)
    type: Mapped[str] = mapped_column(String(100), nullable=False)
    source: Mapped[str] = mapped_column(String(100), nullable=False)
    data: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False)
    raw: Mapped[dict[str, Any] | None] = mapped_column(JSON)
    occurred_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    recorded_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False, server_default=func.now()
    )


class ApprovalRecord(TimestampMixin, Base):
    __tablename__ = "approvals"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    run_id: Mapped[UUID] = mapped_column(
        ForeignKey("runs.id", ondelete="CASCADE"), nullable=False, index=True
    )
    action: Mapped[str] = mapped_column(String(200), nullable=False)
    reason: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(
        String(50), nullable=False, default=ApprovalStatus.PENDING.value
    )
    requested_payload: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)
    resolved_by: Mapped[str | None] = mapped_column(String(200))
    resolved_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))


class ArtifactRecord(TimestampMixin, Base):
    __tablename__ = "artifacts"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    run_id: Mapped[UUID] = mapped_column(
        ForeignKey("runs.id", ondelete="CASCADE"), nullable=False, index=True
    )
    kind: Mapped[str] = mapped_column(String(100), nullable=False)
    uri: Mapped[str] = mapped_column(Text, nullable=False)
    artifact_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSON, nullable=False, default=dict
    )


class DelegationRecord(TimestampMixin, Base):
    __tablename__ = "delegations"

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    parent_run_id: Mapped[UUID] = mapped_column(
        ForeignKey("runs.id", ondelete="CASCADE"), nullable=False, index=True
    )
    target_agent_id: Mapped[UUID] = mapped_column(
        ForeignKey("agents.id", ondelete="RESTRICT"), nullable=False
    )
    child_task_id: Mapped[UUID | None] = mapped_column(ForeignKey("tasks.id"))
    child_run_id: Mapped[UUID | None] = mapped_column(ForeignKey("runs.id"))
    objective: Mapped[str] = mapped_column(Text, nullable=False)
    depth: Mapped[int] = mapped_column(Integer, nullable=False)
    status: Mapped[str] = mapped_column(
        String(50), nullable=False, default=DelegationStatus.REQUESTED.value
    )


class IntegrationRecord(TimestampMixin, Base):
    __tablename__ = "integrations"
    __table_args__ = (UniqueConstraint("project_id", "provider"),)

    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)
    project_id: Mapped[UUID] = mapped_column(
        ForeignKey("projects.id", ondelete="CASCADE"), nullable=False, index=True
    )
    provider: Mapped[str] = mapped_column(String(100), nullable=False)
    config: Mapped[dict[str, Any]] = mapped_column(JSON, nullable=False, default=dict)
    enabled: Mapped[bool] = mapped_column(Boolean, nullable=False, default=True)
