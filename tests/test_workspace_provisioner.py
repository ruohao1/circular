import asyncio
import json
from dataclasses import replace
from pathlib import Path
from typing import Any
from uuid import UUID

import pytest
from circular.domain import RunStatus, Workspace, WorkspaceStatus
from circular.git import ProvisionedWorktree
from circular.runners import (
    ExecutionDirectories,
    FakeWorkloadSpecFactory,
    WorkspaceProvisioner,
    WorkspaceProvisioningContext,
)
from circular.runtimes import ContainerHandle, ContainerSpec

RUN_ID = UUID("00000000-0000-4000-8000-000000000169")
REPOSITORY_ID = UUID("00000000-0000-4000-8000-000000000166")
WORKSPACE_ID = UUID("00000000-0000-4000-8000-000000000269")


def _directories(tmp_path: Path) -> ExecutionDirectories:
    return ExecutionDirectories(
        repository_cache_root=tmp_path / "repositories",
        worktree_root=tmp_path / "worker-worktrees",
        artifact_root=tmp_path / "artifacts",
        docker_worktree_root=tmp_path / "host-worktrees",
    )


def _context() -> WorkspaceProvisioningContext:
    return WorkspaceProvisioningContext(
        run_id=RUN_ID,
        workspace_id=WORKSPACE_ID,
        repository_id=REPOSITORY_ID,
        clone_url="https://example.test/project.git",
        base_ref="main",
        backend="fake",
        task_title="Provision a workspace",
        task_description="Create all resources before execution.",
        instructions="Keep ownership boundaries explicit.",
    )


class RecordingPersistence:
    def __init__(self, context: WorkspaceProvisioningContext) -> None:
        self.context = context
        self.calls: list[tuple[Any, ...]] = []
        self.workspace: Workspace | None = None
        self.run_status = RunStatus.PROVISIONING
        self.failure: Exception | None = None

    async def load_context(self, run_id: UUID) -> WorkspaceProvisioningContext:
        self.calls.append(("load_context", run_id))
        if self.failure is not None:
            raise self.failure
        return self.context

    async def create_pending(self, workspace: Workspace) -> Workspace:
        self.calls.append(("create_pending", workspace))
        if self.failure is not None:
            raise self.failure
        self.workspace = workspace
        return workspace

    async def record_container(self, workspace_id: UUID, container_id: str) -> Workspace:
        self.calls.append(("record_container", workspace_id, container_id))
        if self.failure is not None:
            raise self.failure
        assert self.workspace is not None
        self.workspace = replace(self.workspace, container_id=container_id)
        return self.workspace

    async def mark_ready_and_running(self, workspace_id: UUID, backend: str) -> Workspace:
        self.calls.append(("mark_ready_and_running", workspace_id, backend))
        if self.failure is not None:
            raise self.failure
        assert self.workspace is not None
        assert self.workspace.container_id is not None
        self.workspace = replace(self.workspace, status=WorkspaceStatus.READY)
        self.run_status = RunStatus.RUNNING
        return self.workspace

    async def mark_failed(
        self,
        run_id: UUID,
        error: Exception,
        *,
        container_id: str | None,
    ) -> None:
        self.calls.append(("mark_failed", run_id, error, container_id))
        self.run_status = RunStatus.FAILED
        if self.workspace is not None:
            self.workspace = replace(
                self.workspace,
                container_id=container_id or self.workspace.container_id,
                status=WorkspaceStatus.FAILED,
            )


class RecordingCache:
    def __init__(self, repository_path: Path, calls: list[tuple[Any, ...]]) -> None:
        self.repository_path = repository_path
        self.calls = calls
        self.failure: Exception | None = None

    async def checkout(self, repository_id: UUID, clone_url: str) -> Path:
        self.calls.append(("checkout", repository_id, clone_url))
        if self.failure is not None:
            raise self.failure
        return self.repository_path


class RecordingWorktrees:
    def __init__(self, worktree: ProvisionedWorktree, calls: list[tuple[Any, ...]]) -> None:
        self.worktree = worktree
        self.calls = calls
        self.failure: Exception | None = None

    async def provision(
        self,
        run_id: UUID,
        repository_path: Path,
        base_ref: str,
    ) -> ProvisionedWorktree:
        self.calls.append(("provision", run_id, repository_path, base_ref))
        if self.failure is not None:
            raise self.failure
        return self.worktree

    async def release(self, worktree: ProvisionedWorktree) -> None:
        raise AssertionError("provisioning must not perform cleanup")


