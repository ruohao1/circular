from __future__ import annotations

import hashlib
import io
import os
import re
import stat
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import BinaryIO, Protocol
from uuid import UUID

_ARTIFACT_NAME = re.compile(r"[a-z0-9][a-z0-9._-]{0,127}\Z")
_CHUNK_BYTES = 1024 * 1024


class ArtifactContentError(RuntimeError):
    """Durable artifact bytes could not be safely stored or retrieved."""


@dataclass(frozen=True, slots=True)
class StoredArtifactContent:
    uri: str
    size_bytes: int
    sha256: str


class ArtifactContentStore(Protocol):
    async def write(self, run_id: UUID, name: str, content: bytes) -> StoredArtifactContent: ...

    async def read(self, run_id: UUID, uri: str) -> bytes: ...


class LocalArtifactContentStore:
    """Store immutable Run output beneath a root never mounted into a Run."""

    def __init__(self, root: Path) -> None:
        candidate = Path(root).expanduser()
        if not candidate.is_absolute():
            raise ValueError("artifact root must be absolute")
        candidate = candidate.resolve(strict=False)
        if candidate == Path(candidate.anchor):
            raise ValueError("artifact root cannot be the filesystem root")
        self._root = candidate

    async def write(
        self,
        run_id: UUID,
        name: str,
        content: bytes,
    ) -> StoredArtifactContent:
        self._validate_name(name)
        if not isinstance(content, bytes):
            raise TypeError("artifact content must be bytes")
        return self.write_stream(run_id, name, io.BytesIO(content))

    async def read(self, run_id: UUID, uri: str) -> bytes:
        name = self._name_from_uri(run_id, uri)
        return self._read(run_id, name)

    @staticmethod
    def uri(run_id: UUID, name: str) -> str:
        LocalArtifactContentStore._validate_name(name)
        return f"artifact://{run_id}/{name}"

    def write_stream(self, run_id: UUID, name: str, content: BinaryIO) -> StoredArtifactContent:
        """Publish with bounded memory; callers run large uploads on an I/O thread."""
        self._validate_name(name)
        run_directory = self._run_directory(run_id)
        temporary_path: Path | None = None
        digest = hashlib.sha256()
        size = 0
        try:
            self._root.mkdir(mode=0o700, parents=True, exist_ok=True)
            run_directory.mkdir(mode=0o700, exist_ok=True)
            descriptor, raw_temporary_path = tempfile.mkstemp(
                prefix=f".{name}.",
                dir=run_directory,
            )
            temporary_path = Path(raw_temporary_path)
            with os.fdopen(descriptor, "wb") as stream:
                while chunk := content.read(_CHUNK_BYTES):
                    stream.write(chunk)
                    digest.update(chunk)
                    size += len(chunk)
                stream.flush()
                os.fsync(stream.fileno())
            os.chmod(temporary_path, 0o600, follow_symlinks=False)
            try:
                # Publish complete bytes without ever replacing an existing URI.
                os.link(temporary_path, run_directory / name, follow_symlinks=False)
            except FileExistsError:
                with self._open_content(run_id, name) as existing:
                    existing_digest = hashlib.file_digest(existing, "sha256").hexdigest()
                    existing_size = os.fstat(existing.fileno()).st_size
                if existing_size != size or existing_digest != digest.hexdigest():
                    raise ArtifactContentError("artifact content is immutable") from None
            # Sync every parent entry, including on retries: an interrupted writer
            # may have created directories without making their names durable.
            for directory in (run_directory, *run_directory.parents):
                directory_descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(directory_descriptor)
                finally:
                    os.close(directory_descriptor)
            return StoredArtifactContent(
                uri=self.uri(run_id, name), size_bytes=size, sha256=digest.hexdigest()
            )
        except OSError as error:
            raise ArtifactContentError("artifact content could not be stored") from error
        finally:
            if temporary_path is not None:
                try:
                    temporary_path.unlink()
                except FileNotFoundError:
                    pass

    def _read(self, run_id: UUID, name: str) -> bytes:
        try:
            with self._open_content(run_id, name) as stream:
                return stream.read()
        except OSError as error:
            raise ArtifactContentError("artifact content is unavailable") from error

    def _open_content(self, run_id: UUID, name: str) -> BinaryIO:
        path = self._run_directory(run_id) / name
        flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NONBLOCK
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        try:
            descriptor = os.open(path, flags)
            stream = os.fdopen(descriptor, "rb")
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                stream.close()
                raise ArtifactContentError("artifact content is not a regular file")
            return stream
        except (FileNotFoundError, OSError) as error:
            raise ArtifactContentError("artifact content is unavailable") from error

    def _run_directory(self, run_id: UUID) -> Path:
        if not isinstance(run_id, UUID):
            raise TypeError("artifact paths require a Run UUID")
        candidate = self._root / str(run_id)
        if candidate.resolve(strict=False) != candidate or candidate.is_symlink():
            raise ArtifactContentError("artifact path escaped the Run directory")
        return candidate

    @staticmethod
    def _validate_name(name: str) -> None:
        if not isinstance(name, str) or _ARTIFACT_NAME.fullmatch(name) is None:
            raise ArtifactContentError("artifact name is invalid")

    @staticmethod
    def _name_from_uri(run_id: UUID, uri: str) -> str:
        prefix = f"artifact://{run_id}/"
        if not isinstance(uri, str) or not uri.startswith(prefix):
            raise ArtifactContentError("artifact URI does not belong to the Run")
        name = uri.removeprefix(prefix)
        LocalArtifactContentStore._validate_name(name)
        if uri != LocalArtifactContentStore.uri(run_id, name):
            raise ArtifactContentError("artifact URI is not canonical")
        return name
