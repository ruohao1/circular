import pytest
from circular.domain import WorkspaceStatus
from circular.orchestration import (
    InvalidWorkspaceInitialContainer,
    InvalidWorkspaceInitialStatus,
    InvalidWorkspaceTransition,
    WorkspaceLifecycle,
)


def test_pending_is_the_only_valid_initial_workspace_state() -> None:
    WorkspaceLifecycle.validate_initial(WorkspaceStatus.PENDING)


@pytest.mark.parametrize(
    "status",
    [WorkspaceStatus.READY, WorkspaceStatus.RELEASED, WorkspaceStatus.FAILED],
)
def test_every_other_initial_workspace_state_is_invalid(status: WorkspaceStatus) -> None:
    with pytest.raises(InvalidWorkspaceInitialStatus) as exc_info:
        WorkspaceLifecycle.validate_initial(status)

    assert exc_info.value.status is status


def test_initial_workspace_cannot_already_have_a_container() -> None:
    with pytest.raises(InvalidWorkspaceInitialContainer) as exc_info:
        WorkspaceLifecycle.validate_initial(
            WorkspaceStatus.PENDING,
            container_id="premature-container",
        )

    assert exc_info.value.container_id == "premature-container"


def test_pending_workspace_can_become_ready() -> None:
    WorkspaceLifecycle.validate(WorkspaceStatus.PENDING, WorkspaceStatus.READY)


def test_pending_workspace_can_fail_during_provisioning() -> None:
    WorkspaceLifecycle.validate(WorkspaceStatus.PENDING, WorkspaceStatus.FAILED)


def test_ready_workspace_can_be_released() -> None:
    WorkspaceLifecycle.validate(WorkspaceStatus.READY, WorkspaceStatus.RELEASED)


def test_ready_workspace_can_fail() -> None:
    WorkspaceLifecycle.validate(WorkspaceStatus.READY, WorkspaceStatus.FAILED)


def test_failed_workspace_can_be_released_after_cleanup() -> None:
    WorkspaceLifecycle.validate(WorkspaceStatus.FAILED, WorkspaceStatus.RELEASED)


@pytest.mark.parametrize(
    ("current", "target"),
    [
        (WorkspaceStatus.PENDING, WorkspaceStatus.PENDING),
        (WorkspaceStatus.PENDING, WorkspaceStatus.RELEASED),
        (WorkspaceStatus.READY, WorkspaceStatus.PENDING),
        (WorkspaceStatus.READY, WorkspaceStatus.READY),
        (WorkspaceStatus.RELEASED, WorkspaceStatus.PENDING),
        (WorkspaceStatus.RELEASED, WorkspaceStatus.READY),
        (WorkspaceStatus.RELEASED, WorkspaceStatus.RELEASED),
        (WorkspaceStatus.RELEASED, WorkspaceStatus.FAILED),
        (WorkspaceStatus.FAILED, WorkspaceStatus.PENDING),
        (WorkspaceStatus.FAILED, WorkspaceStatus.READY),
        (WorkspaceStatus.FAILED, WorkspaceStatus.FAILED),
    ],
)
def test_every_other_workspace_transition_is_invalid(
    current: WorkspaceStatus, target: WorkspaceStatus
) -> None:
    with pytest.raises(InvalidWorkspaceTransition) as exc_info:
        WorkspaceLifecycle.validate(current, target)

    assert exc_info.value.current is current
    assert exc_info.value.target is target


@pytest.mark.parametrize(
    ("status", "expected"),
    [
        (WorkspaceStatus.PENDING, False),
        (WorkspaceStatus.READY, False),
        (WorkspaceStatus.RELEASED, True),
        (WorkspaceStatus.FAILED, False),
    ],
)
def test_terminal_behavior_is_explicit(status: WorkspaceStatus, expected: bool) -> None:
    assert WorkspaceLifecycle.is_terminal(status) is expected
