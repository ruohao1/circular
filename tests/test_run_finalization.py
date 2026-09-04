from dataclasses import dataclass
from pathlib import Path
from uuid import UUID

import pytest
from circular.domain import Artifact
from circular.git import GitDiff
from circular.runners import (
    ExecutionDirectories,
    InvalidRunFinalizationState,
    RunFinalizationContext,
    RunFinalizer,
    diff_artifact_id_for_run,
)
from circular.storage import StoredArtifactContent

RUN_ID = UUID("00000000-0000-4000-8000-000000000172")


class RecordingPersistence:
    def __init__(self, context: RunFinalizationContext) -> None:
        self.context = context
        self.persisted: list[tuple[Artifact, GitDiff]] = []

    async def load_context(self, run_id: UUID) -> RunFinalizationContext:
        assert run_id == RUN_ID
        return self.context

    async def persist_diff(self, artifact: Artifact, diff: GitDiff) -> None:
        self.persisted.append((artifact, diff))


@dataclass
class StubDiffCollector:
    result: GitDiff
    captured_path: Path | None = None

    async def capture(self, worktree: Path) -> GitDiff:
        self.captured_path = worktree
        return self.result


class MemoryContentStore:
    def __init__(self) -> None:
        self.writes: list[tuple[UUID, str, bytes]] = []

    async def write(
        self,
        run_id: UUID,
        name: str,
        content: bytes,
    ) -> StoredArtifactContent:
        self.writes.append((run_id, name, content))
        return StoredArtifactContent(
            uri=f"artifact://{run_id}/{name}",
            size_bytes=len(content),
            sha256="a" * 64,
        )

    async def read(self, run_id: UUID, uri: str) -> bytes:
        raise AssertionError("finalization does not read artifact content")


def directories(tmp_path: Path) -> ExecutionDirectories:
    return ExecutionDirectories(
        repository_cache_root=(tmp_path / "repositories").resolve(),
        worktree_root=(tmp_path / "worktrees").resolve(),
        artifact_root=(tmp_path / "artifacts").resolve(),
        docker_worktree_root=(tmp_path / "docker-worktrees").resolve(),
    )


async def test_finalizer_persists_content_and_complete_diff_metadata(tmp_path: Path) -> None:
    roots = directories(tmp_path)
    worktree = roots.run_paths(RUN_ID).worktree
    worktree.mkdir(parents=True)
    diff = GitDiff(b"diff --git a/file b/file\n", changed_files=1, contains_binary=False)
    persistence = RecordingPersistence(RunFinalizationContext(RUN_ID, worktree))
    collector = StubDiffCollector(diff)
    content = MemoryContentStore()

    artifact = await RunFinalizer(persistence, collector, content, roots).finalize(RUN_ID)

    assert collector.captured_path == worktree
    assert content.writes == [(RUN_ID, "git-diff.patch", diff.content)]
    assert artifact.id == diff_artifact_id_for_run(RUN_ID)
    assert artifact.kind == "diff"
    assert artifact.metadata == {
        "media_type": "text/x-diff",
        "size_bytes": len(diff.content),
        "sha256": "a" * 64,
        "changed_files": 1,
        "contains_binary": False,
        "empty": False,
    }
    assert persistence.persisted == [(artifact, diff)]


async def test_finalizer_persists_an_empty_diff_as_a_zero_byte_artifact(tmp_path: Path) -> None:
    roots = directories(tmp_path)
    worktree = roots.run_paths(RUN_ID).worktree
    worktree.mkdir(parents=True)
    diff = GitDiff(b"", changed_files=0, contains_binary=False)
    persistence = RecordingPersistence(RunFinalizationContext(RUN_ID, worktree))
    content = MemoryContentStore()

    artifact = await RunFinalizer(
        persistence,
        StubDiffCollector(diff),
        content,
        roots,
    ).finalize(RUN_ID)

    assert artifact.metadata["empty"] is True
    assert artifact.metadata["size_bytes"] == 0
    assert persistence.persisted == [(artifact, diff)]


async def test_finalizer_rejects_a_workspace_path_outside_the_run_root(
    tmp_path: Path,
) -> None:
    roots = directories(tmp_path)
    outside = (tmp_path / "outside").resolve()
    persistence = RecordingPersistence(RunFinalizationContext(RUN_ID, outside))
    collector = StubDiffCollector(GitDiff(b"", 0, False))

    with pytest.raises(InvalidRunFinalizationState, match="outside"):
        await RunFinalizer(persistence, collector, MemoryContentStore(), roots).finalize(RUN_ID)

    assert collector.captured_path is None
    assert persistence.persisted == []
