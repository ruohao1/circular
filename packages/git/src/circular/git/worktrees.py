from __future__ import annotations

import asyncio
import math
import os
import re
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol
from uuid import UUID

from circular.git._local import (
    GitLaunchError,
    has_valid_primary_checkout,
    remove_owned_tree,
    repository_lock,
    run_git,
)

_CLEANUP_TIMEOUT_SECONDS = 5.0
_COMMIT_PATTERN = re.compile(rb"(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})")


@dataclass(frozen=True, slots=True)
class ProvisionedWorktree:
    run_id: UUID
    repository_path: Path
    path: Path
    branch: str


class WorktreeManager(Protocol):
    async def provision(
        self, run_id: UUID, repository_path: Path, base_ref: str
    ) -> ProvisionedWorktree: ...

    async def release(self, worktree: ProvisionedWorktree) -> None: ...


class _RunPaths(Protocol):
    worktree: Path


class _ExecutionPathPolicy(Protocol):
    repository_cache_root: Path
    worktree_root: Path

    def repository_cache_path(self, repository_id: UUID) -> Path: ...

    def run_paths(self, run_id: UUID) -> _RunPaths: ...


class WorktreeError(RuntimeError):
    """Base class for failures at the local worktree seam."""

    def __init__(self, run_id: UUID, path: Path, message: str) -> None:
        self.run_id = run_id
        self.path = path
        self.cleanup_error: WorktreeCleanupError | None = None
        super().__init__(message)


class InvalidWorktreeRepository(WorktreeError):
    """The requested source is not its managed UUID Repository checkout."""

    def __init__(self, run_id: UUID, path: Path, repository_path: Path) -> None:
        self.repository_path = repository_path
        super().__init__(
            run_id,
            path,
            f"Run {run_id} source is not a managed Repository checkout for {path}",
        )


class InvalidWorktreePath(WorktreeError):
    """The execution-directory policy did not return the Run-owned target."""


class WorktreeConflictError(WorktreeError):
    """The deterministic Run target or branch is already allocated."""


class WorktreeRefError(WorktreeError):
    """The requested base ref could not be resolved to a commit."""

    def __init__(self, run_id: UUID, path: Path, exit_code: int | None) -> None:
        self.exit_code = exit_code
        result = f"git exit code {exit_code}" if exit_code is not None else "invalid or unavailable"
        super().__init__(run_id, path, f"Run {run_id} base ref resolution failed ({result})")


