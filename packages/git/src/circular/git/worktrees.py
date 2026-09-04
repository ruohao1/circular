from __future__ import annotations

import asyncio
import math
import os
import re
import stat
import tempfile
from collections.abc import Coroutine
from contextlib import AbstractAsyncContextManager
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol
from uuid import UUID, uuid4

from circular.git._local import (
    GitLaunchError,
    has_valid_primary_checkout,
    remove_owned_tree,
    repository_lock,
    run_git,
)

_CLEANUP_TIMEOUT_SECONDS = 5.0
_COMMIT_PATTERN = re.compile(rb"(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})")
_OWNERSHIP_MARKER_MAGIC = b"circular-worktree-owner\0\x01"


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
    """A Run-owned worktree could not be safely released or reconciled."""

    def __init__(self, run_id: UUID, path: Path, exit_code: int | None = None) -> None:
        self.exit_code = exit_code
        suffix = f" (git exit code {exit_code})" if exit_code is not None else ""
        super().__init__(run_id, path, f"Run {run_id} worktree release failed at {path}{suffix}")


@dataclass(frozen=True, slots=True)
class _LinkedWorktree:
    repository_path: Path
    branch_ref: bytes
    commit: str


@dataclass(frozen=True, slots=True)
class _RegisteredWorktree:
    path: bytes
    branch_ref: bytes | None
    commit: bytes
    prunable: bool


@dataclass(frozen=True, slots=True)
class _OwnershipMarker:
    marker_identity: tuple[int, int]
    target_identity: tuple[int, int]


class _InvalidLinkedWorktree(RuntimeError):
    pass


class _InvalidOwnershipMarker(RuntimeError):
    pass


