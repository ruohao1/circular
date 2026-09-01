from circular.events import EventType


def test_workspace_provisioning_event_name_is_normalized() -> None:
    assert EventType.WORKSPACE_PROVISIONING == "workspace.provisioning"


def test_workspace_ready_event_name_is_normalized() -> None:
    assert EventType.WORKSPACE_READY == "workspace.ready"


def test_workspace_released_event_name_is_normalized() -> None:
    assert EventType.WORKSPACE_RELEASED == "workspace.released"


def test_workspace_failed_event_name_is_normalized() -> None:
    assert EventType.WORKSPACE_FAILED == "workspace.failed"
