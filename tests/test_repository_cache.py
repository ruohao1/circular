from __future__ import annotations

import asyncio
import fcntl
import os
import signal
import subprocess
from pathlib import Path
from uuid import uuid4

import pytest
from circular.git import (
    InvalidRepositoryCache,
    LocalRepositoryCache,
    RepositoryCloneError,
    RepositoryFetchError,
    RepositoryLockError,
)
from circular.runners import ExecutionDirectories


@pytest.mark.asyncio
async def test_checkout_clones_repository_on_first_use(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)

    checkout = await LocalRepositoryCache(directories).checkout(repository_id, str(source))

    assert checkout == directories.repository_cache_path(repository_id)
    assert _git(checkout, "rev-parse", "--is-inside-work-tree") == "true"
    assert _git(checkout, "rev-parse", "HEAD") == _git(source, "rev-parse", "HEAD")
    linked_worktree = tmp_path / "linked-worktree"
    _git(checkout, "worktree", "add", "--detach", str(linked_worktree), "HEAD")
    assert (linked_worktree / "README.md").read_text() == "first\n"


@pytest.mark.asyncio
async def test_checkout_reuses_an_existing_repository(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    cache = LocalRepositoryCache(_directories(tmp_path))

    first = await cache.checkout(repository_id, str(source))
    second = await cache.checkout(repository_id, str(source))

    assert second == first
    assert _git(second, "rev-parse", "--is-inside-work-tree") == "true"


@pytest.mark.asyncio
async def test_checkout_refreshes_the_default_base_ref(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    cache = LocalRepositoryCache(_directories(tmp_path))
    checkout = await cache.checkout(repository_id, str(source))
    previous_head = _git(checkout, "rev-parse", "HEAD")

    (source / "README.md").write_text("second\n")
    _git(source, "add", "README.md")
    _git(source, "commit", "--message=second")
    source_head = _git(source, "rev-parse", "HEAD")

    refreshed = await cache.checkout(repository_id, str(source))

    assert source_head != previous_head
    assert _git(refreshed, "rev-parse", "HEAD") == source_head
    assert _git(refreshed, "rev-parse", "refs/heads/main") == source_head


@pytest.mark.asyncio
async def test_simultaneous_callers_share_one_valid_checkout(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)

    checkouts = await asyncio.gather(
        *(LocalRepositoryCache(directories).checkout(repository_id, str(source)) for _ in range(8))
    )

    assert checkouts == [directories.repository_cache_path(repository_id)] * 8
    assert _git(checkouts[0], "fsck", "--no-progress") == ""


@pytest.mark.asyncio
async def test_checkout_rejects_an_existing_non_repository(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)
    target = directories.repository_cache_path(repository_id)
    target.mkdir(parents=True)
    marker = target / "owned-by-someone-else"
    marker.write_text("preserve me\n")

    with pytest.raises(InvalidRepositoryCache, match=str(repository_id)):
        await LocalRepositoryCache(directories).checkout(repository_id, str(source))

    assert marker.read_text() == "preserve me\n"


@pytest.mark.asyncio
async def test_checkout_rejects_a_symlink_at_the_managed_target(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    directories = _directories(tmp_path)
    existing_id = uuid4()
    existing = await LocalRepositoryCache(directories).checkout(existing_id, str(source))
    symlink_id = uuid4()
    directories.repository_cache_root.joinpath(str(symlink_id)).symlink_to(
        existing, target_is_directory=True
    )

    with pytest.raises(InvalidRepositoryCache, match=str(symlink_id)):
        await LocalRepositoryCache(directories).checkout(symlink_id, str(source))


@pytest.mark.asyncio
async def test_clone_failure_is_typed_and_does_not_disclose_the_url(tmp_path: Path) -> None:
    repository_id = uuid4()
    directories = _directories(tmp_path)
    clone_url = str(tmp_path / "credential-secret-missing.git")

    with pytest.raises(RepositoryCloneError) as caught:
        await LocalRepositoryCache(directories).checkout(repository_id, clone_url)

    error = caught.value
    assert error.repository_id == repository_id
    assert error.path == directories.repository_cache_path(repository_id)
    assert error.exit_code != 0
    assert "clone failed" in str(error)
    assert clone_url not in str(error)
    assert "credential-secret-missing" not in str(error)


@pytest.mark.asyncio
async def test_fetch_failure_is_typed_and_does_not_disclose_the_remote(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)
    cache = LocalRepositoryCache(directories)
    checkout = await cache.checkout(repository_id, str(source))
    unavailable_remote = str(tmp_path / "fetch-credential-secret-missing.git")

    with pytest.raises(RepositoryFetchError) as caught:
        await cache.checkout(repository_id, unavailable_remote)

    error = caught.value
    assert error.repository_id == repository_id
    assert error.path == checkout
    assert error.exit_code != 0
    assert "refresh failed" in str(error)
    assert unavailable_remote not in str(error)
    assert "fetch-credential-secret" not in str(error)
    assert _git(checkout, "remote", "get-url", "origin") == str(source)
    assert await cache.checkout(repository_id, str(source)) == checkout


@pytest.mark.asyncio
async def test_failed_clone_does_not_poison_the_next_attempt(tmp_path: Path) -> None:
    repository_id = uuid4()
    directories = _directories(tmp_path)
    cache = LocalRepositoryCache(directories)
    source = tmp_path / "source-created-after-failure"

    with pytest.raises(RepositoryCloneError):
        await cache.checkout(repository_id, str(source))

    assert not directories.repository_cache_path(repository_id).exists()
    _create_repository(source)
    checkout = await cache.checkout(repository_id, str(source))

    assert _git(checkout, "rev-parse", "--is-inside-work-tree") == "true"


@pytest.mark.asyncio
async def test_lock_open_failure_is_typed(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)
    directories.repository_cache_root.mkdir()
    target = directories.repository_cache_path(repository_id)
    target.with_name(f".{target.name}.lock").mkdir()

    with pytest.raises(RepositoryLockError, match="lock failed") as caught:
        await LocalRepositoryCache(directories).checkout(repository_id, str(source))

    assert caught.value.repository_id == repository_id
    assert caught.value.path == directories.repository_cache_path(repository_id)
    assert caught.value.timed_out is False


@pytest.mark.asyncio
async def test_repository_locks_are_independent_and_bounded(tmp_path: Path) -> None:
    source = _create_repository(tmp_path / "source")
    locked_repository_id = uuid4()
    other_repository_id = uuid4()
    directories = _directories(tmp_path)
    directories.repository_cache_root.mkdir()
    locked_target = directories.repository_cache_path(locked_repository_id)
    lock_path = locked_target.with_name(f".{locked_target.name}.lock")
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)

    try:
        other_checkout = await LocalRepositoryCache(directories).checkout(
            other_repository_id, str(source)
        )
        with pytest.raises(RepositoryLockError, match="timed out") as caught:
            await LocalRepositoryCache(directories, lock_timeout_seconds=0.05).checkout(
                locked_repository_id, str(source)
            )
    finally:
        fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)

    assert other_checkout == directories.repository_cache_path(other_repository_id)
    assert caught.value.repository_id == locked_repository_id
    assert caught.value.timed_out is True


@pytest.mark.asyncio
async def test_clone_command_launch_failure_is_typed(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    repository_id = uuid4()
    directories = _directories(tmp_path)
    monkeypatch.setenv("PATH", str(tmp_path / "empty-bin-directory"))

    with pytest.raises(RepositoryCloneError) as caught:
        await LocalRepositoryCache(directories).checkout(
            repository_id, str(tmp_path / "unused-source")
        )

    assert caught.value.repository_id == repository_id
    assert caught.value.exit_code is None


@pytest.mark.asyncio
async def test_cancelling_checkout_stops_git_before_releasing_the_cache(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    source = _create_repository(tmp_path / "source")
    repository_id = uuid4()
    directories = _directories(tmp_path)
    hook_pid_file = tmp_path / "hook.pid"
    hook = tmp_path / "slow-pack-objects"
    hook.write_text('#!/bin/sh\nprintf "%s\\n" "$$" > "$CIRCULAR_TEST_HOOK_PID"\nexec sleep 30\n')
    hook.chmod(0o700)
    git_config = tmp_path / "git-config"
    _set_git_config(git_config, "uploadpack.packObjectsHook", str(hook))
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", str(git_config))
    monkeypatch.setenv("CIRCULAR_TEST_HOOK_PID", str(hook_pid_file))
    task = asyncio.create_task(
        LocalRepositoryCache(directories).checkout(repository_id, f"file://{source}")
    )
    hook_pid: int | None = None

    try:
        await _wait_for_path(hook_pid_file)
        hook_pid = int(hook_pid_file.read_text())
        task.cancel()

        with pytest.raises(asyncio.CancelledError):
            await task

        await _wait_for_process_exit(hook_pid)
        assert not directories.repository_cache_path(repository_id).exists()
    finally:
        if not task.done():
            task.cancel()
        if hook_pid is not None and _process_exists(hook_pid):
            os.kill(hook_pid, signal.SIGKILL)


def _directories(tmp_path: Path) -> ExecutionDirectories:
    return ExecutionDirectories(
        repository_cache_root=tmp_path / "cache",
        worktree_root=tmp_path / "worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "docker-worktrees",
    )


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


def _set_git_config(config: Path, key: str, value: str) -> None:
    subprocess.run(
        ["git", "config", "--file", str(config), key, value],
        check=True,
    )


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
            pytest.fail(f"process {process_id} survived checkout cancellation")
        await asyncio.sleep(0.01)


def _process_exists(process_id: int) -> bool:
    try:
        os.kill(process_id, 0)
    except ProcessLookupError:
        return False
    return True
