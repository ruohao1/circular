from __future__ import annotations

import asyncio
import fcntl
import os
import shutil
import signal
import stat
import subprocess
from pathlib import Path
from uuid import UUID, uuid4

import circular.git.worktrees as worktree_module
import pytest
from circular.git import (
    InvalidWorktreeRepository,
    LocalRepositoryCache,
    LocalWorktreeManager,
    ProvisionedWorktree,
    WorktreeConflictError,
    WorktreeLockError,
    WorktreeProvisionError,
    WorktreeRefError,
    WorktreeReleaseError,
)
from circular.runners import ExecutionDirectories


@pytest.mark.asyncio
async def test_provision_creates_the_exact_run_owned_worktree_from_requested_ref(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    _git(source, "checkout", "-b", "feature")
    (source / "README.md").write_text("feature\n")
    _git(source, "commit", "--all", "--message=feature")
    feature_commit = _git(source, "rev-parse", "HEAD")
    _git(source, "checkout", "main")
    main_commit = _git(source, "rev-parse", "HEAD")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()

    provisioned = await LocalWorktreeManager(directories).provision(
        run_id, repository, "origin/feature"
    )

    assert provisioned.run_id == run_id
    assert provisioned.path == directories.run_paths(run_id).worktree
    assert provisioned.branch == f"circular/run/{run_id}"
    assert _ownership_marker(directories, run_id).is_file()
    assert _git(provisioned.path, "rev-parse", "HEAD") == feature_commit
    assert _git(provisioned.path, "branch", "--show-current") == provisioned.branch
    assert (provisioned.path / "README.md").read_text() == "feature\n"
    assert _git(repository, "rev-parse", "HEAD") == main_commit
    assert not (repository / "README.md").exists()


@pytest.mark.asyncio
async def test_two_runs_have_independent_worktrees_and_leave_base_checkout_unchanged(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    base_head = _git(repository, "rev-parse", "HEAD")
    first_run = uuid4()
    second_run = uuid4()
    manager = LocalWorktreeManager(directories)

    first, second = await asyncio.gather(
        manager.provision(first_run, repository, "main"),
        manager.provision(second_run, repository, "main"),
    )
    (first.path / "README.md").write_text("first run\n")

    assert first.path != second.path
    assert first.branch != second.branch
    assert (second.path / "README.md").read_text() == "first\n"
    assert _git(repository, "rev-parse", "HEAD") == base_head
    assert not (repository / "README.md").exists()
    assert _git(repository, "worktree", "list", "--porcelain").count("worktree ") == 3


@pytest.mark.asyncio
async def test_failed_provision_removes_installed_receipt_after_durable_rollback(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    real_fsync = os.fsync
    root_fsync_failed = False

    def fail_first_root_fsync(descriptor: int) -> None:
        nonlocal root_fsync_failed
        if not root_fsync_failed and stat.S_ISDIR(os.fstat(descriptor).st_mode):
            root_fsync_failed = True
            raise OSError("injected ownership marker durability failure")
        real_fsync(descriptor)

    monkeypatch.setattr(worktree_module.os, "fsync", fail_first_root_fsync)

    with pytest.raises(WorktreeProvisionError):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    ownership_marker = _ownership_marker(directories, run_id)
    assert not ownership_marker.exists()
    monkeypatch.setattr(worktree_module.os, "fsync", real_fsync)
    provisioned = await LocalWorktreeManager(directories).provision(run_id, repository, "main")
    assert provisioned.path.exists()


@pytest.mark.asyncio
async def test_failed_provision_retains_installed_receipt_when_branch_rollback_fails(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    real_fsync = os.fsync
    real_run_git = worktree_module.run_git
    root_fsync_failed = False

    def fail_first_root_fsync(descriptor: int) -> None:
        nonlocal root_fsync_failed
        if not root_fsync_failed and stat.S_ISDIR(os.fstat(descriptor).st_mode):
            root_fsync_failed = True
            raise OSError("injected ownership marker durability failure")
        real_fsync(descriptor)

    async def fail_branch_rollback(*arguments: str) -> tuple[bytes, int]:
        if len(arguments) >= 3 and arguments[-3:-1] == (
            "-d",
            f"refs/heads/circular/run/{run_id}",
        ):
            return b"", 17
        return await real_run_git(*arguments)

    monkeypatch.setattr(worktree_module.os, "fsync", fail_first_root_fsync)
    monkeypatch.setattr(worktree_module, "run_git", fail_branch_rollback)

    with pytest.raises(WorktreeProvisionError) as caught:
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert caught.value.cleanup_error is not None
    assert _ownership_marker(directories, run_id).is_file()
    assert _branch_exists(repository, f"circular/run/{run_id}")


@pytest.mark.asyncio
async def test_provision_rejects_and_preserves_an_existing_run_path(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    target = directories.run_paths(run_id).worktree
    target.mkdir(parents=True)
    marker = target / "owned-by-another-allocation"
    marker.write_text("preserve me\n")

    with pytest.raises(WorktreeConflictError, match=str(run_id)):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert marker.read_text() == "preserve me\n"
    assert not _branch_exists(repository, f"circular/run/{run_id}")


@pytest.mark.asyncio
async def test_provision_rejects_a_symlink_at_the_run_path(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    outside = tmp_path / "outside"
    outside.mkdir()
    directories.worktree_root.mkdir()
    directories.worktree_root.joinpath(str(run_id)).symlink_to(outside, target_is_directory=True)

    with pytest.raises(WorktreeConflictError, match=str(run_id)):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert directories.worktree_root.joinpath(str(run_id)).is_symlink()
    assert not _branch_exists(repository, f"circular/run/{run_id}")


@pytest.mark.asyncio
async def test_provision_requires_a_direct_uuid_repository_cache_child(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    nested = repository / "nested"
    nested.mkdir()
    run_id = uuid4()

    with pytest.raises(InvalidWorktreeRepository, match=str(run_id)):
        await LocalWorktreeManager(directories).provision(run_id, nested, "main")

    assert not directories.worktree_root.joinpath(str(run_id)).exists()


@pytest.mark.asyncio
async def test_provision_rejects_an_unmanaged_or_invalid_repository(tmp_path: Path) -> None:
    directories = _directories(tmp_path)
    repository_id = uuid4()
    invalid_repository = directories.repository_cache_path(repository_id)
    invalid_repository.mkdir(parents=True)
    marker = invalid_repository / "preserve"
    marker.write_text("not git\n")
    run_id = uuid4()

    with pytest.raises(InvalidWorktreeRepository, match=str(run_id)):
        await LocalWorktreeManager(directories).provision(run_id, invalid_repository, "main")

    assert marker.read_text() == "not git\n"


@pytest.mark.asyncio
async def test_missing_ref_error_is_typed_and_does_not_disclose_the_ref(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    secret_ref = "refs/heads/credential-secret-missing"

    with pytest.raises(WorktreeRefError) as caught:
        await LocalWorktreeManager(directories).provision(run_id, repository, secret_ref)

    assert caught.value.run_id == run_id
    assert caught.value.path == directories.worktree_root / str(run_id)
    assert secret_ref not in str(caught.value)
    assert "credential-secret" not in str(caught.value)
    assert not directories.worktree_root.joinpath(str(run_id)).exists()
    assert not _branch_exists(repository, f"circular/run/{run_id}")


@pytest.mark.asyncio
async def test_ref_is_passed_after_end_of_options(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()

    with pytest.raises(WorktreeRefError):
        await LocalWorktreeManager(directories).provision(run_id, repository, "--help")

    assert not directories.worktree_root.joinpath(str(run_id)).exists()
    assert not _branch_exists(repository, f"circular/run/{run_id}")


@pytest.mark.asyncio
async def test_existing_run_branch_is_a_conflict_and_is_preserved(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    branch = f"circular/run/{run_id}"
    _git(repository, "branch", branch, "main")
    branch_head = _git(repository, "rev-parse", branch)

    with pytest.raises(WorktreeConflictError, match=str(run_id)):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert _git(repository, "rev-parse", branch) == branch_head
    assert not directories.worktree_root.joinpath(str(run_id)).exists()


@pytest.mark.asyncio
async def test_simultaneous_provisions_for_the_same_run_create_one_allocation(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()
    manager = LocalWorktreeManager(directories)

    outcomes = await asyncio.gather(
        manager.provision(run_id, repository, "main"),
        manager.provision(run_id, repository, "main"),
        return_exceptions=True,
    )

    assert sum(isinstance(outcome, ProvisionedWorktree) for outcome in outcomes) == 1
    assert sum(isinstance(outcome, WorktreeConflictError) for outcome in outcomes) == 1
    assert (
        _git(repository, "worktree", "list", "--porcelain").count(
            str(directories.run_paths(run_id).worktree)
        )
        == 1
    )


@pytest.mark.asyncio
async def test_provision_uses_the_repository_cache_lock_file(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    lock_path = repository.with_name(f".{repository.name}.lock")
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    run_id = uuid4()

    try:
        with pytest.raises(WorktreeLockError, match="timed out") as caught:
            await LocalWorktreeManager(directories, lock_timeout_seconds=0.05).provision(
                run_id, repository, "main"
            )
    finally:
        fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)

    assert caught.value.path == repository
    assert caught.value.timed_out is True
    assert not directories.worktree_root.joinpath(str(run_id)).exists()


@pytest.mark.asyncio
@pytest.mark.parametrize("base_ref", ["", "bad\x00ref", "bad\ud800ref"])
async def test_invalid_ref_text_is_typed_before_git_launch(tmp_path: Path, base_ref: str) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    run_id = uuid4()

    with pytest.raises(WorktreeRefError) as caught:
        await LocalWorktreeManager(directories).provision(run_id, repository, base_ref)

    if base_ref:
        assert base_ref not in str(caught.value)
    assert not directories.worktree_root.joinpath(str(run_id)).exists()


@pytest.mark.asyncio
async def test_platform_git_commands_do_not_execute_repository_hooks(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    marker = tmp_path / "repository-hook-ran"
    hook = repository / ".git" / "hooks" / "post-checkout"
    hook.write_text(f"#!/bin/sh\n: > {marker}\n")
    hook.chmod(0o700)

    provisioned = await LocalWorktreeManager(directories).provision(uuid4(), repository, "main")

    assert provisioned.path.exists()
    assert not marker.exists()


@pytest.mark.asyncio
async def test_failed_add_rolls_back_its_staging_path_and_new_branch(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    real_git = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_add_body='"$CIRCULAR_REAL_GIT" "$@" || exit $?\nexit 17',
    )
    run_id = uuid4()
    original_path = os.environ["PATH"]
    monkeypatch.setenv("PATH", f"{real_git}:{original_path}")
    with pytest.raises(WorktreeProvisionError) as caught:
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert caught.value.exit_code != 0
    assert not directories.worktree_root.joinpath(str(run_id)).exists()
    assert not list(directories.worktree_root.glob(f".{run_id}.worktree-*"))
    assert not _branch_exists(repository, f"circular/run/{run_id}")
    assert str(directories.worktree_root) not in _git(repository, "worktree", "list", "--porcelain")
    monkeypatch.setenv("PATH", original_path)
    provisioned = await LocalWorktreeManager(directories).provision(run_id, repository, "main")
    assert provisioned.path == directories.run_paths(run_id).worktree


@pytest.mark.asyncio
async def test_failed_provision_cleanup_never_prunes_another_runs_stale_metadata(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    metadata_only = await manager.provision(uuid4(), repository, "main")
    metadata_only_git_directory = _linked_git_directory(metadata_only.path)
    shutil.rmtree(metadata_only.path)
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_add_body='"$CIRCULAR_REAL_GIT" "$@" || exit $?\nexit 17',
        worktree_remove_body=(
            "last=\n"
            'for value in "$@"; do last=$value; done\n'
            'if [ -e "$last" ]; then exit 17; fi\n'
            'exec "$CIRCULAR_REAL_GIT" "$@"'
        ),
    )
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{os.environ['PATH']}")

    with pytest.raises(WorktreeProvisionError):
        await manager.provision(uuid4(), repository, "main")

    assert metadata_only_git_directory.exists()
    assert str(metadata_only.path) in _git(repository, "worktree", "list", "--porcelain")
    assert _branch_exists(repository, metadata_only.branch)


@pytest.mark.asyncio
async def test_failed_move_unpublishes_the_final_path_metadata_and_branch(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_operation="move",
        worktree_add_body='"$CIRCULAR_REAL_GIT" "$@" || exit $?\nexit 17',
    )
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{os.environ['PATH']}")
    run_id = uuid4()

    with pytest.raises(WorktreeProvisionError):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert not directories.worktree_root.joinpath(str(run_id)).exists()
    assert not list(directories.worktree_root.glob(f".{run_id}.worktree-*"))
    assert not _branch_exists(repository, f"circular/run/{run_id}")
    assert str(directories.worktree_root) not in _git(repository, "worktree", "list", "--porcelain")


@pytest.mark.asyncio
async def test_failed_add_never_follows_a_swapped_staging_symlink(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    outside = tmp_path / "outside-preserved"
    outside.mkdir()
    marker = outside / "marker"
    marker.write_text("preserve me\n")
    monkeypatch.setenv("CIRCULAR_OUTSIDE_PATH", str(outside))
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_add_body=(
            "last=\nsecond_last=\n"
            'for value in "$@"; do second_last=$last; last=$value; done\n'
            'rmdir "$second_last"\n'
            'ln -s "$CIRCULAR_OUTSIDE_PATH" "$second_last"\n'
            "exit 17"
        ),
    )
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{os.environ['PATH']}")
    run_id = uuid4()

    with pytest.raises(WorktreeProvisionError):
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert marker.read_text() == "preserve me\n"
    assert not list(directories.worktree_root.glob(f".{run_id}.worktree-*"))
    assert not directories.worktree_root.joinpath(str(run_id)).exists()


@pytest.mark.asyncio
async def test_failed_provision_preserves_a_branch_that_advanced_from_the_base(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    _git(repository, "config", "user.name", "Circular Tests")
    _git(repository, "config", "user.email", "circular@example.invalid")
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_add_body=(
            '"$CIRCULAR_REAL_GIT" "$@" || exit $?\n'
            "last=\nsecond_last=\n"
            'for value in "$@"; do second_last=$last; last=$value; done\n'
            '"$CIRCULAR_REAL_GIT" -C "$second_last" commit --allow-empty '
            "--message=advanced-after-add || exit $?\n"
            "exit 17"
        ),
    )
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{os.environ['PATH']}")
    run_id = uuid4()
    branch = f"circular/run/{run_id}"
    base_commit = _git(repository, "rev-parse", "main")

    with pytest.raises(WorktreeProvisionError) as caught:
        await LocalWorktreeManager(directories).provision(run_id, repository, "main")

    assert caught.value.cleanup_error is not None
    assert _branch_exists(repository, branch)
    assert _git(repository, "rev-parse", branch) != base_commit
    assert not directories.worktree_root.joinpath(str(run_id)).exists()
    assert not list(directories.worktree_root.glob(f".{run_id}.worktree-*"))


@pytest.mark.asyncio
async def test_cancelling_provision_stops_git_before_releasing_repository_lock(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    hook_pid_file = tmp_path / "git-wrapper.pid"
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_add_body=(
            '"$CIRCULAR_REAL_GIT" "$@" || exit $?\n'
            'printf "%s\\n" "$$" > "$CIRCULAR_HOOK_PID"\n'
            "exec sleep 30"
        ),
    )
    original_path = os.environ["PATH"]
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{original_path}")
    monkeypatch.setenv("CIRCULAR_HOOK_PID", str(hook_pid_file))
    cancelled_run = uuid4()
    task = asyncio.create_task(
        LocalWorktreeManager(directories).provision(cancelled_run, repository, "main")
    )
    hook_pid: int | None = None

    try:
        await _wait_for_path(hook_pid_file)
        hook_pid = int(hook_pid_file.read_text())
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

        await _wait_for_process_exit(hook_pid)
        assert not directories.worktree_root.joinpath(str(cancelled_run)).exists()
        assert not _branch_exists(repository, f"circular/run/{cancelled_run}")
        monkeypatch.setenv("PATH", original_path)
        next_run = uuid4()
        provisioned = await LocalWorktreeManager(directories, lock_timeout_seconds=0.2).provision(
            next_run, repository, "main"
        )
        assert provisioned.path == directories.run_paths(next_run).worktree
    finally:
        if not task.done():
            task.cancel()
        if hook_pid is not None and _process_exists(hook_pid):
            os.kill(hook_pid, signal.SIGKILL)


@pytest.mark.asyncio
async def test_cancelling_release_stops_git_before_releasing_repository_lock(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    hook_pid_file = tmp_path / "release-wrapper.pid"
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_operation="remove",
        worktree_add_body=(
            '"$CIRCULAR_REAL_GIT" "$@" || exit $?\n'
            'printf "%s\\n" "$$" > "$CIRCULAR_HOOK_PID"\n'
            "exec sleep 30"
        ),
    )
    original_path = os.environ["PATH"]
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{original_path}")
    monkeypatch.setenv("CIRCULAR_HOOK_PID", str(hook_pid_file))
    task = asyncio.create_task(manager.release(provisioned))
    hook_pid: int | None = None

    try:
        await _wait_for_path(hook_pid_file)
        hook_pid = int(hook_pid_file.read_text())
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

        await _wait_for_process_exit(hook_pid)
        assert not provisioned.path.exists()
        assert _branch_exists(repository, provisioned.branch)
        monkeypatch.setenv("PATH", original_path)
        await manager.release(provisioned)
        next_run = await LocalWorktreeManager(directories, lock_timeout_seconds=0.2).provision(
            uuid4(), repository, "main"
        )
        assert next_run.path.exists()
    finally:
        if not task.done():
            task.cancel()
        if hook_pid is not None and _process_exists(hook_pid):
            os.kill(hook_pid, signal.SIGKILL)


@pytest.mark.asyncio
async def test_cancelling_stale_release_interrupts_git_before_destructive_cleanup(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    hook_pid_file = tmp_path / "stale-release-wrapper.pid"
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_operation="list",
        worktree_add_body=('printf "%s\\n" "$$" > "$CIRCULAR_HOOK_PID"\nexec sleep 2'),
    )
    original_path = os.environ["PATH"]
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{original_path}")
    monkeypatch.setenv("CIRCULAR_HOOK_PID", str(hook_pid_file))
    task = asyncio.create_task(manager.release(provisioned))
    hook_pid: int | None = None

    try:
        await _wait_for_path(hook_pid_file)
        hook_pid = int(hook_pid_file.read_text())
        started_cancelling = asyncio.get_running_loop().time()
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task
        cancellation_seconds = asyncio.get_running_loop().time() - started_cancelling

        assert cancellation_seconds < 1
        await _wait_for_process_exit(hook_pid)
        assert provisioned.path.exists()
        assert _ownership_marker(directories, provisioned.run_id).is_file()
        monkeypatch.setenv("PATH", original_path)
        await manager.release(provisioned)
        assert not provisioned.path.exists()
        assert _branch_exists(repository, provisioned.branch)
    finally:
        if not task.done():
            task.cancel()
        if hook_pid is not None and _process_exists(hook_pid):
            os.kill(hook_pid, signal.SIGKILL)


@pytest.mark.asyncio
async def test_cancelling_stale_directory_release_finishes_cleanup_before_unlocking(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    for index in range(1_000):
        provisioned.path.joinpath(f"cleanup-{index:04}.txt").write_text("cleanup\n")
    task = asyncio.create_task(manager.release(provisioned))

    await _wait_for_path_to_disappear_while_parent_remains(
        provisioned.path / ".git", provisioned.path
    )
    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    assert not provisioned.path.exists()
    assert _branch_exists(repository, provisioned.branch)
    await manager.release(provisioned)


@pytest.mark.asyncio
async def test_cleanup_failure_does_not_replace_a_release_cancellation(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    cleanup_started = asyncio.Event()
    allow_cleanup_failure = asyncio.Event()
    real_remove_owned_tree = worktree_module.remove_owned_tree

    async def fail_remove_owned_tree(path: Path, *, deadline: float) -> None:
        assert path == provisioned.path
        assert deadline > asyncio.get_running_loop().time()
        cleanup_started.set()
        await allow_cleanup_failure.wait()
        raise OSError("injected release cleanup failure")

    monkeypatch.setattr(worktree_module, "remove_owned_tree", fail_remove_owned_tree)
    task = asyncio.create_task(manager.release(provisioned))

    try:
        await asyncio.wait_for(cleanup_started.wait(), timeout=5)
        assert task.cancel()
        await asyncio.sleep(0.05)
        assert not task.done()
        allow_cleanup_failure.set()
        with pytest.raises(asyncio.CancelledError) as caught:
            await task
        assert caught.value.__cause__ is not None
    finally:
        if not task.done():
            task.cancel()

    monkeypatch.setattr(worktree_module, "remove_owned_tree", real_remove_owned_tree)
    await manager.release(provisioned)
    assert not provisioned.path.exists()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_basic_release_removes_worktree_but_preserves_run_branch(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    (provisioned.path / "run-output.txt").write_text("result\n")
    _git(provisioned.path, "add", "run-output.txt")
    _git(provisioned.path, "commit", "--message=run-output")
    branch_head = _git(provisioned.path, "rev-parse", "HEAD")
    _ownership_marker(directories, provisioned.run_id).unlink()

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert not _ownership_marker(directories, provisioned.run_id).exists()
    assert _git(repository, "rev-parse", provisioned.branch) == branch_head
    await manager.release(provisioned)
    assert _git(repository, "rev-parse", provisioned.branch) == branch_head


@pytest.mark.asyncio
async def test_fully_absent_release_remains_idempotent_after_run_branch_is_deleted(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    await manager.release(provisioned)
    _git(repository, "update-ref", "-d", f"refs/heads/{provisioned.branch}")

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert not _ownership_marker(directories, provisioned.run_id).exists()


@pytest.mark.asyncio
async def test_release_removes_metadata_left_after_the_worktree_directory_disappears(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    active = await manager.provision(uuid4(), repository, "main")
    branch_head = _git(repository, "rev-parse", provisioned.branch)
    shutil.rmtree(provisioned.path)
    assert str(provisioned.path) in _git(repository, "worktree", "list", "--porcelain")

    await manager.release(provisioned)

    assert str(provisioned.path) not in _git(repository, "worktree", "list", "--porcelain")
    assert _git(repository, "rev-parse", provisioned.branch) == branch_head
    assert active.path.exists()
    assert str(active.path) in _git(repository, "worktree", "list", "--porcelain")
    await manager.release(provisioned)


@pytest.mark.asyncio
async def test_metadata_only_release_preserves_evidence_when_run_branch_is_missing(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    git_directory = _linked_git_directory(provisioned.path)
    ownership_marker = _ownership_marker(directories, provisioned.run_id)
    shutil.rmtree(provisioned.path)
    _git(repository, "update-ref", "-d", f"refs/heads/{provisioned.branch}")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert git_directory.exists()
    assert ownership_marker.is_file()
    assert str(provisioned.path) in _git(repository, "worktree", "list", "--porcelain")


@pytest.mark.asyncio
async def test_metadata_only_release_preserves_evidence_when_run_branch_moves_after_listing(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    provisioned.path.joinpath("run-output.txt").write_text("result\n")
    _git(provisioned.path, "add", "run-output.txt")
    _git(provisioned.path, "commit", "--message=run-output")
    registered_head = _git(repository, "rev-parse", provisioned.branch)
    moved_head = _git(repository, "rev-parse", f"{provisioned.branch}^")
    git_directory = _linked_git_directory(provisioned.path)
    ownership_marker = _ownership_marker(directories, provisioned.run_id)
    shutil.rmtree(provisioned.path)
    real_run_git = worktree_module.run_git
    branch_moved = False

    async def move_branch_after_listing(*arguments: str) -> tuple[bytes, int]:
        nonlocal branch_moved
        output, returncode = await real_run_git(*arguments)
        if not branch_moved and arguments[-4:] == (
            "worktree",
            "list",
            "--porcelain",
            "-z",
        ):
            _git(
                repository,
                "update-ref",
                f"refs/heads/{provisioned.branch}",
                moved_head,
                registered_head,
            )
            branch_moved = True
        return output, returncode

    monkeypatch.setattr(worktree_module, "run_git", move_branch_after_listing)

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert branch_moved
    assert _git(repository, "rev-parse", provisioned.branch) == moved_head
    assert git_directory.exists()
    assert ownership_marker.is_file()
    assert str(provisioned.path) in _git(repository, "worktree", "list", "--porcelain")


@pytest.mark.asyncio
async def test_receipt_only_release_preserves_evidence_when_run_branch_is_missing(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    git_directory = _linked_git_directory(provisioned.path)
    ownership_marker = _ownership_marker(directories, provisioned.run_id)
    shutil.rmtree(provisioned.path)
    shutil.rmtree(git_directory)
    _git(repository, "update-ref", "-d", f"refs/heads/{provisioned.branch}")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert ownership_marker.is_file()


@pytest.mark.asyncio
async def test_metadata_recovery_parses_newlines_in_worktree_paths(tmp_path: Path) -> None:
    newline_root = tmp_path / "newline\nin-root"
    newline_root.mkdir()
    source = _create_repository(newline_root / "source")
    directories = _directories(newline_root)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(provisioned.path)

    await manager.release(provisioned)

    assert str(provisioned.path) not in _git(repository, "worktree", "list", "--porcelain")
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_malformed_worktree_porcelain_is_reported_as_a_typed_release_failure(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    wrapper_directory = _install_git_wrapper(
        tmp_path,
        monkeypatch=monkeypatch,
        worktree_operation="list",
        worktree_add_body='printf "malformed porcelain"\nexit 0',
    )
    monkeypatch.setenv("PATH", f"{wrapper_directory}:{os.environ['PATH']}")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert provisioned.path.exists()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_removes_the_exact_directory_left_after_metadata_disappears(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    active = await manager.provision(uuid4(), repository, "main")
    branch_head = _git(repository, "rev-parse", provisioned.branch)
    _ownership_marker(directories, provisioned.run_id).unlink()
    git_directory = _linked_git_directory(provisioned.path)
    shutil.rmtree(git_directory)
    assert provisioned.path.exists()
    assert str(provisioned.path) not in _git(repository, "worktree", "list", "--porcelain")

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert not _ownership_marker(directories, provisioned.run_id).exists()
    assert _git(repository, "rev-parse", provisioned.branch) == branch_head
    assert active.path.exists()
    assert str(active.path) in _git(repository, "worktree", "list", "--porcelain")
    await manager.release(provisioned)


@pytest.mark.asyncio
async def test_release_resumes_after_directory_cleanup_already_removed_git_backpointer(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    provisioned.path.joinpath(".git").unlink()
    remaining = provisioned.path / "partially-cleaned-output.txt"
    remaining.write_text("remove on retry\n")

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert not _ownership_marker(directories, provisioned.run_id).exists()
    assert _branch_exists(repository, provisioned.branch)
    await manager.release(provisioned)


@pytest.mark.asyncio
async def test_stale_directory_with_a_missing_run_branch_is_preserved(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    provisioned.path.joinpath(".git").unlink()
    _git(repository, "update-ref", "-d", f"refs/heads/{provisioned.branch}")
    remaining = provisioned.path / "unverified-output.txt"
    remaining.write_text("preserve\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert remaining.read_text() == "preserve\n"
    assert _ownership_marker(directories, provisioned.run_id).is_file()


@pytest.mark.asyncio
async def test_ownership_marker_never_authorizes_a_replacement_run_directory(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    shutil.rmtree(provisioned.path)
    provisioned.path.mkdir()
    replacement = provisioned.path / "replacement-owner.txt"
    replacement.write_text("preserve replacement\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert replacement.read_text() == "preserve replacement\n"
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_legacy_double_loss_without_an_ownership_marker_is_preserved_for_recovery(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    _ownership_marker(directories, provisioned.run_id).unlink()
    shutil.rmtree(_linked_git_directory(provisioned.path))
    provisioned.path.joinpath(".git").unlink()
    remaining = provisioned.path / "unverified-legacy-output.txt"
    remaining.write_text("preserve for operator\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert remaining.read_text() == "preserve for operator\n"
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_never_follows_a_symlinked_ownership_marker(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    ownership_marker = _ownership_marker(directories, provisioned.run_id)
    outside_marker = tmp_path / "outside-ownership-marker"
    outside_marker.write_bytes(ownership_marker.read_bytes())
    ownership_marker.unlink()
    ownership_marker.symlink_to(outside_marker)
    shutil.rmtree(_linked_git_directory(provisioned.path))
    provisioned.path.joinpath(".git").unlink()
    remaining = provisioned.path / "preserved-output.txt"
    remaining.write_text("preserve\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert ownership_marker.is_symlink()
    assert outside_marker.is_file()
    assert remaining.read_text() == "preserve\n"


@pytest.mark.asyncio
@pytest.mark.parametrize("marker_kind", ["malformed", "fifo"])
async def test_invalid_ownership_markers_fail_closed_without_blocking(
    tmp_path: Path,
    marker_kind: str,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    ownership_marker = _ownership_marker(directories, provisioned.run_id)
    ownership_marker.unlink()
    if marker_kind == "malformed":
        ownership_marker.write_bytes(b"partial receipt")
    else:
        os.mkfifo(ownership_marker)
    shutil.rmtree(_linked_git_directory(provisioned.path))
    provisioned.path.joinpath(".git").unlink()
    remaining = provisioned.path / "preserved-output.txt"
    remaining.write_text("preserve\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await asyncio.wait_for(manager.release(provisioned), timeout=0.2)

    assert ownership_marker.exists()
    assert remaining.read_text() == "preserve\n"


@pytest.mark.asyncio
async def test_releasing_one_run_never_prunes_another_runs_stale_metadata(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    directory_only = await manager.provision(uuid4(), repository, "main")
    metadata_only = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(directory_only.path))
    metadata_only_git_directory = _linked_git_directory(metadata_only.path)
    shutil.rmtree(metadata_only.path)
    assert metadata_only_git_directory.exists()

    await manager.release(directory_only)

    assert not directory_only.path.exists()
    assert metadata_only_git_directory.exists()
    assert str(metadata_only.path) in _git(repository, "worktree", "list", "--porcelain")
    assert _branch_exists(repository, directory_only.branch)
    assert _branch_exists(repository, metadata_only.branch)


@pytest.mark.asyncio
async def test_releasing_one_metadata_only_run_preserves_another_registration(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    released = await manager.provision(uuid4(), repository, "main")
    retained = await manager.provision(uuid4(), repository, "main")
    released_git_directory = _linked_git_directory(released.path)
    retained_git_directory = _linked_git_directory(retained.path)
    shutil.rmtree(released.path)
    shutil.rmtree(retained.path)

    await manager.release(released)

    assert not released_git_directory.exists()
    assert retained_git_directory.exists()
    registrations = _git(repository, "worktree", "list", "--porcelain")
    assert str(released.path) not in registrations
    assert str(retained.path) in registrations
    assert not _ownership_marker(directories, released.run_id).exists()
    assert _ownership_marker(directories, retained.run_id).is_file()
    assert _branch_exists(repository, released.branch)
    assert _branch_exists(repository, retained.branch)


@pytest.mark.asyncio
async def test_release_preserves_a_dirty_worktree_for_explicit_recovery(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    readme = provisioned.path / "README.md"
    readme.write_text("unsaved change\n")
    untracked = provisioned.path / "untracked.txt"
    untracked.write_text("preserve me\n")

    with pytest.raises(WorktreeReleaseError) as caught:
        await manager.release(provisioned)

    assert caught.value.exit_code is None
    assert readme.read_text() == "unsaved change\n"
    assert untracked.read_text() == "preserve me\n"
    assert str(provisioned.path) in _git(repository, "worktree", "list", "--porcelain")
    assert _ownership_marker(directories, provisioned.run_id).is_file()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_preserves_ignored_run_output_for_explicit_recovery(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    provisioned.path.joinpath(".gitignore").write_text("*.log\n")
    _git(provisioned.path, "add", ".gitignore")
    _git(provisioned.path, "commit", "--message=ignore-run-logs")
    ignored_output = provisioned.path / "execution.log"
    ignored_output.write_bytes(b"agent output\xff\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert ignored_output.read_bytes() == b"agent output\xff\n"
    assert _ownership_marker(directories, provisioned.run_id).is_file()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_reports_non_utf8_symbolic_ref_as_a_typed_failure(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    commit = _git(provisioned.path, "rev-parse", "HEAD").encode("ascii")
    invalid_ref = b"refs/heads/invalid-\xff"
    ref_path = os.fsencode(repository / ".git") + b"/" + invalid_ref
    descriptor = os.open(ref_path, os.O_CLOEXEC | os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    try:
        os.write(descriptor, commit + b"\n")
    finally:
        os.close(descriptor)
    _linked_git_directory(provisioned.path).joinpath("HEAD").write_bytes(
        b"ref: " + invalid_ref + b"\n"
    )

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert provisioned.path.exists()
    assert _ownership_marker(directories, provisioned.run_id).is_file()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_rejects_forged_path_branch_and_repository(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    other_source = _create_repository(tmp_path / "other-source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    other_repository = await _cached_repository(directories, other_source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    forged_values = [
        ProvisionedWorktree(
            run_id=provisioned.run_id,
            repository_path=provisioned.repository_path,
            path=directories.worktree_root / str(uuid4()),
            branch=provisioned.branch,
        ),
        ProvisionedWorktree(
            run_id=provisioned.run_id,
            repository_path=provisioned.repository_path,
            path=provisioned.path,
            branch="circular/run/forged",
        ),
        ProvisionedWorktree(
            run_id=provisioned.run_id,
            repository_path=other_repository,
            path=provisioned.path,
            branch=provisioned.branch,
        ),
    ]

    for forged in forged_values:
        with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
            await manager.release(forged)
        assert provisioned.path.exists()


@pytest.mark.asyncio
async def test_release_never_targets_a_forged_path_outside_the_worktree_root(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    outside = tmp_path / "outside-forged-handle"
    outside.mkdir()
    marker = outside / "marker"
    marker.write_text("preserve\n")
    forged = ProvisionedWorktree(
        run_id=provisioned.run_id,
        repository_path=repository,
        path=outside,
        branch=provisioned.branch,
    )

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(forged)

    assert marker.read_text() == "preserve\n"
    assert provisioned.path.exists()


@pytest.mark.asyncio
async def test_release_rejects_a_stale_directory_with_an_unmanaged_git_backpointer(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    outside = tmp_path / "outside-git-metadata"
    outside.mkdir()
    marker = outside / "marker"
    marker.write_text("preserve\n")
    provisioned.path.joinpath(".git").write_text(f"gitdir: {outside}\n")

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)

    assert marker.read_text() == "preserve\n"
    assert provisioned.path.exists()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_rejects_a_non_regular_git_backpointer_without_blocking(
    tmp_path: Path,
) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    git_file = provisioned.path / ".git"
    git_file.unlink()
    os.mkfifo(git_file)

    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await asyncio.wait_for(manager.release(provisioned), timeout=0.2)

    assert git_file.is_fifo()
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_forged_backpointer_cannot_prune_another_active_worktree(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    stale = await manager.provision(uuid4(), repository, "main")
    active = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(stale.path))
    active_git_directory = _linked_git_directory(active.path)
    stale.path.joinpath(".git").write_text(f"gitdir: {active_git_directory}\n")

    with pytest.raises(WorktreeReleaseError, match=str(stale.run_id)):
        await manager.release(stale)

    assert stale.path.exists()
    assert active.path.exists()
    assert str(active.path) in _git(repository, "worktree", "list", "--porcelain")
    assert _branch_exists(repository, active.branch)
    assert _branch_exists(repository, stale.branch)


@pytest.mark.asyncio
async def test_stale_directory_cleanup_never_follows_nested_symlinks(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    repository = await _cached_repository(directories, source)
    manager = LocalWorktreeManager(directories)
    provisioned = await manager.provision(uuid4(), repository, "main")
    shutil.rmtree(_linked_git_directory(provisioned.path))
    outside = tmp_path / "outside-nested-symlink"
    outside.mkdir()
    marker = outside / "marker"
    marker.write_text("preserve\n")
    provisioned.path.joinpath("outside-link").symlink_to(outside, target_is_directory=True)

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert marker.read_text() == "preserve\n"
    assert _branch_exists(repository, provisioned.branch)


@pytest.mark.asyncio
async def test_release_rejects_a_symlinked_forged_run_target(tmp_path: Path) -> None:
    directories = _directories(tmp_path)
    run_id = uuid4()
    outside = tmp_path / "outside-release"
    outside.mkdir()
    marker = outside / "marker"
    marker.write_text("preserve\n")
    directories.worktree_root.mkdir()
    target = directories.worktree_root / str(run_id)
    target.symlink_to(outside, target_is_directory=True)
    forged = ProvisionedWorktree(
        run_id=run_id,
        repository_path=directories.repository_cache_root / str(uuid4()),
        path=target,
        branch=f"circular/run/{run_id}",
    )

    with pytest.raises(WorktreeReleaseError, match=str(run_id)):
        await LocalWorktreeManager(directories).release(forged)

    assert target.is_symlink()
    assert marker.read_text() == "preserve\n"


def _directories(tmp_path: Path) -> ExecutionDirectories:
    return ExecutionDirectories(
        repository_cache_root=tmp_path / "cache",
        worktree_root=tmp_path / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "docker-worktrees",
    )


async def _cached_repository(directories: ExecutionDirectories, source: Path) -> Path:
    return await LocalRepositoryCache(directories).checkout(uuid4(), str(source))


def _create_repository(path: Path) -> Path:
    path.mkdir()
    _git(path, "init", "--initial-branch=main")
    _git(path, "config", "user.name", "Circular Tests")
    _git(path, "config", "user.email", "circular@example.invalid")
    (path / "README.md").write_text("first\n")
    _git(path, "add", "README.md")
    _git(path, "commit", "--message=first")
    return path


def _git(repository: Path, *arguments: str) -> str:
    completed = subprocess.run(
        ["git", "-C", str(repository), *arguments],
        check=True,
        capture_output=True,
        text=True,
    )
    return completed.stdout.strip()


def _branch_exists(repository: Path, branch: str) -> bool:
    completed = subprocess.run(
        ["git", "-C", str(repository), "show-ref", "--verify", "--quiet", f"refs/heads/{branch}"],
        check=False,
    )
    return completed.returncode == 0


def _linked_git_directory(worktree: Path) -> Path:
    prefix = "gitdir: "
    contents = worktree.joinpath(".git").read_text().strip()
    assert contents.startswith(prefix)
    return Path(contents.removeprefix(prefix))


def _ownership_marker(directories: ExecutionDirectories, run_id: UUID) -> Path:
    return directories.worktree_root / f".{run_id}.owner"


def _install_git_wrapper(
    tmp_path: Path,
    *,
    monkeypatch: pytest.MonkeyPatch | None,
    worktree_add_body: str,
    worktree_operation: str = "add",
    worktree_remove_body: str | None = None,
) -> Path:
    real_git = shutil.which("git")
    assert real_git is not None
    wrapper_directory = tmp_path / "fake-git-bin"
    wrapper_directory.mkdir(exist_ok=True)
    wrapper = wrapper_directory / "git"
    remove_case = ""
    if worktree_remove_body is not None:
        remove_case = (
            '  if [ "$previous" = worktree ] && [ "$argument" = remove ]; then\n'
            f"    {worktree_remove_body}\n"
            "  fi\n"
        )
    wrapper.write_text(
        "#!/bin/sh\n"
        "previous=\n"
        'for argument in "$@"; do\n'
        f'  if [ "$previous" = worktree ] && [ "$argument" = {worktree_operation} ]; then\n'
        f"    {worktree_add_body}\n"
        "  fi\n"
        f"{remove_case}"
        "  previous=$argument\n"
        "done\n"
        'exec "$CIRCULAR_REAL_GIT" "$@"\n'
    )
    wrapper.chmod(0o700)
    if monkeypatch is None:
        os.environ["CIRCULAR_REAL_GIT"] = real_git
    else:
        monkeypatch.setenv("CIRCULAR_REAL_GIT", real_git)
    return wrapper_directory


async def _wait_for_path(path: Path) -> None:
    loop = asyncio.get_running_loop()
    deadline = loop.time() + 2
    while not path.exists():  # noqa: ASYNC240 - test-only polling
        if loop.time() >= deadline:
            pytest.fail(f"timed out waiting for {path}")
        await asyncio.sleep(0.01)


async def _wait_for_path_to_disappear_while_parent_remains(path: Path, parent: Path) -> None:
    loop = asyncio.get_running_loop()
    deadline = loop.time() + 2
    while path.exists():  # noqa: ASYNC240 - test-only polling
        if not parent.exists():  # noqa: ASYNC240 - test-only polling
            pytest.fail("stale worktree cleanup completed before cancellation could be tested")
        if loop.time() >= deadline:
            pytest.fail(f"timed out waiting for partial cleanup of {parent}")
        await asyncio.sleep(0)
    assert parent.exists()  # noqa: ASYNC240 - test-only polling


async def _wait_for_process_exit(process_id: int) -> None:
    loop = asyncio.get_running_loop()
    deadline = loop.time() + 2
    while _process_exists(process_id):
        if loop.time() >= deadline:
            pytest.fail(f"process {process_id} survived provision cancellation")
        await asyncio.sleep(0.01)


def _process_exists(process_id: int) -> bool:
    try:
        os.kill(process_id, 0)
    except ProcessLookupError:
        return False
    return True