class LocalWorktreeManager:
    """Provision one linked Git worktree at the deterministic Run-owned path.

    ``directories`` is accepted structurally so this package stays independent
    from the higher-level runners package. Repository cache refresh and worktree
    metadata changes share the same cross-process Repository lock.

    ``release`` preserves the Run branch, refuses dirty live worktrees, and
    reconciles interrupted directory or Git-metadata cleanup using only
    ownership evidence rooted in these managed paths.
    """

    def __init__(
        self,
        directories: _ExecutionPathPolicy,
        *,
        lock_timeout_seconds: float = 30.0,
        owner: tuple[int, int] | None = None,
    ) -> None:
        if not math.isfinite(lock_timeout_seconds) or lock_timeout_seconds <= 0:
            raise ValueError("lock_timeout_seconds must be a finite positive number")
        self._directories = directories
        self._lock_timeout_seconds = lock_timeout_seconds
        self._owner = owner

    async def provision(
        self, run_id: UUID, repository_path: Path, base_ref: str
    ) -> ProvisionedWorktree:
        target = self._run_target(run_id)
        self._ensure_worktree_root(run_id, target)
        repository_id = self._repository_id(run_id, target, repository_path)
        branch = f"circular/run/{run_id}"
        ownership_marker = _ownership_marker_path(target, run_id)

        if _path_exists(target) or _path_exists(ownership_marker):
            raise _conflict(run_id, target)

        async with self._lock(run_id, target):
            if _path_exists(target) or _path_exists(ownership_marker):
                raise _conflict(run_id, target)
            async with self._lock(run_id, repository_path):
                await self._validate_repository(run_id, target, repository_id, repository_path)
                if _path_exists(target) or _path_exists(ownership_marker):
                    raise _conflict(run_id, target)
                await self._require_absent_branch(run_id, target, repository_path, branch)
                commit = await self._resolve_ref(run_id, target, repository_path, base_ref)
                worktree = await self._provision_locked(
                    run_id,
                    target,
                    repository_id,
                    repository_path,
                    branch,
                    commit,
                )
                if self._owner is not None:
                    uid, gid = self._owner
                    for directory, subdirectories, files in os.walk(target, followlinks=False):
                        for name in [*subdirectories, *files]:
                            os.chown(Path(directory) / name, uid, gid, follow_symlinks=False)
                    os.chown(target, uid, gid, follow_symlinks=False)
                return worktree

    async def release(
        self, worktree: ProvisionedWorktree, *, discard_changes: bool = False
    ) -> None:
        try:
            target = self._run_target(worktree.run_id)
        except WorktreeError as error:
            raw_target = Path(self._directories.worktree_root) / str(worktree.run_id)
            raise WorktreeReleaseError(worktree.run_id, raw_target) from error
        expected_branch = f"circular/run/{worktree.run_id}"
        if worktree.path != target or worktree.branch != expected_branch:
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
                registrations = await self._release_staging(worktree, target, repository_id)
                marker_present = self._ownership_marker_present(
                    worktree.run_id, target, repository_id
                )
                if not _path_exists(target):
                    await self._remove_stale_registration(
                        worktree.run_id,
                        target,
                        worktree.repository_path,
                        expected_branch,
                        marker_present=marker_present,
                        registrations=registrations,
                    )
                    if marker_present:
                        self._remove_ownership_marker(worktree.run_id, target, repository_id)
                    return
                try:
                    linked = await _inspect_linked_worktree(target)
                except (GitLaunchError, _InvalidLinkedWorktree):
                    try:
                        await self._remove_stale_directory(
                            worktree.run_id,
                            target,
                            repository_id,
                            worktree.repository_path,
                            expected_branch,
                            marker_present=marker_present,
                        )
                    except WorktreeReleaseError:
                        raise
                    except (
                        GitLaunchError,
                        OSError,
                        TimeoutError,
                        _InvalidLinkedWorktree,
                    ) as cleanup_error:
                        raise WorktreeReleaseError(worktree.run_id, target) from cleanup_error
                    return
                if (
                    linked.repository_path != worktree.repository_path
                    or linked.branch_ref != os.fsencode(f"refs/heads/{expected_branch}")
                ):
                    raise WorktreeReleaseError(worktree.run_id, target)
                if not marker_present:
                    self._create_ownership_marker(worktree.run_id, target, repository_id)
                try:
                    preserved_changes = await _worktree_has_preserved_changes(target)
                except (GitLaunchError, _InvalidLinkedWorktree) as error:
                    raise WorktreeReleaseError(worktree.run_id, target) from error
                if preserved_changes and not discard_changes:
                    raise WorktreeReleaseError(worktree.run_id, target)
                try:
                    _, returncode = await run_git(
                        "-C",
                        str(worktree.repository_path),
                        "worktree",
                        "remove",
                        *(("--force",) if discard_changes else ()),
                        str(target),
                    )
                except GitLaunchError as error:
                    raise WorktreeReleaseError(worktree.run_id, target) from error
                if returncode:
                    raise WorktreeReleaseError(worktree.run_id, target, returncode)
                if _path_exists(target):
                    raise WorktreeReleaseError(worktree.run_id, target)
                self._remove_ownership_marker(worktree.run_id, target, repository_id)

    async def _release_staging(
        self, worktree: ProvisionedWorktree, target: Path, repository_id: UUID
    ) -> tuple[_RegisteredWorktree, ...]:
        """Reconcile private allocations under the same Run/Repository locks.

        A durable receipt proves new partial allocations. Older allocations need
        the linked Git layout and exact registered Run branch as ownership proof.
        A matching filename alone never authorizes deleting a directory.
        """
        try:
            registrations = await _list_registered_worktrees(worktree.repository_path)
            prefix = f".{worktree.run_id}.worktree-"
            candidates = {
                entry.with_name(entry.name.removesuffix(".owner"))
                for entry in target.parent.iterdir()
                if entry.name.startswith(prefix)
            }
            candidates.update(
                Path(os.fsdecode(entry.path))
                for entry in registrations
                if Path(os.fsdecode(entry.path)).parent == target.parent
                and Path(os.fsdecode(entry.path)).name.startswith(prefix)
            )
            for staging in sorted(candidates):
                marker = self._ownership_marker_present(worktree.run_id, staging, repository_id)
                matches = [entry for entry in registrations if entry.path == os.fsencode(staging)]
                if matches and (
                    len(matches) != 1
                    or matches[0].branch_ref != os.fsencode(f"refs/heads/{worktree.branch}")
                ):
                    raise WorktreeReleaseError(worktree.run_id, staging)
                if _path_exists(staging):
                    if not marker and not (
                        matches
                        and await _is_owned_linked_worktree(
                            staging, worktree.repository_path, worktree.branch
                        )
                    ):
                        raise WorktreeReleaseError(worktree.run_id, staging)
                    await _settle_release_cleanup(
                        _remove_private_staging(
                            worktree.repository_path, target, staging, worktree.branch
                        )
                    )
                else:
                    await self._remove_stale_registration(
                        worktree.run_id,
                        staging,
                        worktree.repository_path,
                        worktree.branch,
                        marker_present=False,
                    )
                if marker:
                    self._remove_ownership_marker(worktree.run_id, staging, repository_id)
            if candidates:
                descriptor = _open_worktree_root(target)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
            return registrations
        except (GitLaunchError, OSError, TimeoutError, _InvalidLinkedWorktree) as error:
            raise WorktreeReleaseError(worktree.run_id, target) from error

    async def _remove_stale_registration(
        self,
        run_id: UUID,
        target: Path,
        repository_path: Path,
        expected_branch: str,
        *,
        marker_present: bool,
        registrations: tuple[_RegisteredWorktree, ...] | None = None,
    ) -> None:
        try:
            if registrations is None:
                registrations = await _list_registered_worktrees(repository_path)
        except (GitLaunchError, _InvalidLinkedWorktree) as error:
            raise WorktreeReleaseError(run_id, target) from error
        matches = [entry for entry in registrations if entry.path == os.fsencode(target)]
        if not matches and not marker_present:
            return
        try:
            branch_commit = await _resolve_branch_commit(repository_path, expected_branch)
        except (GitLaunchError, _InvalidLinkedWorktree) as error:
            raise WorktreeReleaseError(run_id, target) from error
        if not matches:
            return
        if (
            len(matches) != 1
            or matches[0].branch_ref != os.fsencode(f"refs/heads/{expected_branch}")
            or matches[0].commit != branch_commit
            or not matches[0].prunable
        ):
            raise WorktreeReleaseError(run_id, target)
        try:
            _, returncode = await run_git(
                "-C", str(repository_path), "worktree", "remove", str(target)
            )
        except GitLaunchError as error:
            raise WorktreeReleaseError(run_id, target) from error
        if returncode:
            raise WorktreeReleaseError(run_id, target, returncode)

    async def _remove_stale_directory(
        self,
        run_id: UUID,
        target: Path,
        repository_id: UUID,
        repository_path: Path,
        expected_branch: str,
        *,
        marker_present: bool,
    ) -> None:
        registrations = await _list_registered_worktrees(repository_path)
        if any(entry.path == os.fsencode(target) for entry in registrations):
            raise WorktreeReleaseError(run_id, target)
        git_entry = target / ".git"
        if _path_exists(git_entry):
            if not _has_stale_git_backpointer(target, repository_path):
                raise WorktreeReleaseError(run_id, target)
            if not marker_present:
                self._create_ownership_marker(run_id, target, repository_id)
        elif not marker_present:
            raise WorktreeReleaseError(run_id, target)
        try:
            await _resolve_branch_commit(repository_path, expected_branch)
        except (GitLaunchError, _InvalidLinkedWorktree) as error:
            raise WorktreeReleaseError(run_id, target) from error
        if not self._ownership_marker_present(run_id, target, repository_id):
            raise WorktreeReleaseError(run_id, target)

        await _settle_release_cleanup(
            self._remove_stale_directory_contents(run_id, target, repository_id)
        )

    async def _remove_stale_directory_contents(
        self,
        run_id: UUID,
        target: Path,
        repository_id: UUID,
    ) -> None:
        deadline = asyncio.get_running_loop().time() + _CLEANUP_TIMEOUT_SECONDS
        await remove_owned_tree(target, deadline=deadline)
        if _path_exists(target):
            raise WorktreeReleaseError(run_id, target)
        self._remove_ownership_marker(run_id, target, repository_id)

    def _ownership_marker_present(self, run_id: UUID, target: Path, repository_id: UUID) -> bool:
        try:
            return _has_ownership_marker(target, run_id, repository_id)
        except (OSError, _InvalidOwnershipMarker) as error:
            raise WorktreeReleaseError(run_id, target) from error

    def _create_ownership_marker(self, run_id: UUID, target: Path, repository_id: UUID) -> None:
        try:
            _create_ownership_marker(target, run_id, repository_id)
        except (OSError, _InvalidOwnershipMarker) as error:
            raise WorktreeReleaseError(run_id, target) from error

    def _remove_ownership_marker(self, run_id: UUID, target: Path, repository_id: UUID) -> None:
        try:
            _remove_ownership_marker(target, run_id, repository_id)
        except (OSError, _InvalidOwnershipMarker) as error:
            raise WorktreeReleaseError(run_id, target) from error

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
        repository_id: UUID,
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
            # Persist ownership before Git can leave a partial checkout behind.
            try:
                _create_ownership_marker(staging, run_id, repository_id)
            except (OSError, _InvalidOwnershipMarker) as error:
                raise WorktreeProvisionError(run_id, target, None, filesystem_error=True) from error
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
                or linked.branch_ref != os.fsencode(f"refs/heads/{branch}")
                or linked.commit != commit
                or not _is_valid_linked_layout(target)
            ):
                raise WorktreeProvisionError(run_id, target, None)
            try:
                _create_ownership_marker(target, run_id, repository_id)
            except FileExistsError as error:
                raise _conflict(run_id, target) from error
            except (OSError, _InvalidOwnershipMarker) as error:
                raise WorktreeProvisionError(run_id, target, None, filesystem_error=True) from error
            _remove_ownership_marker(staging, run_id, repository_id)
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
                        repository_id,
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
        repository_id: UUID,
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
            await _remove_private_staging(repository_path, target, staging, branch)
            _remove_ownership_marker(staging, run_id, repository_id)
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
            _remove_ownership_marker(target, run_id, repository_id)
        except WorktreeCleanupError:
            raise
        except (GitLaunchError, OSError, TimeoutError, _InvalidOwnershipMarker) as error:
            raise WorktreeCleanupError(
                run_id,
                target,
                f"Run {run_id} failed worktree cleanup failed at {target}",
            ) from error

    def _lock(self, run_id: UUID, path: Path) -> AbstractAsyncContextManager[None]:
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


