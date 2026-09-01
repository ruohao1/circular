"""Initial control-plane schema."""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

revision: str = "0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def timestamps() -> list[sa.Column]:
    return [
        sa.Column(
            "created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False
        ),
    ]


def upgrade() -> None:
    op.create_table(
        "projects",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column("name", sa.String(200), nullable=False),
        sa.Column("description", sa.Text()),
        *timestamps(),
    )
    op.create_table(
        "repositories",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "project_id",
            sa.Uuid(),
            sa.ForeignKey("projects.id", ondelete="CASCADE"),
            nullable=False,
        ),
        sa.Column("name", sa.String(200), nullable=False),
        sa.Column("clone_url", sa.Text(), nullable=False),
        sa.Column("default_branch", sa.String(200), nullable=False),
        sa.Column("external_refs", sa.JSON(), nullable=False),
        *timestamps(),
        sa.UniqueConstraint("project_id", "name"),
    )
    op.create_index("ix_repositories_project_id", "repositories", ["project_id"])
    op.create_table(
        "agents",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "project_id",
            sa.Uuid(),
            sa.ForeignKey("projects.id", ondelete="CASCADE"),
            nullable=False,
        ),
        sa.Column("name", sa.String(200), nullable=False),
        sa.Column("backend", sa.String(100), nullable=False),
        sa.Column("instructions", sa.Text(), nullable=False),
        sa.Column("backend_config", sa.JSON(), nullable=False),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        *timestamps(),
        sa.UniqueConstraint("project_id", "name"),
    )
    op.create_index("ix_agents_project_id", "agents", ["project_id"])
    op.create_table(
        "tasks",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "project_id",
            sa.Uuid(),
            sa.ForeignKey("projects.id", ondelete="CASCADE"),
            nullable=False,
        ),
        sa.Column(
            "repository_id", sa.Uuid(), sa.ForeignKey("repositories.id", ondelete="SET NULL")
        ),
        sa.Column("title", sa.String(500), nullable=False),
        sa.Column("description", sa.Text(), nullable=False),
        sa.Column("status", sa.String(50), nullable=False),
        sa.Column("external_refs", sa.JSON(), nullable=False),
        *timestamps(),
    )
    op.create_index("ix_tasks_project_id", "tasks", ["project_id"])
    op.create_index("ix_tasks_repository_id", "tasks", ["repository_id"])
    op.create_index("ix_tasks_status", "tasks", ["status"])
    op.create_table(
        "runs",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "task_id", sa.Uuid(), sa.ForeignKey("tasks.id", ondelete="CASCADE"), nullable=False
        ),
        sa.Column(
            "agent_id", sa.Uuid(), sa.ForeignKey("agents.id", ondelete="RESTRICT"), nullable=False
        ),
        sa.Column("parent_run_id", sa.Uuid(), sa.ForeignKey("runs.id", ondelete="SET NULL")),
        sa.Column("backend", sa.String(100), nullable=False),
        sa.Column("status", sa.String(50), nullable=False),
        sa.Column("attempt", sa.Integer(), nullable=False),
        sa.Column("worker_id", sa.String(200)),
        sa.Column("claimed_at", sa.DateTime(timezone=True)),
        sa.Column("started_at", sa.DateTime(timezone=True)),
        sa.Column("finished_at", sa.DateTime(timezone=True)),
        sa.Column("error", sa.Text()),
        sa.Column("external_refs", sa.JSON(), nullable=False),
        *timestamps(),
        sa.UniqueConstraint("task_id", "attempt"),
    )
    op.create_index("ix_runs_task_id", "runs", ["task_id"])
    op.create_index("ix_runs_agent_id", "runs", ["agent_id"])
    op.create_index("ix_runs_parent_run_id", "runs", ["parent_run_id"])
    op.create_index("ix_runs_queue", "runs", ["status", "created_at"])
    op.create_table(
        "workspaces",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "run_id",
            sa.Uuid(),
            sa.ForeignKey("runs.id", ondelete="CASCADE"),
            nullable=False,
            unique=True,
        ),
        sa.Column("worktree_path", sa.Text(), nullable=False),
        sa.Column("container_id", sa.String(200)),
        sa.Column("status", sa.String(50), nullable=False),
        *timestamps(),
    )
    op.create_table(
        "events",
        sa.Column("position", sa.BigInteger(), primary_key=True, autoincrement=True),
        sa.Column("id", sa.Uuid(), nullable=False, unique=True),
        sa.Column(
            "run_id", sa.Uuid(), sa.ForeignKey("runs.id", ondelete="CASCADE"), nullable=False
        ),
        sa.Column("sequence", sa.BigInteger(), nullable=False),
        sa.Column("type", sa.String(100), nullable=False),
        sa.Column("source", sa.String(100), nullable=False),
        sa.Column("data", sa.JSON(), nullable=False),
        sa.Column("raw", sa.JSON()),
        sa.Column("occurred_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column(
            "recorded_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False
        ),
        sa.UniqueConstraint("run_id", "sequence"),
    )
    op.create_index("ix_events_run_sequence", "events", ["run_id", "sequence"])
    op.create_table(
        "approvals",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "run_id", sa.Uuid(), sa.ForeignKey("runs.id", ondelete="CASCADE"), nullable=False
        ),
        sa.Column("action", sa.String(200), nullable=False),
        sa.Column("reason", sa.Text(), nullable=False),
        sa.Column("status", sa.String(50), nullable=False),
        sa.Column("requested_payload", sa.JSON(), nullable=False),
        sa.Column("resolved_by", sa.String(200)),
        sa.Column("resolved_at", sa.DateTime(timezone=True)),
        *timestamps(),
    )
    op.create_index("ix_approvals_run_id", "approvals", ["run_id"])
    op.create_table(
        "artifacts",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "run_id", sa.Uuid(), sa.ForeignKey("runs.id", ondelete="CASCADE"), nullable=False
        ),
        sa.Column("kind", sa.String(100), nullable=False),
        sa.Column("uri", sa.Text(), nullable=False),
        sa.Column("metadata", sa.JSON(), nullable=False),
        *timestamps(),
    )
    op.create_index("ix_artifacts_run_id", "artifacts", ["run_id"])
    op.create_table(
        "delegations",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "parent_run_id", sa.Uuid(), sa.ForeignKey("runs.id", ondelete="CASCADE"), nullable=False
        ),
        sa.Column(
            "target_agent_id",
            sa.Uuid(),
            sa.ForeignKey("agents.id", ondelete="RESTRICT"),
            nullable=False,
        ),
        sa.Column("child_task_id", sa.Uuid(), sa.ForeignKey("tasks.id")),
        sa.Column("child_run_id", sa.Uuid(), sa.ForeignKey("runs.id")),
        sa.Column("objective", sa.Text(), nullable=False),
        sa.Column("depth", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(50), nullable=False),
        *timestamps(),
    )
    op.create_index("ix_delegations_parent_run_id", "delegations", ["parent_run_id"])
    op.create_table(
        "integrations",
        sa.Column("id", sa.Uuid(), primary_key=True),
        sa.Column(
            "project_id",
            sa.Uuid(),
            sa.ForeignKey("projects.id", ondelete="CASCADE"),
            nullable=False,
        ),
        sa.Column("provider", sa.String(100), nullable=False),
        sa.Column("config", sa.JSON(), nullable=False),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        *timestamps(),
        sa.UniqueConstraint("project_id", "provider"),
    )
    op.create_index("ix_integrations_project_id", "integrations", ["project_id"])


def downgrade() -> None:
    for table in (
        "integrations",
        "delegations",
        "artifacts",
        "approvals",
        "events",
        "workspaces",
        "runs",
        "tasks",
        "agents",
        "repositories",
        "projects",
    ):
        op.drop_table(table)
