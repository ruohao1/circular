from circular.domain import WorkspaceStatus


class InvalidWorkspaceTransition(ValueError):
    def __init__(self, current: WorkspaceStatus, target: WorkspaceStatus) -> None:
        super().__init__(f"workspace cannot transition from {current.value} to {target.value}")
        self.current = current
        self.target = target


class WorkspaceLifecycle:
    """Single authority for deterministic Workspace lifecycle transitions."""

    _transitions: dict[WorkspaceStatus, frozenset[WorkspaceStatus]] = {
        WorkspaceStatus.PENDING: frozenset({WorkspaceStatus.READY, WorkspaceStatus.FAILED}),
        WorkspaceStatus.READY: frozenset({WorkspaceStatus.RELEASED, WorkspaceStatus.FAILED}),
        WorkspaceStatus.RELEASED: frozenset(),
        WorkspaceStatus.FAILED: frozenset({WorkspaceStatus.RELEASED}),
    }

    @classmethod
    def allowed_targets(cls, current: WorkspaceStatus) -> frozenset[WorkspaceStatus]:
        return cls._transitions[current]

    @classmethod
    def validate(cls, current: WorkspaceStatus, target: WorkspaceStatus) -> None:
        if target not in cls.allowed_targets(current):
            raise InvalidWorkspaceTransition(current, target)

    @classmethod
    def is_terminal(cls, status: WorkspaceStatus) -> bool:
        return not cls.allowed_targets(status)
