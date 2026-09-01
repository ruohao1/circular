from __future__ import annotations

import asyncio
import fcntl
import os
import shutil
import signal
import subprocess
from pathlib import Path
from uuid import uuid4

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

    await manager.release(provisioned)

    assert not provisioned.path.exists()
    assert _git(repository, "rev-parse", provisioned.branch) == branch_head
    with pytest.raises(WorktreeReleaseError, match=str(provisioned.run_id)):
        await manager.release(provisioned)


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


def _install_git_wrapper(
    tmp_path: Path,
    *,
    monkeypatch: pytest.MonkeyPatch | None,
    worktree_add_body: str,
    worktree_operation: str = "add",
) -> Path:
    real_git = shutil.which("git")
    assert real_git is not None
    wrapper_directory = tmp_path / "fake-git-bin"
    wrapper_directory.mkdir(exist_ok=True)
    wrapper = wrapper_directory / "git"
    wrapper.write_text(
        "#!/bin/sh\n"
        "previous=\n"
        'for argument in "$@"; do\n'
        f'  if [ "$previous" = worktree ] && [ "$argument" = {worktree_operation} ]; then\n'
        f"    {worktree_add_body}\n"
        "  fi\n"
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
