from pathlib import Path
from uuid import uuid4

import pytest
from circular.runners import ExecutionDirectories, InvalidExecutionPath


def test_paths_are_derived_only_from_uuid_under_managed_roots(tmp_path: Path) -> None:
    repository_id = uuid4()
    run_id = uuid4()
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=tmp_path / "worker" / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host" / "worktrees",
    )

    assert directories.repository_cache_path(repository_id) == (
        tmp_path / "repositories" / str(repository_id)
    )
    assert directories.run_paths(run_id).worktree == (
        tmp_path / "worker" / "worktrees" / str(run_id)
    )
    assert directories.run_paths(run_id).artifacts == tmp_path / "artifacts" / str(run_id)
    assert directories.run_paths(run_id).docker_host_worktree == (
        tmp_path / "host" / "worktrees" / str(run_id)
    )


def test_docker_mapping_rejects_traversal_outside_worker_root(tmp_path: Path) -> None:
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=tmp_path / "worker" / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host" / "worktrees",
    )
    traversal = directories.worktree_root / str(uuid4()) / ".." / ".." / "outside"

    with pytest.raises(InvalidExecutionPath, match="outside managed root"):
        directories.docker_host_path(traversal)


def test_docker_mapping_rejects_existing_symlink_escape(tmp_path: Path) -> None:
    worktree_root = tmp_path / "worker" / "worktrees"
    outside = tmp_path / "outside"
    worktree_root.mkdir(parents=True)
    outside.mkdir()
    (worktree_root / "escape").symlink_to(outside, target_is_directory=True)
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=worktree_root,
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host" / "worktrees",
    )

    with pytest.raises(InvalidExecutionPath, match="outside managed root"):
        directories.docker_host_path(worktree_root / "escape" / str(uuid4()))


def test_mapping_roots_must_be_absolute_and_managed(tmp_path: Path) -> None:
    with pytest.raises(InvalidExecutionPath, match="must be absolute"):
        ExecutionDirectories(
            repository_cache_root=tmp_path / "repositories",
            worktree_root=tmp_path / "worktrees",
            artifact_root=tmp_path / "artifacts",
            docker_worktree_root=Path("relative/worktrees"),
        )

    with pytest.raises(InvalidExecutionPath, match="filesystem root"):
        ExecutionDirectories(
            repository_cache_root=Path("/"),
            worktree_root=tmp_path / "worktrees",
            artifact_root=tmp_path / "artifacts",
            docker_worktree_root=tmp_path / "host" / "worktrees",
        )


def test_managed_paths_require_uuid_identifiers(tmp_path: Path) -> None:
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=tmp_path / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "worktrees",
    )

    with pytest.raises(TypeError, match="UUID"):
        directories.repository_cache_path("../../outside")  # type: ignore[arg-type]