def _ownership_marker_path(target: Path, run_id: UUID) -> Path:
    if target.name.startswith(f".{run_id}.worktree-"):
        return target.with_name(f"{target.name}.owner")
    if target.name != str(run_id):
        raise _InvalidOwnershipMarker
    return target.with_name(f".{run_id}.owner")


def _ownership_marker_prefix(run_id: UUID, repository_id: UUID) -> bytes:
    return _OWNERSHIP_MARKER_MAGIC + run_id.bytes + repository_id.bytes


def _ownership_marker_payload(
    run_id: UUID,
    repository_id: UUID,
    target_identity: tuple[int, int],
) -> bytes:
    try:
        encoded_identity = b"".join(
            value.to_bytes(16, byteorder="big", signed=False) for value in target_identity
        )
    except OverflowError as error:
        raise _InvalidOwnershipMarker from error
    return _ownership_marker_prefix(run_id, repository_id) + encoded_identity


def _open_worktree_root(target: Path) -> int:
    flags = os.O_CLOEXEC | os.O_RDONLY | os.O_DIRECTORY
    flags |= getattr(os, "O_NOFOLLOW", 0)
    return os.open(target.parent, flags)


def _target_identity_at(root_descriptor: int, target_name: str) -> tuple[int, int] | None:
    flags = os.O_CLOEXEC | os.O_RDONLY | os.O_DIRECTORY
    flags |= getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(target_name, flags, dir_fd=root_descriptor)
    except FileNotFoundError:
        return None
    except OSError as error:
        raise _InvalidOwnershipMarker from error
    try:
        metadata = os.fstat(descriptor)
        return metadata.st_dev, metadata.st_ino
    finally:
        os.close(descriptor)