class RecordingRuntime:
    def __init__(self, calls: list[tuple[Any, ...]]) -> None:
        self.calls = calls
        self.spec: ContainerSpec | None = None
        self.handle = ContainerHandle("container-169")
        self.failure: Exception | None = None

    async def start(self, spec: ContainerSpec) -> ContainerHandle:
        self.calls.append(("runtime.start", spec))
        self.spec = spec
        if self.failure is not None:
            raise self.failure
        return self.handle

    def output(self, handle: ContainerHandle):
        raise AssertionError("event ingestion is outside workspace provisioning")

    async def wait(self, handle: ContainerHandle):
        raise AssertionError("execution completion is outside workspace provisioning")

    async def stop(self, handle: ContainerHandle) -> None:
        raise AssertionError("provisioning failures do not implement cleanup")


def _system(tmp_path: Path):
    context = _context()
    directories = _directories(tmp_path)
    persistence = RecordingPersistence(context)
    external_calls: list[tuple[Any, ...]] = []
    repository_path = directories.repository_cache_path(REPOSITORY_ID)
    worktree = ProvisionedWorktree(
        run_id=RUN_ID,
        repository_path=repository_path,
        path=directories.run_paths(RUN_ID).worktree,
        branch=f"circular/run/{RUN_ID}",
    )
    cache = RecordingCache(repository_path, external_calls)
    worktrees = RecordingWorktrees(worktree, external_calls)
    runtime = RecordingRuntime(external_calls)
    provisioner = WorkspaceProvisioner(
        persistence=persistence,
        repository_cache=cache,
        worktrees=worktrees,
        runtime=runtime,
        directories=directories,
        spec_factory=FakeWorkloadSpecFactory(
            image="circular-runner:test",
            cpu_limit=1.5,
            memory_limit_mb=768,
            delay_ms=25,
        ),
    )
    return provisioner, persistence, cache, worktrees, runtime, external_calls, directories


async def test_claimed_run_is_running_only_after_its_workspace_is_ready(
    tmp_path: Path,
) -> None:
    (
        provisioner,
        persistence,
        _cache,
        _worktrees,
        runtime,
        external_calls,
        directories,
    ) = _system(tmp_path)

    workspace = await provisioner.provision(RUN_ID)

    worker_worktree = directories.run_paths(RUN_ID).worktree
    assert workspace == Workspace(
        id=WORKSPACE_ID,
        run_id=RUN_ID,
        worktree_path=str(worker_worktree),
        container_id="container-169",
        status=WorkspaceStatus.READY,
    )
    assert persistence.run_status is RunStatus.RUNNING
    assert [call[0] for call in persistence.calls] == [
        "load_context",
        "create_pending",
        "record_container",
        "mark_ready_and_running",
    ]
    assert [call[0] for call in external_calls] == [
        "checkout",
        "provision",
        "runtime.start",
    ]

    assert runtime.spec is not None
    assert runtime.spec.run_id == RUN_ID
    assert runtime.spec.worktree == directories.run_paths(RUN_ID).docker_host_worktree
    assert runtime.spec.image == "circular-runner:test"
    assert runtime.spec.command == ()
    assert runtime.spec.cpu_limit == 1.5
    assert runtime.spec.memory_limit_mb == 768
    assert runtime.spec.environment == {}
    assert runtime.spec.network_enabled is False
    assert json.loads(runtime.spec.stdin) == {
        "protocol_version": 1,
        "run": {
            "id": str(RUN_ID),
            "task_title": "Provision a workspace",
            "task_description": "Create all resources before execution.",
            "instructions": "Keep ownership boundaries explicit.",
        },
        "behavior": {"delay_ms": 25, "failure": "none"},
    }
    assert runtime.spec.stdin.endswith(b"\n")


