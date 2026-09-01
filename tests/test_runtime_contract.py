import asyncio
from abc import ABC, abstractmethod
from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from pathlib import Path

from circular.runtimes import (
    CompletionReason,
    ContainerHandle,
    ContainerSpec,
    OutputStream,
    Runtime,
    RuntimeOutput,
    RuntimeResult,
)


@dataclass(frozen=True, slots=True)
class RuntimeCase:
    runtime: Runtime
    spec: ContainerSpec


class RuntimeContract(ABC):
    """Adapter-independent behavior every runtime implementation must satisfy."""

    @abstractmethod
    def make_case(
        self,
        *,
        output: tuple[RuntimeOutput, ...] = (),
        exit_code: int = 0,
        waits_for_stop: bool = False,
    ) -> RuntimeCase: ...

    async def test_preserves_order_across_stdout_and_stderr(self) -> None:
        expected = (
            RuntimeOutput(OutputStream.STDOUT, b"first\n"),
            RuntimeOutput(OutputStream.STDERR, b"warning\n"),
            RuntimeOutput(OutputStream.STDOUT, b"last\n"),
        )
        case = self.make_case(output=expected)

        handle = await case.runtime.start(case.spec)
        actual = [chunk async for chunk in case.runtime.output(handle)]

        assert actual == list(expected)
        assert await case.runtime.wait(handle) == RuntimeResult.exited(0)

    async def test_returns_a_nonzero_exit_code(self) -> None:
        case = self.make_case(exit_code=23)

        handle = await case.runtime.start(case.spec)

        assert await case.runtime.wait(handle) == RuntimeResult.exited(23)

    async def test_stop_cancels_a_running_execution(self) -> None:
        case = self.make_case(waits_for_stop=True)
        handle = await case.runtime.start(case.spec)
        output_task = asyncio.create_task(_collect(case.runtime.output(handle)))
        result_task = asyncio.create_task(case.runtime.wait(handle))
        await asyncio.sleep(0)

        async with asyncio.timeout(1):
            await case.runtime.stop(handle)
            assert await output_task == []
            assert await result_task == RuntimeResult.stopped()

    async def test_stop_is_idempotent_and_wait_is_stable(self) -> None:
        case = self.make_case(waits_for_stop=True)
        handle = await case.runtime.start(case.spec)

        async with asyncio.timeout(1):
            await case.runtime.stop(handle)
            await case.runtime.stop(handle)
            first_result = await case.runtime.wait(handle)
            await case.runtime.stop(handle)

        assert first_result == RuntimeResult.stopped()
        async with asyncio.timeout(1):
            assert await case.runtime.wait(handle) == first_result

    async def test_stop_after_natural_completion_is_a_noop(self) -> None:
        case = self.make_case(exit_code=7)
        handle = await case.runtime.start(case.spec)
        result = await case.runtime.wait(handle)

        await case.runtime.stop(handle)
        await case.runtime.stop(handle)

        assert await case.runtime.wait(handle) == result


async def _collect(output: AsyncIterator[RuntimeOutput]) -> list[RuntimeOutput]:
    return [chunk async for chunk in output]


@dataclass(frozen=True, slots=True)
class _FakePlan:
    output: tuple[RuntimeOutput, ...]
    exit_code: int
    waits_for_stop: bool


@dataclass(slots=True)
class _FakeExecution:
    plan: _FakePlan
    completed: asyncio.Event = field(default_factory=asyncio.Event)
    result: RuntimeResult | None = None


class _FakeRuntime:
    """Deterministic in-memory adapter used to exercise the runtime contract."""

    def __init__(self, plan: _FakePlan) -> None:
        self._plan = plan
        self._next_id = 1
        self._executions: dict[str, _FakeExecution] = {}

    async def start(self, spec: ContainerSpec) -> ContainerHandle:
        del spec
        handle = ContainerHandle(id=f"fake:{self._next_id}")
        self._next_id += 1
        execution = _FakeExecution(plan=self._plan)
        if not self._plan.waits_for_stop:
            execution.result = RuntimeResult.exited(self._plan.exit_code)
            execution.completed.set()
        self._executions[handle.id] = execution
        return handle

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        execution = self._executions[handle.id]
        for chunk in execution.plan.output:
            if execution.result == RuntimeResult.stopped():
                return
            yield chunk
        await execution.completed.wait()

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        execution = self._executions[handle.id]
        await execution.completed.wait()
        if execution.result is None:
            raise AssertionError("completed fake execution has no result")
        return execution.result

    async def stop(self, handle: ContainerHandle) -> None:
        execution = self._executions[handle.id]
        if execution.result is not None:
            return
        execution.result = RuntimeResult.stopped()
        execution.completed.set()


class TestFakeRuntime(RuntimeContract):
    def make_case(
        self,
        *,
        output: tuple[RuntimeOutput, ...] = (),
        exit_code: int = 0,
        waits_for_stop: bool = False,
    ) -> RuntimeCase:
        runtime = _FakeRuntime(
            _FakePlan(output=output, exit_code=exit_code, waits_for_stop=waits_for_stop)
        )
        assert isinstance(runtime, Runtime)
        return RuntimeCase(
            runtime=runtime,
            spec=ContainerSpec(
                image="runtime-contract",
                worktree=Path("/workspace"),
                command=("contract-command",),
            ),
        )


def test_runtime_result_rejects_ambiguous_completion_states() -> None:
    for reason, exit_code in (
        (CompletionReason.EXITED, None),
        (CompletionReason.STOPPED, 137),
    ):
        try:
            RuntimeResult(reason=reason, exit_code=exit_code)
        except ValueError:
            pass
        else:
            raise AssertionError("invalid runtime result was accepted")