def _read_ownership_marker(
    root_descriptor: int,
    marker_name: str,
    expected_prefix: bytes,
) -> _OwnershipMarker | None:
    flags = os.O_CLOEXEC | os.O_RDONLY | os.O_NONBLOCK
    flags |= getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(marker_name, flags, dir_fd=root_descriptor)
    except FileNotFoundError:
        return None
    except OSError as error:
        raise _InvalidOwnershipMarker from error
    try:
        metadata = os.fstat(descriptor)
        expected_size = len(expected_prefix) + 32
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_size != expected_size:
            raise _InvalidOwnershipMarker
        contents = bytearray()
        while len(contents) <= expected_size:
            chunk = os.read(descriptor, expected_size + 1 - len(contents))
            if not chunk:
                break
            contents.extend(chunk)
        if len(contents) != expected_size or not contents.startswith(expected_prefix):
            raise _InvalidOwnershipMarker
        raw_identity = contents[len(expected_prefix) :]
        target_identity = (
            int.from_bytes(raw_identity[:16], byteorder="big", signed=False),
            int.from_bytes(raw_identity[16:], byteorder="big", signed=False),
        )
        path_metadata = os.stat(marker_name, dir_fd=root_descriptor, follow_symlinks=False)
        identity = (metadata.st_dev, metadata.st_ino)
        if (path_metadata.st_dev, path_metadata.st_ino) != identity:
            raise _InvalidOwnershipMarker
        return _OwnershipMarker(
            marker_identity=identity,
            target_identity=target_identity,
        )
    finally:
        os.close(descriptor)


