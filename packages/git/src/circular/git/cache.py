from __future__ import annotations

import asyncio
import math
import os
import tempfile
from pathlib import Path
from typing import Protocol, runtime_checkable
from uuid import UUID

from circular.git._local import (
    GitLaunchError,
    has_valid_primary_checkout,
    remove_owned_tree,
    repository_lock,
    run_git,
)

_CLONE_CLEANUP_TIMEOUT_SECONDS = 5.0


class _RepositoryPathPolicy(Protocol):
    repository_cache_root: Path

    def repository_cache_path(self, repository_id: UUID) -> Path: ...


@runtime_checkable
class RepositoryCache(Protocol):
    """Checkout seam consumed by Run workspace orchestration."""

    async def checkout(self, repository_id: UUID, clone_url: str) -> Path: ...


class RepositoryCacheError(RuntimeError):
    """Base class for failures at the local Repository cache seam."""

    def __init__(self, repository_id: UUID, path: Path, message: str) -> None:
        self.repository_id = repository_id
        self.path = path
        self.cleanup_error: RepositoryCloneCleanupError | None = None
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


class RepositoryCloneCleanupError(RepositoryCacheError):
    """A failed first checkout could not remove its private staging directory."""

    def __init__(self, repository_id: UUID, path: Path, *, timed_out: bool = False) -> None:
        self.timed_out = timed_out
        outcome = "timed out" if timed_out else "failed"
        super().__init__(
            repository_id,
            path,
            f"Repository {repository_id} clone staging cleanup {outcome} at {path}",
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
        async with repository_lock(
            target,
            timeout_seconds=self._lock_timeout_seconds,
            error_factory=lambda timed_out: RepositoryLockError(
                repository_id, target, timed_out=timed_out
            ),
        ):
            return await self._checkout_locked(repository_id, target, clone_url)

    async def _checkout_locked(self, repository_id: UUID, target: Path, clone_url: str) -> Path:
        if target.exists():  # noqa: ASYNC240 - worker-owned local filesystem metadata
            try:
                await self._validate(repository_id, target)
            except GitLaunchError as error:
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
        primary_error: BaseException | None = None
        try:
            try:
                _, returncode = await run_git(
                    "clone",
                    "--no-checkout",
                    "--",
                    clone_url,
                    str(staging),
                )
            except GitLaunchError as error:
                raise RepositoryCloneError(repository_id, target, None) from error
            if returncode:
                raise RepositoryCloneError(repository_id, target, returncode)
            try:
                await self._validate(repository_id, staging, reported_path=target)
            except GitLaunchError as error:
                raise RepositoryCloneError(repository_id, target, None) from error
            try:
                staging.rename(target)  # noqa: ASYNC240 - same local filesystem
            except OSError as error:
                raise RepositoryCloneError(
                    repository_id, target, None, filesystem_error=True
                ) from error
            published = True
        except BaseException as error:
            primary_error = error
            raise
        finally:
            if not published:
                try:
                    await _remove_clone_staging(repository_id, target, staging)
                except RepositoryCloneCleanupError as cleanup_error:
                    if primary_error is None:
                        raise
                    primary_error.add_note(str(cleanup_error))
                    if isinstance(primary_error, RepositoryCacheError):
                        primary_error.cleanup_error = cleanup_error
        return target

    async def _refresh_command(self, repository_id: UUID, target: Path, *arguments: str) -> bytes:
        try:
            stdout, returncode = await run_git(*arguments)
        except GitLaunchError as error:
            raise RepositoryFetchError(repository_id, target, None) from error
        if returncode:
            raise RepositoryFetchError(repository_id, target, returncode)
        return stdout

    async def _validate(
        self, repository_id: UUID, target: Path, *, reported_path: Path | None = None
    ) -> None:
        valid_layout = has_valid_primary_checkout(target)
        if valid_layout:
            inside, returncode = await run_git(
                "-C", str(target), "rev-parse", "--is-inside-work-tree"
            )
            valid_layout = returncode == 0 and inside.strip() == b"true"
        if valid_layout:
            head, returncode = await run_git(
                "-C", str(target), "rev-parse", "--verify", "HEAD^{commit}"
            )
            valid_layout = returncode == 0 and bool(head.strip())
        if not valid_layout:
            error_path = reported_path or target
            raise _invalid_cache(repository_id, error_path)


def _invalid_cache(repository_id: UUID, path: Path) -> InvalidRepositoryCache:
    return InvalidRepositoryCache(
        repository_id,
        path,
        f"Repository {repository_id} cache at {path} is not a managed Git checkout",
    )


async def _remove_clone_staging(repository_id: UUID, target: Path, staging: Path) -> None:
    expected_prefix = f".{target.name}.clone-"
    if staging.parent != target.parent or not staging.name.startswith(expected_prefix):
        raise RepositoryCloneCleanupError(repository_id, target)

    loop = asyncio.get_running_loop()
    deadline = loop.time() + _CLONE_CLEANUP_TIMEOUT_SECONDS
    try:
        await remove_owned_tree(staging, deadline=deadline)
    except TimeoutError as error:
        raise RepositoryCloneCleanupError(repository_id, target, timed_out=True) from error
    except OSError as error:
        raise RepositoryCloneCleanupError(repository_id, target) from error
