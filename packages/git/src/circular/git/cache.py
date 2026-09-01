from __future__ import annotations

import asyncio
import fcntl
import math
import os
import shutil
import signal
import tempfile
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Protocol
from uuid import UUID


class _RepositoryPathPolicy(Protocol):
    repository_cache_root: Path

    def repository_cache_path(self, repository_id: UUID) -> Path: ...


class _GitLaunchError(RuntimeError):
    pass


class RepositoryCacheError(RuntimeError):
    """Base class for failures at the local Repository cache seam."""

    def __init__(self, repository_id: UUID, path: Path, message: str) -> None:
        self.repository_id = repository_id
        self.path = path
        super().__init__(message)


class InvalidRepositoryCache(RepositoryCacheError):
    """An existing managed cache target is not a usable Git checkout."""


class RepositoryCloneError(RepositoryCacheError):
    """Git could not create the first local checkout."""

    def __init__(
        self,
        repository_id: UUID,
        path: Path,
        exit_code: int | None,
        *,
        filesystem_error: bool = False,
    ) -> None:
        self.exit_code = exit_code
        if exit_code is not None:
            result = f"git exit code {exit_code}"
        elif filesystem_error:
            result = "filesystem error"
        else:
            result = "git launch error"
        super().__init__(
            repository_id,
            path,
            f"Repository {repository_id} clone failed at {path} ({result})",
        )


class RepositoryFetchError(RepositoryCacheError):
    """Git could not refresh an existing local checkout."""

    def __init__(self, repository_id: UUID, path: Path, exit_code: int | None) -> None:
        self.exit_code = exit_code
        result = f"git exit code {exit_code}" if exit_code is not None else "git launch error"
        super().__init__(
            repository_id,
            path,
            f"Repository {repository_id} refresh failed at {path} ({result})",
        )


class RepositoryLockError(RepositoryCacheError):
    """The per-Repository inter-process cache lock could not be acquired."""

    def __init__(self, repository_id: UUID, path: Path, *, timed_out: bool = False) -> None:
        self.timed_out = timed_out
        outcome = "timed out" if timed_out else "failed"
        super().__init__(
            repository_id,
            path,
            f"Repository {repository_id} cache lock {outcome} at {path}",
        )


