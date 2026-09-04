import io
import os
from pathlib import Path
from uuid import UUID

import pytest
from circular.storage import ArtifactContentError, LocalArtifactContentStore

RUN_ID = UUID("00000000-0000-4000-8000-000000000172")


async def test_publication_syncs_new_directory_entries_even_after_an_interrupted_write(
    tmp_path, monkeypatch
):
    root = tmp_path / "new-parent" / "artifacts"
    store = LocalArtifactContentStore(root)
    synced = []
    original_fsync = os.fsync

    def record_sync(descriptor):
        metadata = os.fstat(descriptor)
        synced.append((metadata.st_dev, metadata.st_ino))
        original_fsync(descriptor)

    monkeypatch.setattr(os, "fsync", record_sync)
    # An interrupted writer may have made these directories without syncing them.
    (root / str(RUN_ID)).mkdir(parents=True)
    await store.write(RUN_ID, "output.txt", b"durable")

    for directory in (tmp_path, root.parent, root, root / str(RUN_ID)):
        metadata = directory.stat()
        assert (metadata.st_dev, metadata.st_ino) in synced


async def test_failed_parent_sync_is_not_acknowledged_and_publication_can_retry(
    tmp_path, monkeypatch
):
    root = tmp_path / "artifacts"
    store = LocalArtifactContentStore(root)
    original_fsync = os.fsync

    def fail_parent_sync(descriptor):
        info = os.fstat(descriptor)
        parent = root.stat()
        if (info.st_dev, info.st_ino) == (parent.st_dev, parent.st_ino):
            raise OSError("injected parent sync failure")
        original_fsync(descriptor)

    with monkeypatch.context() as failing:
        failing.setattr(os, "fsync", fail_parent_sync)
        with pytest.raises(ArtifactContentError):
            await store.write(RUN_ID, "output.txt", b"durable")
    stored = await store.write(RUN_ID, "output.txt", b"durable")
    assert await store.read(RUN_ID, stored.uri) == b"durable"


def test_stream_publication_uses_bounded_reads_and_immutable_retries(tmp_path):
    class BoundedReader(io.BytesIO):
        def read(self, size=-1):
            assert 0 < size <= 1024 * 1024
            return super().read(size)

    store = LocalArtifactContentStore(tmp_path / "artifacts")
    payload = b"output" * 500_000
    first = store.write_stream(RUN_ID, "worktree.tar", BoundedReader(payload))
    assert first.size_bytes == 3_000_000
    assert store.write_stream(RUN_ID, "worktree.tar", BoundedReader(payload)) == first
    with pytest.raises(ArtifactContentError, match="immutable"):
        store.write_stream(RUN_ID, "worktree.tar", BoundedReader(b"different"))


async def test_local_artifact_content_survives_worktree_removal(tmp_path: Path) -> None:
    worktree = tmp_path / "worktrees" / str(RUN_ID)
    worktree.mkdir(parents=True)
    artifact_root = (tmp_path / "artifacts").resolve()
    store = LocalArtifactContentStore(artifact_root)

    stored = await store.write(RUN_ID, "git-diff.patch", b"diff content\n")
    worktree.rmdir()

    assert stored.uri == f"artifact://{RUN_ID}/git-diff.patch"
    assert stored.size_bytes == len(b"diff content\n")
    assert len(stored.sha256) == 64
    assert await store.read(RUN_ID, stored.uri) == b"diff content\n"


async def test_local_artifact_content_rejects_foreign_and_traversing_uris(
    tmp_path: Path,
) -> None:
    store = LocalArtifactContentStore((tmp_path / "artifacts").resolve())

    for uri in (
        "artifact://00000000-0000-4000-8000-000000000999/git-diff.patch",
        f"artifact://{RUN_ID}/../secret",
        f"artifact://{RUN_ID}/nested/secret",
        f"file://{tmp_path}/secret",
    ):
        with pytest.raises(ArtifactContentError):
            await store.read(RUN_ID, uri)


async def test_local_artifact_content_is_idempotent_but_never_overwrites(
    tmp_path: Path,
) -> None:
    store = LocalArtifactContentStore((tmp_path / "artifacts").resolve())

    first = await store.write(RUN_ID, "git-diff.patch", b"first")
    second = await store.write(RUN_ID, "git-diff.patch", b"first")

    assert first.uri == second.uri
    with pytest.raises(ArtifactContentError, match="immutable"):
        await store.write(RUN_ID, "git-diff.patch", b"second")
    assert await store.read(RUN_ID, second.uri) == b"first"


async def test_artifact_content_rejects_file_and_cross_run_directory_symlinks(tmp_path):
    root = tmp_path / "artifacts"
    store = LocalArtifactContentStore(root)
    stored = await store.write(RUN_ID, "output.txt", b"owned")
    path = root / str(RUN_ID) / "output.txt"
    outside = tmp_path / "secret"
    outside.write_bytes(b"private")
    path.unlink()
    path.symlink_to(outside)
    with pytest.raises(ArtifactContentError):
        await store.read(RUN_ID, stored.uri)
    foreign_id = UUID("00000000-0000-4000-8000-000000000999")
    (root / str(foreign_id)).symlink_to(root / str(RUN_ID), target_is_directory=True)
    with pytest.raises(ArtifactContentError):
        await store.read(foreign_id, store.uri(foreign_id, "output.txt"))


async def test_artifact_content_rejects_fifo_without_blocking(tmp_path):
    root = tmp_path / "artifacts"
    (root / str(RUN_ID)).mkdir(parents=True)
    os.mkfifo(root / str(RUN_ID) / "pipe")
    store = LocalArtifactContentStore(root)
    with pytest.raises(ArtifactContentError, match="regular file"):
        await store.read(RUN_ID, store.uri(RUN_ID, "pipe"))
