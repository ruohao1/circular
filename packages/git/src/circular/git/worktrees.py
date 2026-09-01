from dataclasses import dataclass
from pathlib import Path
from typing import Protocol
from uuid import UUID


@dataclass(frozen=True, slots=True)
class ProvisionedWorktree:
    run_id: UUID
    path: Path
    branch: str


class WorktreeManager(Protocol):
    async def provision(
        self, run_id: UUID, repository_path: Path, base_ref: str
    ) -> ProvisionedWorktree: ...

    async def release(self, worktree: ProvisionedWorktree) -> None: ...