class WorktreeProvisionError(WorktreeError):
    """Git could not create and publish the Run worktree."""

    def __init__(
        self,
        run_id: UUID,
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
        super().__init__(run_id, path, f"Run {run_id} worktree provisioning failed ({result})")


class WorktreeCleanupError(WorktreeError):
    """A failed provision could not fully roll back its private allocation."""


class WorktreeLockError(WorktreeError):
    """A local Run or Repository mutation lock could not be acquired."""

    def __init__(self, run_id: UUID, path: Path, *, timed_out: bool = False) -> None:
        self.timed_out = timed_out
        outcome = "timed out" if timed_out else "failed"
        super().__init__(run_id, path, f"Run {run_id} worktree lock {outcome} at {path}")


class WorktreeReleaseError(WorktreeError):
    """The basic clean-worktree release operation failed."""

    def __init__(self, run_id: UUID, path: Path, exit_code: int | None = None) -> None:
        self.exit_code = exit_code
        suffix = f" (git exit code {exit_code})" if exit_code is not None else ""
        super().__init__(run_id, path, f"Run {run_id} worktree release failed at {path}{suffix}")


@dataclass(frozen=True, slots=True)
class _LinkedWorktree:
    repository_path: Path
    branch_ref: str
    commit: str


class _InvalidLinkedWorktree(RuntimeError):
    pass


class LocalWorktreeManager:
    """Provision one linked Git worktree at the deterministic Run-owned path.

    ``directories`` is accepted structurally so this package stays independent
    from the higher-level runners package. Repository cache refresh and worktree
    metadata changes share the same cross-process Repository lock.

    ``release`` intentionally supports only a present, valid, clean linked
    worktree and preserves its Run branch. Idempotence, stale metadata recovery,
    and interrupted-cleanup reconciliation belong to ISQ-167.
    """

    def __init__(
        self,
        directories: _ExecutionPathPolicy,
        *,
        lock_timeout_seconds: float = 30.0,
    ) -> None:
        if not math.isfinite(lock_timeout_seconds) or lock_timeout_seconds <= 0:
            raise ValueError("lock_timeout_seconds must be a finite positive number")
        self._directories = directories
        self._lock_timeout_seconds = lock_timeout_seconds

    async def provision(
        self, run_id: UUID, repository_path: Path, base_ref: str
    ) -> ProvisionedWorktree:
        target = self._run_target(run_id)
        self._ensure_worktree_root(run_id, target)
        repository_id = self._repository_id(run_id, target, repository_path)
        branch = f"circular/run/{run_id}"

        if _path_exists(target):
            raise _conflict(run_id, target)

        async with self._lock(run_id, target):
            if _path_exists(target):
                raise _conflict(run_id, target)
            async with self._lock(run_id, repository_path):
                await self._validate_repository(run_id, target, repository_id, repository_path)
                if _path_exists(target):
                    raise _conflict(run_id, target)
                await self._require_absent_branch(run_id, target, repository_path, branch)
                commit = await self._resolve_ref(run_id, target, repository_path, base_ref)
                return await self._provision_locked(run_id, target, repository_path, branch, commit)

    async def release(self, worktree: ProvisionedWorktree) -> None:
        try:
            target = self._run_target(worktree.run_id)
        except WorktreeError as error:
            raw_target = Path(self._directories.worktree_root) / str(worktree.run_id)
            raise WorktreeReleaseError(worktree.run_id, raw_target) from error
        expected_branch = f"circular/run/{worktree.run_id}"
        if worktree.path != target or worktree.branch != expected_branch or not target.exists():
            raise WorktreeReleaseError(worktree.run_id, target)
        repository_id = self._release_repository_id(worktree, target)

        async with self._lock(worktree.run_id, target):
            async with self._lock(worktree.run_id, worktree.repository_path):
                await self._validate_repository(
                    worktree.run_id,
                    target,
                    repository_id,
                    worktree.repository_path,
                )
                try:
                    linked = await _inspect_linked_worktree(target)
                except (GitLaunchError, _InvalidLinkedWorktree) as error:
                    raise WorktreeReleaseError(worktree.run_id, target) from error
                if (
                    linked.repository_path != worktree.repository_path
                    or linked.branch_ref != f"refs/heads/{expected_branch}"
                ):
                    raise WorktreeReleaseError(worktree.run_id, target)
                try:
                    _, returncode = await run_git(
                        "-C",
                        str(worktree.repository_path),
                        "worktree",
                        "remove",
                        str(target),
                    )
                except GitLaunchError as error:
                    raise WorktreeReleaseError(worktree.run_id, target) from error
                if returncode:
                    raise WorktreeReleaseError(worktree.run_id, target, returncode)

    def _run_target(self, run_id: UUID) -> Path:
        if not isinstance(run_id, UUID):
            raise TypeError("worktree paths require a UUID Run identifier")
        root = Path(self._directories.worktree_root)
        raw_target = root / str(run_id)
        if raw_target.is_symlink():
            raise _conflict(run_id, raw_target)
        try:
            target = Path(self._directories.run_paths(run_id).worktree)
        except (TypeError, ValueError) as error:
            raise InvalidWorktreePath(
                run_id,
                raw_target,
                f"Run {run_id} worktree path is outside its managed root",
            ) from error
        if target != raw_target or target.parent != root or target.name != str(run_id):
            raise InvalidWorktreePath(
                run_id,
                raw_target,
                f"Run {run_id} worktree path is not its managed UUID target",
            )
        return target

    def _ensure_worktree_root(self, run_id: UUID, target: Path) -> None:
        root = target.parent
        try:
            root.mkdir(parents=True, exist_ok=True)
        except OSError as error:
            raise WorktreeProvisionError(run_id, target, None, filesystem_error=True) from error
        if root.is_symlink() or not root.is_dir() or root.resolve() != root:
            raise InvalidWorktreePath(
                run_id,
                target,
                f"Run {run_id} worktree root is not a canonical managed directory",
            )

    def _repository_id(self, run_id: UUID, target: Path, repository_path: Path) -> UUID:
        repository_path = Path(repository_path)
        root = Path(self._directories.repository_cache_root)
        try:
            repository_id = UUID(repository_path.name)
        except ValueError as error:
            raise InvalidWorktreeRepository(run_id, target, repository_path) from error
        expected = root / str(repository_id)
        try:
            policy_path = Path(self._directories.repository_cache_path(repository_id))
        except (TypeError, ValueError) as error:
            raise InvalidWorktreeRepository(run_id, target, repository_path) from error
        if (
            repository_path.name != str(repository_id)
            or repository_path.parent != root
            or repository_path != expected
            or policy_path != expected
            or repository_path.is_symlink()
            or repository_path.resolve(strict=False) != repository_path
        ):
            raise InvalidWorktreeRepository(run_id, target, repository_path)
        return repository_id

    def _release_repository_id(self, worktree: ProvisionedWorktree, target: Path) -> UUID:
        try:
            return self._repository_id(worktree.run_id, target, worktree.repository_path)
        except InvalidWorktreeRepository as error:
            raise WorktreeReleaseError(worktree.run_id, target) from error

    async def _validate_repository(
        self,
        run_id: UUID,
        target: Path,
        repository_id: UUID,
        repository_path: Path,
    ) -> None:
        if not has_valid_primary_checkout(repository_path):
            raise InvalidWorktreeRepository(run_id, target, repository_path)
        try:
            inside, returncode = await run_git(
                "-C", str(repository_path), "rev-parse", "--is-inside-work-tree"
            )
            common, common_returncode = await run_git(
                "-C",
                str(repository_path),
                "rev-parse",
                "--path-format=absolute",
                "--git-common-dir",
            )
        except GitLaunchError as error:
            raise InvalidWorktreeRepository(run_id, target, repository_path) from error
        expected_common = repository_path / ".git"
        actual_common = Path(os.fsdecode(common.rstrip(b"\r\n")))
        if (
            returncode
            or inside.strip() != b"true"
            or common_returncode
            or actual_common != expected_common
            or UUID(repository_path.name) != repository_id
        ):
            raise InvalidWorktreeRepository(run_id, target, repository_path)

    async def _require_absent_branch(
        self,
        run_id: UUID,
        target: Path,
        repository_path: Path,
        branch: str,
    ) -> None:
        try:
            _, returncode = await run_git(
                "-C",
                str(repository_path),
                "show-ref",
                "--verify",
                "--quiet",
                f"refs/heads/{branch}",
            )
        except GitLaunchError as error:
            raise WorktreeProvisionError(run_id, target, None) from error
        if returncode == 0:
            raise _conflict(run_id, target)
        if returncode != 1:
            raise WorktreeProvisionError(run_id, target, returncode)

    async def _resolve_ref(
        self,
        run_id: UUID,
        target: Path,
        repository_path: Path,
        base_ref: str,
    ) -> str:
        try:
            encoded_ref = base_ref.encode("utf-8")
        except (AttributeError, UnicodeEncodeError) as error:
            raise WorktreeRefError(run_id, target, None) from error
        if not encoded_ref or any(character in encoded_ref for character in (0, 10, 13)):
            raise WorktreeRefError(run_id, target, None)
        try:
            stdout, returncode = await run_git(
                "-C",
                str(repository_path),
                "rev-parse",
                "--verify",
                "--end-of-options",
                f"{base_ref}^{{commit}}",
            )
        except GitLaunchError as error:
            raise WorktreeRefError(run_id, target, None) from error
        commit = stdout.strip()
        if returncode or not _COMMIT_PATTERN.fullmatch(commit):
            raise WorktreeRefError(run_id, target, returncode)
        return commit.decode("ascii").lower()

    async def _provision_locked(
        self,
        run_id: UUID,
        target: Path,
        repository_path: Path,
        branch: str,
        commit: str,
    ) -> ProvisionedWorktree:
        try:
            staging = Path(tempfile.mkdtemp(prefix=f".{run_id}.worktree-", dir=target.parent))
        except OSError as error:
            raise WorktreeProvisionError(run_id, target, None, filesystem_error=True) from error

        primary_error: BaseException | None = None
        published = False
        branch_may_be_owned = False
        try:
            branch_may_be_owned = True
            await self._require_git_success(
                run_id,
                target,
                "-C",
                str(repository_path),
                "worktree",
                "add",
                "-b",
                branch,
                str(staging),
                commit,
            )
            await self._require_git_success(
                run_id,
                target,
                "-C",
                str(repository_path),
                "worktree",
                "move",
                str(staging),
                str(target),
            )
            linked = await _inspect_linked_worktree(target)
            if (
                linked.repository_path != repository_path
                or linked.branch_ref != f"refs/heads/{branch}"
                or linked.commit != commit
                or not _is_valid_linked_layout(target)
            ):
                raise WorktreeProvisionError(run_id, target, None)
            published = True
            return ProvisionedWorktree(
                run_id=run_id,
                repository_path=repository_path,
                path=target,
                branch=branch,
            )
        except _InvalidLinkedWorktree as error:
            primary_error = WorktreeProvisionError(run_id, target, None)
            raise primary_error from error
        except BaseException as error:
            primary_error = error
            raise
        finally:
            if not published:
                cleanup_error = await _complete_cleanup(
                    self._cleanup_failed_provision(
                        run_id,
                        target,
                        repository_path,
                        staging,
                        branch,
                        commit,
                        branch_may_be_owned=branch_may_be_owned,
                    )
                )
                if cleanup_error is not None and primary_error is not None:
                    primary_error.add_note(str(cleanup_error))
                    if isinstance(primary_error, WorktreeError):
                        primary_error.cleanup_error = cleanup_error

    async def _require_git_success(self, run_id: UUID, target: Path, *arguments: str) -> None:
        try:
            _, returncode = await run_git(*arguments)
        except GitLaunchError as error:
            raise WorktreeProvisionError(run_id, target, None) from error
        if returncode:
            raise WorktreeProvisionError(run_id, target, returncode)

    async def _cleanup_failed_provision(
        self,
        run_id: UUID,
        target: Path,
        repository_path: Path,
        staging: Path,
        branch: str,
        commit: str,
        *,
        branch_may_be_owned: bool,
    ) -> None:
        try:
            if await _is_owned_linked_worktree(target, repository_path, branch):
                await _remove_failed_worktree(repository_path, target)
            await _remove_private_staging(repository_path, target, staging)
            if branch_may_be_owned:
                _, returncode = await run_git(
                    "-C",
                    str(repository_path),
                    "update-ref",
                    "-d",
                    f"refs/heads/{branch}",
                    commit,
                )
                if returncode:
                    raise WorktreeCleanupError(
                        run_id,
                        target,
                        f"Run {run_id} failed worktree branch rollback failed",
                    )
        except WorktreeCleanupError:
            raise
        except (GitLaunchError, OSError, TimeoutError) as error:
            raise WorktreeCleanupError(
                run_id,
                target,
                f"Run {run_id} failed worktree cleanup failed at {target}",
            ) from error

    def _lock(self, run_id: UUID, path: Path):  # type: ignore[no-untyped-def]
        return repository_lock(
            path,
            timeout_seconds=self._lock_timeout_seconds,
            error_factory=lambda timed_out: WorktreeLockError(run_id, path, timed_out=timed_out),
        )


def _conflict(run_id: UUID, target: Path) -> WorktreeConflictError:
    return WorktreeConflictError(
        run_id,
        target,
        f"Run {run_id} worktree target or branch is already allocated at {target}",
    )


def _path_exists(path: Path) -> bool:
    return os.path.lexists(path)


def _is_valid_linked_layout(path: Path) -> bool:
    git_file = path / ".git"
    return path.is_dir() and git_file.is_file() and not git_file.is_symlink()


async def _inspect_linked_worktree(path: Path) -> _LinkedWorktree:
    if not _is_valid_linked_layout(path):
        raise _InvalidLinkedWorktree
    common, common_code = await run_git(
        "-C", str(path), "rev-parse", "--path-format=absolute", "--git-common-dir"
    )
    top, top_code = await run_git(
        "-C", str(path), "rev-parse", "--path-format=absolute", "--show-toplevel"
    )
    branch, branch_code = await run_git("-C", str(path), "symbolic-ref", "--quiet", "HEAD")
    commit, commit_code = await run_git("-C", str(path), "rev-parse", "--verify", "HEAD^{commit}")
    common_path = Path(os.fsdecode(common.rstrip(b"\r\n")))
    top_path = Path(os.fsdecode(top.rstrip(b"\r\n")))
    commit_value = commit.strip()
    if (
        common_code
        or top_code
        or branch_code
        or commit_code
        or top_path != path
        or common_path.name != ".git"
        or not _COMMIT_PATTERN.fullmatch(commit_value)
    ):
        raise _InvalidLinkedWorktree
    return _LinkedWorktree(
        repository_path=common_path.parent,
        branch_ref=branch.decode("utf-8", errors="strict").strip(),
        commit=commit_value.decode("ascii").lower(),
    )


async def _is_owned_linked_worktree(path: Path, repository_path: Path, branch: str) -> bool:
    if not path.exists() or path.is_symlink():  # noqa: ASYNC240 - local metadata
        return False
    try:
        linked = await _inspect_linked_worktree(path)
    except _InvalidLinkedWorktree:
        return False
    return linked.repository_path == repository_path and linked.branch_ref == f"refs/heads/{branch}"


async def _remove_failed_worktree(repository_path: Path, path: Path) -> None:
    _, returncode = await run_git(
        "-C", str(repository_path), "worktree", "remove", "--force", str(path)
    )
    if returncode:
        raise OSError("failed to remove call-owned linked worktree")


async def _remove_private_staging(repository_path: Path, target: Path, staging: Path) -> None:
    expected_prefix = f".{target.name}.worktree-"
    if staging.parent != target.parent or not staging.name.startswith(expected_prefix):
        raise OSError("invalid worktree staging path")
    if not _path_exists(staging):
        return

    _, returncode = await run_git(
        "-C", str(repository_path), "worktree", "remove", "--force", str(staging)
    )
    if not _path_exists(staging):
        return
    if returncode:
        deadline = asyncio.get_running_loop().time() + _CLEANUP_TIMEOUT_SECONDS
        await remove_owned_tree(staging, deadline=deadline)
        _, prune_returncode = await run_git(
            "-C", str(repository_path), "worktree", "prune", "--expire", "now"
        )
        if prune_returncode:
            raise OSError("failed to prune call-owned worktree metadata")
    if _path_exists(staging):
        raise OSError("call-owned worktree staging path survived cleanup")


async def _complete_cleanup(cleanup_coroutine) -> WorktreeCleanupError | None:  # type: ignore[no-untyped-def]
    cleanup = asyncio.create_task(cleanup_coroutine)
    while not cleanup.done():
        try:
            await asyncio.shield(cleanup)
        except asyncio.CancelledError:
            continue
        except WorktreeCleanupError:
            break
    try:
        cleanup.result()
    except WorktreeCleanupError as error:
        return error
    return None
