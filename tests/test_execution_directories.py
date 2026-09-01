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


@pytest.mark.parametrize(
    "relative_path",
    [
        Path("."),
        Path("not-a-run-id"),
        Path(f"{uuid4()}/nested"),
    ],
)
def test_docker_mapping_rejects_anything_except_one_uuid_owned_worktree(
    tmp_path: Path, relative_path: Path
) -> None:
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=tmp_path / "worker" / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host" / "worktrees",
    )

    with pytest.raises(InvalidExecutionPath, match="UUID-owned Run worktree"):
        directories.docker_host_path(directories.worktree_root / relative_path)


def test_docker_mapping_rejects_existing_symlink_escape(tmp_path: Path) -> None:
    worktree_root = tmp_path / "worker" / "worktrees"
    outside = tmp_path / "outside"
    run_id = uuid4()
    worktree_root.mkdir(parents=True)
    outside.mkdir()
    (worktree_root / str(run_id)).symlink_to(outside, target_is_directory=True)
    directories = ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=worktree_root,
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host" / "worktrees",
    )

    with pytest.raises(InvalidExecutionPath, match="outside managed root"):
        directories.run_paths(run_id)


@pytest.mark.parametrize(
    ("repository_suffix", "worktree_suffix", "artifact_suffix"),
    [
        ("managed", "managed", "artifacts"),
        ("managed", "managed/worktrees", "artifacts"),
        ("repositories", "managed", "managed/artifacts"),
    ],
)
def test_worker_owned_roots_must_not_overlap(
    tmp_path: Path,
    repository_suffix: str,
    worktree_suffix: str,
    artifact_suffix: str,
) -> None:
    with pytest.raises(InvalidExecutionPath, match="must not overlap"):
        ExecutionDirectories(
            repository_cache_root=tmp_path / repository_suffix,
            worktree_root=tmp_path / worktree_suffix,
            artifact_root=tmp_path / artifact_suffix,
            docker_worktree_root=tmp_path / "host" / "worktrees",
        )


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