def _has_ownership_marker(target: Path, run_id: UUID, repository_id: UUID) -> bool:
    marker = _ownership_marker_path(target, run_id)
    root_descriptor = _open_worktree_root(target)
    try:
        ownership = _read_ownership_marker(
            root_descriptor,
            marker.name,
            _ownership_marker_prefix(run_id, repository_id),
        )
        if ownership is None:
            return False
        target_identity = _target_identity_at(root_descriptor, target.name)
    finally:
        os.close(root_descriptor)
    if target_identity is not None and target_identity != ownership.target_identity:
        raise _InvalidOwnershipMarker
    return True


def _create_ownership_marker(target: Path, run_id: UUID, repository_id: UUID) -> None:
    marker = _ownership_marker_path(target, run_id)
    root_descriptor = _open_worktree_root(target)
    descriptor: int | None = None
    temporary_name = f".{run_id}.owner-{uuid4()}.tmp"
    try:
        target_identity = _target_identity_at(root_descriptor, target.name)
        if target_identity is None:
            raise _InvalidOwnershipMarker
        payload = _ownership_marker_payload(run_id, repository_id, target_identity)
        flags = os.O_CLOEXEC | os.O_CREAT | os.O_EXCL | os.O_WRONLY
        flags |= getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(temporary_name, flags, 0o600, dir_fd=root_descriptor)
        offset = 0
        while offset < len(payload):
            written = os.write(descriptor, payload[offset:])
            if written <= 0:
                raise OSError("ownership marker write made no progress")
            offset += written
        os.fsync(descriptor)
        os.close(descriptor)
        descriptor = None
        os.link(
            temporary_name,
            marker.name,
            src_dir_fd=root_descriptor,
            dst_dir_fd=root_descriptor,
            follow_symlinks=False,
        )
        os.fsync(root_descriptor)
        os.unlink(temporary_name, dir_fd=root_descriptor)
        os.fsync(root_descriptor)
    except BaseException:
        if descriptor is not None:
            os.close(descriptor)
        try:
            os.unlink(temporary_name, dir_fd=root_descriptor)
            os.fsync(root_descriptor)
        except FileNotFoundError:
            pass
        raise
    finally:
        os.close(root_descriptor)


def _remove_ownership_marker(target: Path, run_id: UUID, repository_id: UUID) -> None:
    marker = _ownership_marker_path(target, run_id)
    root_descriptor = _open_worktree_root(target)
    try:
        if _target_identity_at(root_descriptor, target.name) is not None:
            raise _InvalidOwnershipMarker
        ownership = _read_ownership_marker(
            root_descriptor,
            marker.name,
            _ownership_marker_prefix(run_id, repository_id),
        )
        if ownership is None:
            return
        metadata = os.stat(marker.name, dir_fd=root_descriptor, follow_symlinks=False)
        if (metadata.st_dev, metadata.st_ino) != ownership.marker_identity:
            raise _InvalidOwnershipMarker
        if _target_identity_at(root_descriptor, target.name) is not None:
            raise _InvalidOwnershipMarker
        os.fsync(root_descriptor)
        os.unlink(marker.name, dir_fd=root_descriptor)
        os.fsync(root_descriptor)
    finally:
        os.close(root_descriptor)


