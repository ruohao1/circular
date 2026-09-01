from circular.domain import RunStatus


class InvalidRunTransition(ValueError):
    def __init__(self, current: RunStatus, target: RunStatus) -> None:
        super().__init__(f"run cannot transition from {current.value} to {target.value}")
        self.current = current
        self.target = target


class RunLifecycle:
    """Single authority for deterministic Run lifecycle transitions."""

    _transitions: dict[RunStatus, frozenset[RunStatus]] = {
        RunStatus.QUEUED: frozenset({RunStatus.PROVISIONING, RunStatus.CANCELLED}),
        RunStatus.PROVISIONING: frozenset(
            {RunStatus.RUNNING, RunStatus.FAILED, RunStatus.CANCELLED}
        ),
        RunStatus.RUNNING: frozenset(
            {
                RunStatus.WAITING_FOR_APPROVAL,
                RunStatus.WAITING_FOR_INPUT,
                RunStatus.FINALIZING,
                RunStatus.FAILED,
                RunStatus.CANCELLED,
            }
        ),
        RunStatus.WAITING_FOR_APPROVAL: frozenset(
            {RunStatus.RUNNING, RunStatus.FAILED, RunStatus.CANCELLED}
        ),
        RunStatus.WAITING_FOR_INPUT: frozenset(
            {RunStatus.RUNNING, RunStatus.FAILED, RunStatus.CANCELLED}
        ),
        RunStatus.FINALIZING: frozenset(
            {RunStatus.SUCCEEDED, RunStatus.FAILED, RunStatus.CANCELLED}
        ),
        RunStatus.SUCCEEDED: frozenset(),
        RunStatus.FAILED: frozenset(),
        RunStatus.CANCELLED: frozenset(),
    }

    @classmethod
    def allowed_targets(cls, current: RunStatus) -> frozenset[RunStatus]:
        return cls._transitions[current]

    @classmethod
    def validate(cls, current: RunStatus, target: RunStatus) -> None:
        if target not in cls.allowed_targets(current):
            raise InvalidRunTransition(current, target)

    @classmethod
    def is_terminal(cls, status: RunStatus) -> bool:
        return not cls.allowed_targets(status)
