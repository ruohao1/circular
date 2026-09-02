from contextlib import asynccontextmanager
from copy import deepcopy
from dataclasses import dataclass, field, replace
from typing import Any
from uuid import UUID

import pytest
from circular.domain import RunStatus, Workspace, WorkspaceStatus
from circular.events import EventEnvelope, EventType
from circular.runners import (
    SqlWorkspaceProvisioningPersistence,
    WorkspaceProvisioningConflict,
)
from circular.storage import WorkspaceAlreadyExistsError

RUN_ID = UUID("00000000-0000-4000-8000-000000000169")
WORKSPACE_ID = UUID("00000000-0000-4000-8000-000000000269")


@dataclass
class TransactionalState:
    workspace: Workspace = field(
        default_factory=lambda: Workspace(
            id=WORKSPACE_ID,
            run_id=RUN_ID,
            worktree_path="/worktrees/run-169",
            container_id="container-169",
        )
    )
    run_status: RunStatus = RunStatus.PROVISIONING
    events: list[tuple[str, dict[str, Any]]] = field(
        default_factory=lambda: [
            ("workspace.provisioning", {"status": "pending"}),
            (
                "workspace.provisioning",
                {"status": "pending", "stage": "container_started"},
            ),
        ]
    )


class TransactionalSessions:
    def __init__(self, state: TransactionalState) -> None:
        self.state = state

    @asynccontextmanager
    async def begin(self):
        snapshot = deepcopy(self.state)
        try:
            yield object()
        except BaseException:
            self.state.workspace = snapshot.workspace
            self.state.run_status = snapshot.run_status
            self.state.events = snapshot.events
            raise


class InjectedFailure(RuntimeError):
    pass


class RecordingWorkspaceStore:
    def __init__(self, state: TransactionalState, fail_at: str | None = None) -> None:
        self.state = state
        self.fail_at = fail_at
        self.create_called = False

    async def create(
        self,
        session: object,
        workspace: Workspace,
        *,
        source: str,
    ) -> Workspace:
        self.create_called = True
        self.state.workspace = workspace
        self.state.events.append(("workspace.provisioning", {"status": "pending"}))
        return workspace

    async def load_for_run(self, session: object, run_id: UUID) -> Workspace:
        assert run_id == RUN_ID
        return self.state.workspace

    async def load(self, session: object, workspace_id: UUID) -> Workspace:
        assert workspace_id == WORKSPACE_ID
        return self.state.workspace

    async def record_container(
        self,
        session: object,
        workspace_id: UUID,
        container_id: str,
        *,
        source: str,
    ) -> Workspace:
        if self.fail_at == "container_event":
            raise InjectedFailure("container_event failed")
        assert workspace_id == WORKSPACE_ID
        self.state.workspace = replace(self.state.workspace, container_id=container_id)
        self.state.events.append(
            (
                "workspace.provisioning",
                {
                    "status": "pending",
                    "stage": "container_started",
                    "container_id": container_id,
                },
            )
        )
        return self.state.workspace

    async def transition(
        self,
        session: object,
        workspace_id: UUID,
        target: WorkspaceStatus,
        *,
        source: str,
        container_id: str | None = None,
    ) -> Workspace:
        step = "workspace_ready" if target is WorkspaceStatus.READY else "workspace_failed"
        if self.fail_at == step:
            raise InjectedFailure(f"{step} failed")
        assert workspace_id == WORKSPACE_ID
        self.state.workspace = replace(
            self.state.workspace,
            status=target,
            container_id=container_id or self.state.workspace.container_id,
        )
        self.state.events.append(
            (
                f"workspace.{target.value}",
                {
                    "status": target.value,
                    "container_id": self.state.workspace.container_id,
                },
            )
        )
        return self.state.workspace


class RecordingRunStore:
    def __init__(self, state: TransactionalState, fail_at: str | None = None) -> None:
        self.state = state
        self.fail_at = fail_at

    async def require_status(
        self,
        session: object,
        run_id: UUID,
        expected: RunStatus,
    ) -> object:
        assert run_id == RUN_ID
        if self.state.run_status is not expected:
            raise ValueError("unexpected run status")
        return object()

    async def transition(
        self,
        session: object,
        run_id: UUID,
        target: RunStatus,
        *,
        error: str | None = None,
    ) -> object:
        step = "run_running" if target is RunStatus.RUNNING else "run_failed"
        if self.fail_at == step:
            raise InjectedFailure(f"{step} failed")
        assert run_id == RUN_ID
        self.state.run_status = target
        return object()

    async def append_event(self, session: object, envelope: EventEnvelope) -> object:
        step = "run_started" if envelope.type is EventType.RUN_STARTED else "run_failed_event"
        if self.fail_at == step:
            raise InjectedFailure(f"{step} failed")
        self.state.events.append((envelope.type.value, envelope.data))
        return object()


def _persistence(
    state: TransactionalState,
    *,
    fail_at: str | None = None,
) -> SqlWorkspaceProvisioningPersistence:
    return SqlWorkspaceProvisioningPersistence(
        TransactionalSessions(state),  # type: ignore[arg-type]
        RecordingRunStore(state, fail_at),  # type: ignore[arg-type]
        RecordingWorkspaceStore(state, fail_at),  # type: ignore[arg-type]
        source="test-worker",
    )


class ExistingWorkspaceStore(RecordingWorkspaceStore):
    async def create(
        self,
        session: object,
        workspace: Workspace,
        *,
        source: str,
    ) -> Workspace:
        raise WorkspaceAlreadyExistsError(workspace.run_id)