def _is_valid_linked_layout(path: Path) -> bool:
    git_file = path / ".git"
    return path.is_dir() and git_file.is_file() and not git_file.is_symlink()


def _has_stale_git_backpointer(target: Path, repository_path: Path) -> bool:
    flags = os.O_CLOEXEC | os.O_RDONLY | os.O_DIRECTORY
    nofollow = getattr(os, "O_NOFOLLOW", 0)
    try:
        target_descriptor = os.open(target, flags | nofollow)
    except OSError:
        return False
    try:
        try:
            git_descriptor = os.open(
                ".git",
                os.O_CLOEXEC | os.O_RDONLY | os.O_NONBLOCK | nofollow,
                dir_fd=target_descriptor,
            )
        except OSError:
            return False
        try:
            metadata = os.fstat(git_descriptor)
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > 4096:
                return False
            contents = os.read(git_descriptor, 4097)
        finally:
            os.close(git_descriptor)
    finally:
        os.close(target_descriptor)

    prefix = b"gitdir: "
    if not contents.startswith(prefix) or b"\0" in contents:
        return False
    raw_path = contents.removeprefix(prefix).removesuffix(b"\n")
    if not raw_path or b"\n" in raw_path or b"\r" in raw_path:
        return False
    git_directory = Path(os.fsdecode(raw_path))
    administrative_root = repository_path / ".git" / "worktrees"
    return (
        git_directory.is_absolute()
        and administrative_root.is_dir()
        and not administrative_root.is_symlink()
        and administrative_root.resolve() == administrative_root
        and git_directory.parent == administrative_root
        and git_directory.name not in {"", ".", ".."}
        and not git_directory.is_symlink()
        and git_directory.resolve(strict=False) == git_directory
        and not _path_exists(git_directory)
    )


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
    branch_ref = branch.removesuffix(b"\n").removesuffix(b"\r")
    commit_value = commit.strip()
    if (
        common_code
        or top_code
        or branch_code
        or commit_code
        or top_path != path
        or common_path.name != ".git"
        or not branch_ref
        or not _COMMIT_PATTERN.fullmatch(commit_value)
    ):
        raise _InvalidLinkedWorktree
    return _LinkedWorktree(
        repository_path=common_path.parent,
        branch_ref=branch_ref,
        commit=commit_value.decode("ascii").lower(),
    )


async def _worktree_has_preserved_changes(path: Path) -> bool:
    status, returncode = await run_git(
        "-C",
        str(path),
        "status",
        "--porcelain=v1",
        "-z",
        "--untracked-files=all",
        "--ignored=matching",
        "--ignore-submodules=none",
    )
    if returncode:
        raise _InvalidLinkedWorktree
    return bool(status)


async def _list_registered_worktrees(repository_path: Path) -> tuple[_RegisteredWorktree, ...]:
    output, returncode = await run_git(
        "-C", str(repository_path), "worktree", "list", "--porcelain", "-z"
    )
    if returncode or (output and not output.endswith(b"\0\0")):
        raise _InvalidLinkedWorktree

    registrations: list[_RegisteredWorktree] = []
    for record in output.split(b"\0\0"):
        if not record:
            continue
        fields = record.split(b"\0")
        if not fields or not fields[0].startswith(b"worktree "):
            raise _InvalidLinkedWorktree
        path = fields[0].removeprefix(b"worktree ")
        branch_fields = [
            field.removeprefix(b"branch ") for field in fields if field.startswith(b"branch ")
        ]
        commit_fields = [
            field.removeprefix(b"HEAD ") for field in fields if field.startswith(b"HEAD ")
        ]
        if (
            not path
            or len(branch_fields) > 1
            or len(commit_fields) != 1
            or not _COMMIT_PATTERN.fullmatch(commit_fields[0])
        ):
            raise _InvalidLinkedWorktree
        registrations.append(
            _RegisteredWorktree(
                path=path,
                branch_ref=branch_fields[0] if branch_fields else None,
                commit=commit_fields[0].lower(),
                prunable=any(
                    field == b"prunable" or field.startswith(b"prunable ") for field in fields
                ),
            )
        )
    return tuple(registrations)


