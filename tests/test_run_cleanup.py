import io
import os
import tarfile
import tempfile

import pytest
from circular.runners.cleanup import archive_worktree


def test_output_archive_excludes_git_and_never_follows_symlinks(tmp_path):
    worktree = tmp_path / "worktree"
    worktree.mkdir()
    (worktree / ".git").write_text("private metadata")
    (worktree / "output.txt").write_text("retained")
    secret = tmp_path / "secret"
    secret.write_text("must not be read")
    (worktree / "link").symlink_to(secret)
    destination = io.BytesIO()
    archive_worktree(worktree, destination)
    content = destination.getvalue()
    assert b"must not be read" not in content
    with tarfile.open(fileobj=io.BytesIO(content)) as archive:
        assert set(archive.getnames()) == {"output.txt", "link"}
        assert archive.getmember("link").issym()
        assert archive.extractfile("output.txt").read() == b"retained"


def test_output_archive_rejects_special_files_without_blocking(tmp_path):
    os.mkfifo(tmp_path / "pipe")
    with pytest.raises(ValueError, match="special file"):
        archive_worktree(tmp_path, io.BytesIO())


@pytest.mark.skipif(os.getuid() == 0, reason="root can read mode-000 directories")
def test_output_archive_never_silently_omits_unreadable_directories(tmp_path):
    hidden = tmp_path / "unreadable"
    hidden.mkdir()
    (hidden / "output").write_text("must not be lost")
    hidden.chmod(0)
    try:
        with pytest.raises(PermissionError):
            archive_worktree(tmp_path, io.BytesIO())
    finally:
        hidden.chmod(0o700)


def test_output_archive_streams_checkouts_larger_than_32_mib(tmp_path):
    size = 33 * 1024 * 1024
    output = tmp_path / "large-output"
    with output.open("wb") as stream:
        stream.truncate(size)
    with tempfile.TemporaryFile() as content:
        archive_worktree(tmp_path, content)
        content.seek(0)
        with tarfile.open(fileobj=content) as archive:
            assert archive.getmember("large-output").size == size
    assert output.stat().st_size == size