@pytest.mark.parametrize("step", ["cache", "worktree", "runtime"])
async def test_provisioning_failure_marks_partial_workspace_and_run_failed(
    tmp_path: Path,
    step: str,
) -> None:
    (
        provisioner,
        persistence,
        cache,
        worktrees,
        runtime,
        external_calls,
        directories,
    ) = _system(tmp_path)
    failure = RuntimeError(f"{step} failed")
    target = {"cache": cache, "worktree": worktrees, "runtime": runtime}[step]
    target.failure = failure

    with pytest.raises(RuntimeError, match=rf"{step} failed") as exc_info:
        await provisioner.provision(RUN_ID)

    assert exc_info.value is failure
    assert persistence.run_status is RunStatus.FAILED
    assert persistence.workspace == Workspace(
        id=WORKSPACE_ID,
        run_id=RUN_ID,
        worktree_path=str(directories.run_paths(RUN_ID).worktree),
        status=WorkspaceStatus.FAILED,
    )
    assert persistence.calls[-1] == ("mark_failed", RUN_ID, failure, None)
    expected_calls = {
        "cache": ["checkout"],
        "worktree": ["checkout", "provision"],
        "runtime": ["checkout", "provision", "runtime.start"],
    }
    assert [call[0] for call in external_calls] == expected_calls[step]


async def test_container_identity_survives_a_persistence_failure_after_start(
    tmp_path: Path,
) -> None:
    provisioner, persistence, _cache, _worktrees, _runtime, _calls, _directories = _system(tmp_path)
    original_record_container = persistence.record_container
    failure = RuntimeError("container persistence failed")

    async def fail_once(workspace_id: UUID, container_id: str) -> Workspace:
        persistence.record_container = original_record_container
        raise failure

    persistence.record_container = fail_once  # type: ignore[method-assign]

    with pytest.raises(RuntimeError, match="container persistence failed") as exc_info:
        await provisioner.provision(RUN_ID)

    assert exc_info.value is failure
    assert persistence.workspace is not None
    assert persistence.workspace.status is WorkspaceStatus.FAILED
    assert persistence.workspace.container_id == "container-169"
    assert persistence.calls[-1] == ("mark_failed", RUN_ID, failure, "container-169")


async def test_workspace_creation_failure_fails_run_before_external_resources(
    tmp_path: Path,
) -> None:
    provisioner, persistence, _cache, _worktrees, _runtime, calls, _directories = _system(tmp_path)
    original_create_pending = persistence.create_pending
    failure = RuntimeError("workspace creation failed")

    async def fail_once(workspace: Workspace) -> Workspace:
        persistence.create_pending = original_create_pending
        raise failure

    persistence.create_pending = fail_once  # type: ignore[method-assign]

    with pytest.raises(RuntimeError, match="workspace creation failed") as exc_info:
        await provisioner.provision(RUN_ID)

    assert exc_info.value is failure
    assert calls == []
    assert persistence.workspace is None
    assert persistence.run_status is RunStatus.FAILED
    assert persistence.calls[-1] == ("mark_failed", RUN_ID, failure, None)


async def test_final_state_or_event_failure_retains_container_and_fails_both_lifecycles(
    tmp_path: Path,
) -> None:
    provisioner, persistence, _cache, _worktrees, _runtime, _calls, _directories = _system(tmp_path)
    original_finalize = persistence.mark_ready_and_running
    failure = RuntimeError("ready/running transaction failed")

    async def fail_once(workspace_id: UUID, backend: str) -> Workspace:
        persistence.mark_ready_and_running = original_finalize
        raise failure

    persistence.mark_ready_and_running = fail_once  # type: ignore[method-assign]

    with pytest.raises(RuntimeError, match="ready/running transaction failed") as exc_info:
        await provisioner.provision(RUN_ID)

    assert exc_info.value is failure
    assert persistence.run_status is RunStatus.FAILED
    assert persistence.workspace is not None
    assert persistence.workspace.status is WorkspaceStatus.FAILED
    assert persistence.workspace.container_id == "container-169"


async def test_failure_state_error_does_not_mask_the_provisioning_error(tmp_path: Path) -> None:
    provisioner, persistence, cache, _worktrees, _runtime, _calls, _directories = _system(tmp_path)
    primary = RuntimeError("cache unavailable")
    secondary = RuntimeError("database unavailable")
    cache.failure = primary

    async def fail_failure_state(
        run_id: UUID,
        error: Exception,
        *,
        container_id: str | None,
    ) -> None:
        raise secondary

    persistence.mark_failed = fail_failure_state  # type: ignore[method-assign]

    with pytest.raises(RuntimeError, match="cache unavailable") as exc_info:
        await provisioner.provision(RUN_ID)

    assert exc_info.value is primary
    assert getattr(primary, "__notes__", ()) == [
        "failed to persist provisioning failure (RuntimeError)"
    ]