async def _resolve_branch_commit(repository_path: Path, branch: str) -> bytes:
    commit, returncode = await run_git(
        "-C",
        str(repository_path),
        "show-ref",
        "--verify",
        "--hash",
        f"refs/heads/{branch}",
    )
    commit = commit.strip()
    if returncode or not _COMMIT_PATTERN.fullmatch(commit):
        raise _InvalidLinkedWorktree
    return commit.lower()


async def _is_owned_linked_worktree(path: Path, repository_path: Path, branch: str) -> bool:
    if not path.exists() or path.is_symlink():  # noqa: ASYNC240 - local metadata
        return False
    try:
        linked = await _inspect_linked_worktree(path)
    except _InvalidLinkedWorktree:
        return False
    return linked.repository_path == repository_path and linked.branch_ref == os.fsencode(
        f"refs/heads/{branch}"
    )


async def _remove_failed_worktree(repository_path: Path, path: Path) -> None:
    _, returncode = await run_git(
        "-C", str(repository_path), "worktree", "remove", "--force", str(path)
    )
    if returncode:
        raise OSError("failed to remove call-owned linked worktree")


async def _remove_private_staging(
    repository_path: Path,
    target: Path,
    staging: Path,
    branch: str,
) -> None:
    expected_prefix = f".{target.name}.worktree-"
    if staging.parent != target.parent or not staging.name.startswith(expected_prefix):
        raise OSError("invalid worktree staging path")
    if not _path_exists(staging):
        await _remove_private_registration(repository_path, staging, branch)
        return

    await run_git("-C", str(repository_path), "worktree", "remove", "--force", str(staging))
    if _path_exists(staging):
        deadline = asyncio.get_running_loop().time() + _CLEANUP_TIMEOUT_SECONDS
        await remove_owned_tree(staging, deadline=deadline)
    if _path_exists(staging):
        raise OSError("call-owned worktree staging path survived cleanup")
    await _remove_private_registration(repository_path, staging, branch)


async def _remove_private_registration(
    repository_path: Path,
    staging: Path,
    branch: str,
) -> None:
    try:
        registrations = await _list_registered_worktrees(repository_path)
    except _InvalidLinkedWorktree as error:
        raise OSError("failed to inspect call-owned worktree metadata") from error
    matches = [entry for entry in registrations if entry.path == os.fsencode(staging)]
    if not matches:
        return
    if (
        len(matches) != 1
        or matches[0].branch_ref != os.fsencode(f"refs/heads/{branch}")
        or not matches[0].prunable
    ):
        raise OSError("refused to remove unverified worktree metadata")
    _, returncode = await run_git(
        "-C", str(repository_path), "worktree", "remove", "--force", str(staging)
    )
    if returncode:
        raise OSError("failed to remove call-owned worktree metadata")


async def _complete_cleanup(
    cleanup_coroutine: Coroutine[Any, Any, None],
) -> WorktreeCleanupError | None:
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


async def _settle_release_cleanup[CleanupResult](
    cleanup_coroutine: Coroutine[Any, Any, CleanupResult],
) -> CleanupResult:
    cleanup = asyncio.create_task(cleanup_coroutine)
    cancellation: asyncio.CancelledError | None = None
    while not cleanup.done():
        try:
            await asyncio.shield(cleanup)
        except asyncio.CancelledError as error:
            cancellation = cancellation or error
            continue
        except BaseException:
            break
    try:
        result = cleanup.result()
    except BaseException as cleanup_error:
        if cancellation is not None:
            cancellation.add_note("release cleanup also failed while settling cancellation")
            raise cancellation from cleanup_error
        raise
    if cancellation is not None:
        raise cancellation
    return result
