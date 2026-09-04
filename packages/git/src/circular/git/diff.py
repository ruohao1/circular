from __future__ import annotations

import os
import stat
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol
from uuid import UUID

from circular.git._local import GitLaunchError, run_git


@dataclass(frozen=True, slots=True)
class GitDiff:
    """A byte-stable snapshot of all non-ignored worktree changes."""

    content: bytes
    changed_files: int
    contains_binary: bool

    @property
    def empty(self) -> bool:
        return self.changed_files == 0


class GitDiffCollector(Protocol):
    async def capture(self, worktree: Path) -> GitDiff: ...


class GitDiffCaptureError(RuntimeError):
    """Git could not produce a trustworthy final worktree snapshot."""

    def __init__(self, stage: str, exit_code: int | None = None) -> None:
        suffix = f" (git exit code {exit_code})" if exit_code is not None else ""
        super().__init__(f"Git diff capture failed during {stage}{suffix}")
        self.stage = stage
        self.exit_code = exit_code


class LocalGitDiffCollector:
    """Capture tracked and untracked changes without modifying the live index.

    A private temporary index is populated from ``HEAD`` and the worktree. This
    makes new files visible to ``git diff --cached`` while leaving the Run's own
    index untouched. ``--binary`` keeps binary additions reconstructable.
    """

    def __init__(self, repository_cache_root: Path | None = None) -> None:
        self._repository_cache_root = repository_cache_root

    async def capture(self, worktree: Path) -> GitDiff:
        path = Path(worktree)
        if not path.is_absolute():
            raise GitDiffCaptureError("worktree validation")

        environment: dict[str, str] = {}
        if self._repository_cache_root is not None:
            git_directory = self._trusted_git_directory(path)
            environment.update(
                GIT_DIR=str(git_directory),
                GIT_COMMON_DIR=str(git_directory.parent.parent),
                GIT_WORK_TREE=str(path),
            )
        index_path = self._temporary_index(path)
        environment["GIT_INDEX_FILE"] = os.fspath(index_path)
        try:
            await self._run(
                path,
                "index initialization",
                "read-tree",
                "HEAD",
                environment=environment,
            )
            await self._run(
                path,
                "worktree snapshot",
                "add",
                "--all",
                "--",
                ".",
                environment=environment,
            )
            content = await self._run(
                path,
                "patch generation",
                "diff",
                "--cached",
                "--binary",
                "--full-index",
                "--no-ext-diff",
                "--no-textconv",
                "--no-color",
                "HEAD",
                "--",
                environment=environment,
            )
            names = await self._run(
                path,
                "changed-file enumeration",
                "diff",
                "--cached",
                "--name-only",
                "-z",
                "HEAD",
                "--",
                environment=environment,
            )
            numstat = await self._run(
                path,
                "binary-file detection",
                "diff",
                "--cached",
                "--numstat",
                "-z",
                "HEAD",
                "--",
                environment=environment,
            )
        finally:
            try:
                index_path.unlink()
            except FileNotFoundError:
                pass
            except OSError as error:
                raise GitDiffCaptureError("temporary-index cleanup") from error

        changed_files = sum(bool(name) for name in names.split(b"\0"))
        contains_binary = any(record.startswith(b"-\t-\t") for record in numstat.split(b"\0"))
        return GitDiff(
            content=content,
            changed_files=changed_files,
            contains_binary=contains_binary,
        )

    def _trusted_git_directory(self, worktree: Path) -> Path:
        """Do not let agent-written .git data redirect trusted worker Git commands."""
        try:
            descriptor = os.open(worktree / ".git", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            try:
                metadata = os.fstat(descriptor)
                if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > 4096:
                    raise ValueError("invalid Git backpointer")
                pointer = os.read(descriptor, 4097).decode().removesuffix("\n")
            finally:
                os.close(descriptor)
            if not pointer.startswith("gitdir: "):
                raise ValueError("not a linked worktree")
            git_directory = Path(pointer.removeprefix("gitdir: "))
            repository = git_directory.parent.parent.parent
            UUID(repository.name)
            UUID(worktree.name)
            if (
                not git_directory.is_absolute()
                or git_directory.resolve(strict=True) != git_directory
                or repository.parent != self._repository_cache_root
                or git_directory.parent.name != "worktrees"
                or git_directory.parent.parent.name != ".git"
                or (git_directory / "gitdir").read_text().strip() != str(worktree / ".git")
                or (git_directory / "HEAD").read_text().strip()
                != f"ref: refs/heads/circular/run/{worktree.name}"
            ):
                raise ValueError("foreign Git metadata")
            return git_directory
        except (OSError, ValueError, UnicodeError) as error:
            raise GitDiffCaptureError("worktree ownership validation") from error

    @staticmethod
    def _temporary_index(worktree: Path) -> Path:
        try:
            descriptor, raw_path = tempfile.mkstemp(
                prefix=f".{worktree.name}.diff-index-",
                dir=worktree.parent,
            )
            os.close(descriptor)
            index_path = Path(raw_path)
            index_path.unlink()
            return index_path
        except OSError as error:
            raise GitDiffCaptureError("temporary-index creation") from error

    @staticmethod
    async def _run(
        worktree: Path,
        stage: str,
        *arguments: str,
        environment: dict[str, str],
    ) -> bytes:
        try:
            stdout, returncode = await run_git(
                "--literal-pathspecs",
                "-C",
                os.fspath(worktree),
                *arguments,
                extra_environment=environment,
            )
        except GitLaunchError as error:
            raise GitDiffCaptureError(stage) from error
        if returncode != 0:
            raise GitDiffCaptureError(stage, returncode)
        return stdout
