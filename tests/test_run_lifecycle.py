import pytest
from circular.domain import RunStatus
from circular.orchestration import InvalidRunTransition, RunLifecycle


@pytest.mark.parametrize(
    ("current", "target"),
    [
        (RunStatus.QUEUED, RunStatus.PROVISIONING),
        (RunStatus.PROVISIONING, RunStatus.RUNNING),
        (RunStatus.RUNNING, RunStatus.WAITING_FOR_APPROVAL),
        (RunStatus.WAITING_FOR_APPROVAL, RunStatus.RUNNING),
        (RunStatus.RUNNING, RunStatus.WAITING_FOR_INPUT),
        (RunStatus.WAITING_FOR_INPUT, RunStatus.RUNNING),
        (RunStatus.RUNNING, RunStatus.FINALIZING),
        (RunStatus.FINALIZING, RunStatus.SUCCEEDED),
    ],
)
def test_valid_lifecycle_paths(current: RunStatus, target: RunStatus) -> None:
    RunLifecycle.validate(current, target)


@pytest.mark.parametrize("terminal", [RunStatus.SUCCEEDED, RunStatus.FAILED, RunStatus.CANCELLED])
def test_terminal_states_cannot_transition(terminal: RunStatus) -> None:
    assert RunLifecycle.is_terminal(terminal)
    with pytest.raises(InvalidRunTransition):
        RunLifecycle.validate(terminal, RunStatus.RUNNING)


def test_run_cannot_skip_provisioning() -> None:
    with pytest.raises(InvalidRunTransition, match="queued to running"):
        RunLifecycle.validate(RunStatus.QUEUED, RunStatus.RUNNING)


def test_cancellation_is_allowed_from_every_non_terminal_state() -> None:
    non_terminal = set(RunStatus) - {
        RunStatus.SUCCEEDED,
        RunStatus.FAILED,
        RunStatus.CANCELLED,
    }
    for state in non_terminal:
        RunLifecycle.validate(state, RunStatus.CANCELLED)
