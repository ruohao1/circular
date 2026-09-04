import subprocess
from pathlib import Path
from uuid import uuid4

import pytest
from circular.git import GitDiffCaptureError, LocalGitDiffCollector


def git(repository: Path, *arguments: str) -> bytes:
    return subprocess.run(
        ["git", "-C", str(repository), *arguments],
        check=True,
        capture_output=True,
    ).stdout


def committed_repository(tmp_path: Path) -> Path:
    repository = tmp_path / "repository"
    repository.mkdir()
    git(repository, "init", "--initial-branch=main")
    git(repository, "config", "user.name", "Circular Test")
    git(repository, "config", "user.email", "circular@example.test")
    repository.joinpath("tracked.txt").write_text("before\n")
    git(repository, "add", "tracked.txt")
    git(repository, "commit", "-m", "initial")
    return repository


async def test_capture_includes_tracked_and_untracked_changes_without_mutating_index(
    tmp_path: Path,
) -> None:
    repository = committed_repository(tmp_path)
    repository.joinpath("tracked.txt").write_text("after\n")
    repository.joinpath("new file.txt").write_text("new\n")
    original_index_diff = git(repository, "diff", "--cached", "--binary", "HEAD")

    captured = await LocalGitDiffCollector().capture(repository)

    assert captured.changed_files == 2
    assert captured.contains_binary is False
    assert b"-before" in captured.content
    assert b"+after" in captured.content
    assert b"new file.txt" in captured.content
    assert b"+new" in captured.content
    assert git(repository, "diff", "--cached", "--binary", "HEAD") == original_index_diff


async def test_capture_represents_an_empty_diff_explicitly(tmp_path: Path) -> None:
    repository = committed_repository(tmp_path)

    captured = await LocalGitDiffCollector().capture(repository)

    assert captured.empty is True
    assert captured.changed_files == 0
    assert captured.contains_binary is False
    assert captured.content == b""


async def test_capture_keeps_binary_additions_reconstructable(tmp_path: Path) -> None:
    repository = committed_repository(tmp_path)
    repository.joinpath("asset.bin").write_bytes(bytes(range(256)) + b"\0\xff" * 128)

    captured = await LocalGitDiffCollector().capture(repository)

    assert captured.changed_files == 1
    assert captured.contains_binary is True
    assert b"GIT binary patch" in captured.content
    assert b"asset.bin" in captured.content


async def test_managed_diff_uses_only_the_owned_cache_metadata(tmp_path):
    source = committed_repository(tmp_path)
    root = tmp_path / "cache"
    root.mkdir()
    repository = root / str(uuid4())
    source.rename(repository)
    run_id = uuid4()
    worktree = tmp_path / str(run_id)
    git(repository, "worktree", "add", "-b", f"circular/run/{run_id}", str(worktree))
    worktree.joinpath("result.txt").write_text("output\n")
    collector = LocalGitDiffCollector(root)
    assert (await collector.capture(worktree)).changed_files == 1

    # The agent controls this file, but cannot redirect Git into another cache.
    (worktree / ".git").write_text(f"gitdir: {repository / '.git'}\n")
    with pytest.raises(GitDiffCaptureError, match="ownership validation"):
        await collector.capture(worktree)


async def test_managed_diff_rejects_an_agent_supplied_git_directory(tmp_path):
    repository = committed_repository(tmp_path)
    with pytest.raises(GitDiffCaptureError, match="ownership validation"):
        await LocalGitDiffCollector(tmp_path / "cache").capture(repository)
