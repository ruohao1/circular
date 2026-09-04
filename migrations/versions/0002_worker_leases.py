"""Bound worker claims and recovery attempts."""

import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("runs", sa.Column("lease_expires_at", sa.DateTime(timezone=True)))
    op.add_column(
        "runs", sa.Column("recovery_attempts", sa.Integer(), server_default="0", nullable=False)
    )
    op.create_index("ix_runs_lease_expires_at", "runs", ["lease_expires_at"])


def downgrade() -> None:
    op.drop_index("ix_runs_lease_expires_at", table_name="runs")
    op.drop_column("runs", "recovery_attempts")
    op.drop_column("runs", "lease_expires_at")
