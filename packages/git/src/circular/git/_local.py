from __future__ import annotations

import asyncio
import fcntl
import os
import signal
import stat
from collections.abc import AsyncIterator, Callable, Mapping
from contextlib import asynccontextmanager
from pathlib import Path


class GitLaunchError(RuntimeError):
    """The local Git executable could not be launched."""


async def run_git(
    *arguments: str,
    extra_environment: Mapping[str, str] | None = None,
) -> tuple[bytes, int]:
    """Run one credential-noninteractive Git command without a shell.

    All Git subprocesses use a separate process group. Cancellation terminates
    and awaits the complete group before returning control to a caller that may
    release a Repository metadata lock.
    """

    environment = os.environ.copy()
    environment["GIT_ALLOW_PROTOCOL"] = "file:https"
    environment["GIT_TERMINAL_PROMPT"] = "0"
    environment["GCM_INTERACTIVE"] = "Never"
    safe_directory: tuple[str, ...] = ()
    if "-C" in arguments:
        safe_directory = ("-c", f"safe.directory={arguments[arguments.index('-C') + 1]}")
    if extra_environment is not None:
        environment.update(extra_environment)
    try:
        process = await asyncio.create_subprocess_exec(
            "git",
            "-c",
            "protocol.allow=never",
            "-c",
            "protocol.file.allow=always",
            "-c",
            "protocol.https.allow=always",
            "-c",
            "core.hooksPath=/dev/null",
            *safe_directory,
            *arguments,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=environment,
            start_new_session=True,
        )
    except (OSError, ValueError, UnicodeError) as error:
        raise GitLaunchError from error
    try:
        stdout, _ = await process.communicate()
    except asyncio.CancelledError:
        cleanup = asyncio.create_task(_terminate_process_group(process))
        while not cleanup.done():
            try:
                await asyncio.shield(cleanup)
            except asyncio.CancelledError:
                continue
        await cleanup
        raise
    return stdout, process.returncode or 0


@asynccontextmanager
async def repository_lock(
    target: Path,
    *,
    timeout_seconds: float,
    error_factory: Callable[[bool], BaseException],
) -> AsyncIterator[None]:
    """Serialize mutations for one UUID-owned local Repository checkout."""

    lock_path = target.with_name(f".{target.name}.lock")
    flags = os.O_CLOEXEC | os.O_CREAT | os.O_RDWR
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(lock_path, flags, 0o600)
    except OSError as error:
        raise error_factory(False) from error
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout_seconds
    try:
        while True:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                remaining = deadline - loop.time()
                if remaining <= 0:
                    raise error_factory(True) from None
                await asyncio.sleep(min(0.01, remaining))
            except OSError as error:
                raise error_factory(False) from error
        yield
    finally:
        release_error: OSError | None = None
        try:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        except OSError as error:
            release_error = error
        try:
            os.close(descriptor)
        except OSError as error:
            release_error = release_error or error
        if release_error is not None:
            raise error_factory(False) from release_error


def has_valid_primary_checkout(target: Path) -> bool:
    git_directory = target / ".git"
    return (
        target.is_dir()
        and git_directory.is_dir()
        and not git_directory.is_symlink()
        and git_directory.resolve().is_relative_to(target.resolve())
    )


async def remove_owned_tree(path: Path, *, deadline: float) -> None:
    """Remove a call-owned staging tree without following directory symlinks.

    Traversal is relative to no-follow directory descriptors. A top-level path
    swapped to a symlink is unlinked as a link; its target is never opened.
    """

    flags = os.O_CLOEXEC | os.O_RDONLY | os.O_DIRECTORY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(path, flags)
    except FileNotFoundError:
        return
    except OSError:
        metadata = os.lstat(path)
        if stat.S_ISLNK(metadata.st_mode):
            os.unlink(path)
            return
        raise
    try:
        await _remove_directory_contents(descriptor, deadline=deadline)
    finally:
        os.close(descriptor)
    try:
        os.rmdir(path)
    except FileNotFoundError:
        pass


async def _remove_directory_contents(descriptor: int, *, deadline: float) -> None:
    loop = asyncio.get_running_loop()
    if loop.time() >= deadline:
        raise TimeoutError
    with os.scandir(descriptor) as iterator:
        names = [entry.name for entry in iterator]
    for name in names:
        if loop.time() >= deadline:
            raise TimeoutError
        try:
            metadata = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
        except FileNotFoundError:
            continue
        if stat.S_ISDIR(metadata.st_mode):
            flags = os.O_CLOEXEC | os.O_RDONLY | os.O_DIRECTORY
            if hasattr(os, "O_NOFOLLOW"):
                flags |= os.O_NOFOLLOW
            child_descriptor = os.open(name, flags, dir_fd=descriptor)
            try:
                await _remove_directory_contents(child_descriptor, deadline=deadline)
            finally:
                os.close(child_descriptor)
            try:
                os.rmdir(name, dir_fd=descriptor)
            except FileNotFoundError:
                pass
        else:
            try:
                os.unlink(name, dir_fd=descriptor)
            except FileNotFoundError:
                pass
        await asyncio.sleep(0)


async def _terminate_process_group(process: asyncio.subprocess.Process) -> None:
    if process.returncode is not None:
        await process.wait()
        return

    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        await process.wait()
        return

    wait_task = asyncio.create_task(process.wait())
    try:
        await asyncio.wait_for(asyncio.shield(wait_task), timeout=1)
    except TimeoutError:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        await wait_task
