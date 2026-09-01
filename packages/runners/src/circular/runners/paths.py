from dataclasses import dataclass
from pathlib import Path
from uuid import UUID


class InvalidExecutionPath(ValueError):
    """A configured or translated execution path escaped its managed root."""


@dataclass(frozen=True, slots=True)
class RunPaths:
    """Worker-visible and Docker-host-visible paths belonging to one Run."""

    worktree: Path
    docker_host_worktree: Path
    artifacts: Path


@dataclass(frozen=True, slots=True)
class ExecutionDirectories:
    """Derive stable managed paths without accepting user-controlled path fragments.

    ``worktree_root`` is visible to the worker. ``docker_worktree_root`` is the
    equivalent root as seen by the Docker daemon. They are normally identical for
    a local worker and differ when the worker itself runs in a container.
    """

    repository_cache_root: Path
    worktree_root: Path
    artifact_root: Path
    docker_worktree_root: Path

    def __post_init__(self) -> None:
        for field_name in (
            "repository_cache_root",
            "worktree_root",
            "artifact_root",
            "docker_worktree_root",
        ):
            root = _canonical_root(getattr(self, field_name), field_name)
            object.__setattr__(self, field_name, root)

    def repository_cache_path(self, repository_id: UUID) -> Path:
        return _uuid_child(self.repository_cache_root, repository_id)

    def run_paths(self, run_id: UUID) -> RunPaths:
        worktree = _uuid_child(self.worktree_root, run_id)
        return RunPaths(
            worktree=worktree,
            docker_host_worktree=self.docker_host_path(worktree),
            artifacts=_uuid_child(self.artifact_root, run_id),
        )

    def docker_host_path(self, worker_worktree_path: Path) -> Path:
        """Translate a worker-visible worktree path for the Docker daemon.

        Only descendants of the configured worker worktree root can cross this
        mapping. Canonicalization also rejects ``..`` traversal and existing
        symlinks that leave the root.
        """

        worker_path = _canonical_absolute(worker_worktree_path, "worker worktree path")
        relative_path = _relative_to_root(worker_path, self.worktree_root)
        docker_path = (self.docker_worktree_root / relative_path).resolve(strict=False)
        _relative_to_root(docker_path, self.docker_worktree_root)
        return docker_path


def _uuid_child(root: Path, identifier: UUID) -> Path:
    if not isinstance(identifier, UUID):
        raise TypeError("managed execution paths require a UUID identifier")

    child = (root / str(identifier)).resolve(strict=False)
    _relative_to_root(child, root)
    return child


def _canonical_root(path: Path, name: str) -> Path:
    root = _canonical_absolute(path, name)
    if root == Path(root.anchor):
        raise InvalidExecutionPath(f"{name} cannot be the filesystem root")
    return root


def _canonical_absolute(path: Path, name: str) -> Path:
    candidate = Path(path).expanduser()
    if not candidate.is_absolute():
        raise InvalidExecutionPath(f"{name} must be absolute: {candidate}")
    return candidate.resolve(strict=False)


def _relative_to_root(path: Path, root: Path) -> Path:
    try:
        return path.relative_to(root)
    except ValueError as error:
        raise InvalidExecutionPath(f"path {path} is outside managed root {root}") from error