class LocalRepositoryCache:
    """Maintain worker-local Repository checkouts behind one operation.

    ``directories`` is the managed execution-directory policy. The structural
    type keeps this package independent from the higher-level runners package.
    """

    def __init__(
        self,
        directories: _RepositoryPathPolicy,
        *,
        lock_timeout_seconds: float = 30.0,
    ) -> None:
        if not math.isfinite(lock_timeout_seconds) or lock_timeout_seconds <= 0:
            raise ValueError("lock_timeout_seconds must be a finite positive number")
        self._directories = directories
        self._lock_timeout_seconds = lock_timeout_seconds

    async def checkout(self, repository_id: UUID, clone_url: str) -> Path:
        """Return a validated checkout refreshed from its configured origin."""

        expected_target = self._directories.repository_cache_root / str(repository_id)
        if expected_target.is_symlink():  # noqa: ASYNC240 - local metadata check
            raise _invalid_cache(repository_id, expected_target)
        target = self._directories.repository_cache_path(repository_id)
        if target != expected_target:
            raise _invalid_cache(repository_id, expected_target)
        try:
            target.parent.mkdir(parents=True, exist_ok=True)
        except OSError as error:
            raise RepositoryLockError(repository_id, target) from error
        async with _repository_lock(
            repository_id, target, timeout_seconds=self._lock_timeout_seconds
        ):
            return await self._checkout_locked(repository_id, target, clone_url)

    async def _checkout_locked(self, repository_id: UUID, target: Path, clone_url: str) -> Path:
        if target.exists():  # noqa: ASYNC240 - worker-owned local filesystem metadata
            try:
                await self._validate(repository_id, target)
            except _GitLaunchError as error:
                raise RepositoryFetchError(repository_id, target, None) from error
            previous_origin = await self._refresh_command(
                repository_id,
                target,
                "-C",
                str(target),
                "remote",
                "get-url",
                "--",
                "origin",
            )
            await self._refresh_command(
                repository_id,
                target,
                "-C",
                str(target),
                "remote",
                "set-url",
                "--",
                "origin",
                clone_url,
            )
            try:
                await self._refresh_command(
                    repository_id,
                    target,
                    "-C",
                    str(target),
                    "fetch",
                    "--prune",
                    "--",
                    "origin",
                )
            except RepositoryFetchError:
                prior_url = os.fsdecode(previous_origin.rstrip(b"\r\n"))
                try:
                    await self._refresh_command(
                        repository_id,
                        target,
                        "-C",
                        str(target),
                        "remote",
                        "set-url",
                        "--",
                        "origin",
                        prior_url,
                    )
                except RepositoryFetchError:
                    pass
                raise

            default_ref = await self._refresh_command(
                repository_id, target, "-C", str(target), "symbolic-ref", "--quiet", "HEAD"
            )
            local_ref = default_ref.decode(errors="replace").strip()
            remote_ref = local_ref.replace("refs/heads/", "refs/remotes/origin/", 1)
            await self._refresh_command(
                repository_id, target, "-C", str(target), "update-ref", local_ref, remote_ref
            )
            return target

        try:
            staging = Path(
                tempfile.mkdtemp(
                    prefix=f".{target.name}.clone-",
                    dir=target.parent,
                )
            )
        except OSError as error:
            raise RepositoryCloneError(
                repository_id, target, None, filesystem_error=True
            ) from error

        published = False
        try:
            try:
                _, returncode = await _run_git(
                    "clone",
                    "--no-checkout",
                    "--",
                    clone_url,
                    str(staging),
                )
            except _GitLaunchError as error:
                raise RepositoryCloneError(repository_id, target, None) from error
            if returncode:
                raise RepositoryCloneError(repository_id, target, returncode)
            try:
                await self._validate(repository_id, staging, reported_path=target)
            except _GitLaunchError as error:
                raise RepositoryCloneError(repository_id, target, None) from error
            try:
                staging.rename(target)  # noqa: ASYNC240 - same local filesystem
            except OSError as error:
                raise RepositoryCloneError(
                    repository_id, target, None, filesystem_error=True
                ) from error
            published = True
        finally:
            if not published:
                shutil.rmtree(staging, ignore_errors=True)
        return target

    async def _refresh_command(self, repository_id: UUID, target: Path, *arguments: str) -> bytes:
        try:
            stdout, returncode = await _run_git(*arguments)
        except _GitLaunchError as error:
            raise RepositoryFetchError(repository_id, target, None) from error
        if returncode:
            raise RepositoryFetchError(repository_id, target, returncode)
        return stdout

    async def _validate(
        self, repository_id: UUID, target: Path, *, reported_path: Path | None = None
    ) -> None:
        valid_layout = _has_valid_git_layout(target)
        if valid_layout:
            inside, returncode = await _run_git(
                "-C", str(target), "rev-parse", "--is-inside-work-tree"
            )
            valid_layout = returncode == 0 and inside.strip() == b"true"
        if not valid_layout:
            error_path = reported_path or target
            raise _invalid_cache(repository_id, error_path)


def _invalid_cache(repository_id: UUID, path: Path) -> InvalidRepositoryCache:
    return InvalidRepositoryCache(
        repository_id,
        path,
        f"Repository {repository_id} cache at {path} is not a managed Git checkout",
    )


def _has_valid_git_layout(target: Path) -> bool:
    git_directory = target / ".git"
    return (
        target.is_dir()
        and git_directory.is_dir()
        and not git_directory.is_symlink()
        and git_directory.resolve().is_relative_to(target.resolve())
    )


@asynccontextmanager
async def _repository_lock(
    repository_id: UUID, target: Path, *, timeout_seconds: float
) -> AsyncIterator[None]:
    lock_path = target.with_name(f".{target.name}.lock")
    flags = os.O_CLOEXEC | os.O_CREAT | os.O_RDWR
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(lock_path, flags, 0o600)
    except OSError as error:
        raise RepositoryLockError(repository_id, target) from error
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
                    raise RepositoryLockError(repository_id, target, timed_out=True) from None
                await asyncio.sleep(min(0.01, remaining))
            except OSError as error:
                raise RepositoryLockError(repository_id, target) from error
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
            raise RepositoryLockError(repository_id, target) from release_error


async def _run_git(*arguments: str) -> tuple[bytes, int]:
    environment = os.environ.copy()
    environment["GIT_TERMINAL_PROMPT"] = "0"
    environment["GCM_INTERACTIVE"] = "Never"
    try:
        process = await asyncio.create_subprocess_exec(
            "git",
            "-c",
            "protocol.allow=never",
            "-c",
            "protocol.file.allow=always",
            "-c",
            "protocol.https.allow=always",
            *arguments,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=environment,
            start_new_session=True,
        )
    except OSError as error:
        raise _GitLaunchError from error
    try:
        stdout, _ = await process.communicate()
    except asyncio.CancelledError:
        cleanup = asyncio.create_task(_terminate_git_process(process))
        while not cleanup.done():
            try:
                await asyncio.shield(cleanup)
            except asyncio.CancelledError:
                continue
        await cleanup
        raise
    return stdout, process.returncode or 0


async def _terminate_git_process(process: asyncio.subprocess.Process) -> None:
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