async def test_cancellation_leaves_partial_state_for_recovery(tmp_path: Path) -> None:
    provisioner, persistence, cache, _worktrees, _runtime, _calls, _directories = _system(tmp_path)
    cache.failure = asyncio.CancelledError()

    with pytest.raises(asyncio.CancelledError):
        await provisioner.provision(RUN_ID)

    assert persistence.run_status is RunStatus.PROVISIONING
    assert persistence.workspace is not None
    assert persistence.workspace.status is WorkspaceStatus.PENDING
    assert all(call[0] != "mark_failed" for call in persistence.calls)


async def test_cancellation_after_container_start_finishes_identity_persistence(
    tmp_path: Path,
) -> None:
    provisioner, persistence, _cache, _worktrees, _runtime, _calls, _directories = _system(tmp_path)
    record_started = asyncio.Event()
    allow_record = asyncio.Event()
    original_record_container = persistence.record_container

    async def slow_record(workspace_id: UUID, container_id: str) -> Workspace:
        record_started.set()
        await allow_record.wait()
        return await original_record_container(workspace_id, container_id)

    persistence.record_container = slow_record  # type: ignore[method-assign]
    provisioning = asyncio.create_task(provisioner.provision(RUN_ID))
    await record_started.wait()

    provisioning.cancel()
    allow_record.set()
    with pytest.raises(asyncio.CancelledError):
        await provisioning

    assert persistence.run_status is RunStatus.PROVISIONING
    assert persistence.workspace is not None
    assert persistence.workspace.status is WorkspaceStatus.PENDING
    assert persistence.workspace.container_id == "container-169"
    assert all(call[0] != "mark_failed" for call in persistence.calls)


@pytest.mark.parametrize(
    ("boundary", "message"),
    [
        ("context", "context does not belong to the requested Run"),
        ("cache", "cache returned a path outside the managed Repository checkout"),
        ("worktree_run", "worktree does not belong to the requested Run"),
        ("worktree_repository", "worktree references a different Repository checkout"),
        ("worktree_path", "worktree manager returned a path not owned by the Run"),
        ("worktree_branch", "worktree manager returned an unexpected Run branch"),
        ("container", "runtime returned an invalid container identity"),
    ],
)
async def test_untrusted_port_results_fail_closed_before_the_next_handoff(
    tmp_path: Path,
    boundary: str,
    message: str,
) -> None:
    provisioner, persistence, cache, worktrees, runtime, external_calls, directories = _system(
        tmp_path
    )
    if boundary == "context":
        persistence.context = replace(persistence.context, run_id=UUID(int=999))
    elif boundary == "cache":
        cache.repository_path = tmp_path / "unmanaged-cache"
    elif boundary == "worktree_run":
        worktrees.worktree = replace(worktrees.worktree, run_id=UUID(int=998))
    elif boundary == "worktree_repository":
        worktrees.worktree = replace(
            worktrees.worktree,
            repository_path=tmp_path / "other-repository",
        )
    elif boundary == "worktree_path":
        worktrees.worktree = replace(worktrees.worktree, path=tmp_path / "other-worktree")
    elif boundary == "worktree_branch":
        worktrees.worktree = replace(worktrees.worktree, branch="unexpected/branch")
    elif boundary == "container":
        runtime.handle = ContainerHandle("")

    with pytest.raises(ValueError, match=message):
        await provisioner.provision(RUN_ID)

    assert persistence.run_status is RunStatus.FAILED
    if boundary == "context":
        assert external_calls == []
        assert persistence.workspace is None
    elif boundary == "cache":
        assert [call[0] for call in external_calls] == ["checkout"]
    elif boundary.startswith("worktree"):
        assert [call[0] for call in external_calls] == ["checkout", "provision"]
    else:
        assert [call[0] for call in external_calls] == [
            "checkout",
            "provision",
            "runtime.start",
        ]
    assert all(call[0] != "record_container" for call in persistence.calls)