def _persistence_with_existing_workspace(
    state: TransactionalState,
) -> SqlWorkspaceProvisioningPersistence:
    return SqlWorkspaceProvisioningPersistence(
        TransactionalSessions(state),  # type: ignore[arg-type]
        RecordingRunStore(state),  # type: ignore[arg-type]
        ExistingWorkspaceStore(state),  # type: ignore[arg-type]
        source="test-worker",
    )


async def test_repeated_pending_workspace_creation_reuses_identical_durable_state() -> None:
    state = TransactionalState(
        workspace=Workspace(
            id=WORKSPACE_ID,
            run_id=RUN_ID,
            worktree_path="/worktrees/run-169",
        ),
        events=[("workspace.provisioning", {"status": "pending"})],
    )

    workspace = await _persistence_with_existing_workspace(state).create_pending(state.workspace)

    assert workspace == state.workspace
    assert state.events == [("workspace.provisioning", {"status": "pending"})]


async def test_repeated_creation_rejects_workspace_with_allocated_container() -> None:
    state = TransactionalState()
    requested = replace(state.workspace, container_id=None)

    with pytest.raises(WorkspaceProvisioningConflict, match="incompatible Workspace state"):
        await _persistence_with_existing_workspace(state).create_pending(requested)


async def test_pending_workspace_is_not_created_after_run_status_changes() -> None:
    state = TransactionalState(run_status=RunStatus.CANCELLED, events=[])
    workspace_store = RecordingWorkspaceStore(state)
    persistence = SqlWorkspaceProvisioningPersistence(
        TransactionalSessions(state),  # type: ignore[arg-type]
        RecordingRunStore(state),  # type: ignore[arg-type]
        workspace_store,  # type: ignore[arg-type]
        source="test-worker",
    )
    requested = replace(state.workspace, container_id=None)

    with pytest.raises(ValueError, match="unexpected run status"):
        await persistence.create_pending(requested)

    assert workspace_store.create_called is False
    assert state.events == []


async def test_ready_workspace_and_running_run_commit_with_events() -> None:
    state = TransactionalState()

    workspace = await _persistence(state).mark_ready_and_running(WORKSPACE_ID, "fake")

    assert workspace.status is WorkspaceStatus.READY
    assert workspace.container_id == "container-169"
    assert state.run_status is RunStatus.RUNNING
    assert [event_type for event_type, _data in state.events] == [
        "workspace.provisioning",
        "workspace.provisioning",
        "workspace.ready",
        "run.started",
    ]
    assert state.events[-1] == ("run.started", {"backend": "fake"})


@pytest.mark.parametrize("fail_at", ["workspace_ready", "run_running", "run_started"])
async def test_ready_and_running_roll_back_together_when_a_final_step_fails(
    fail_at: str,
) -> None:
    state = TransactionalState()

    with pytest.raises(InjectedFailure, match=fail_at):
        await _persistence(state, fail_at=fail_at).mark_ready_and_running(
            WORKSPACE_ID,
            "fake",
        )

    assert state.workspace.status is WorkspaceStatus.PENDING
    assert state.workspace.container_id == "container-169"
    assert state.run_status is RunStatus.PROVISIONING
    assert [event_type for event_type, _data in state.events] == [
        "workspace.provisioning",
        "workspace.provisioning",
    ]


async def test_failure_transaction_records_late_container_and_both_failures() -> None:
    state = TransactionalState(
        workspace=Workspace(
            id=WORKSPACE_ID,
            run_id=RUN_ID,
            worktree_path="/worktrees/run-169",
        ),
        events=[("workspace.provisioning", {"status": "pending"})],
    )

    await _persistence(state).mark_failed(
        RUN_ID,
        RuntimeError("runtime persistence failed"),
        container_id="container-late-169",
    )

    assert state.run_status is RunStatus.FAILED
    assert state.workspace.status is WorkspaceStatus.FAILED
    assert state.workspace.container_id == "container-late-169"
    assert [event_type for event_type, _data in state.events] == [
        "workspace.provisioning",
        "workspace.provisioning",
        "workspace.failed",
        "run.failed",
    ]
    assert state.events[-1] == (
        "run.failed",
        {"error": "runtime persistence failed"},
    )


@pytest.mark.parametrize(
    "fail_at",
    ["run_failed", "container_event", "workspace_failed", "run_failed_event"],
)
async def test_failure_state_and_events_roll_back_as_one_transaction(fail_at: str) -> None:
    state = TransactionalState(
        workspace=Workspace(
            id=WORKSPACE_ID,
            run_id=RUN_ID,
            worktree_path="/worktrees/run-169",
        ),
        events=[("workspace.provisioning", {"status": "pending"})],
    )

    with pytest.raises(InjectedFailure, match=fail_at):
        await _persistence(state, fail_at=fail_at).mark_failed(
            RUN_ID,
            RuntimeError("primary"),
            container_id="container-late-169",
        )

    assert state.run_status is RunStatus.PROVISIONING
    assert state.workspace.status is WorkspaceStatus.PENDING
    assert state.workspace.container_id is None
    assert state.events == [("workspace.provisioning", {"status": "pending"})]


async def test_provisioning_failure_cannot_overwrite_a_run_that_already_advanced() -> None:
    state = TransactionalState(run_status=RunStatus.RUNNING)

    with pytest.raises(ValueError, match="unexpected run status"):
        await _persistence(state).mark_failed(
            RUN_ID,
            RuntimeError("stale provisioning attempt"),
            container_id="container-169",
        )

    assert state.run_status is RunStatus.RUNNING
    assert state.workspace.status is WorkspaceStatus.PENDING
    assert [event_type for event_type, _data in state.events] == [
        "workspace.provisioning",
        "workspace.provisioning",
    ]
