import asyncio
import json
import os
import subprocess
from pathlib import Path
from uuid import UUID, uuid4

import pytest
from circular.runtimes import ContainerSpec, DockerRuntime, OutputStream, RuntimeResult

pytestmark = pytest.mark.skipif(
    os.getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1",
    reason="CIRCULAR_RUN_DOCKER_TESTS is not set to 1",
)

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "circular-fake-agent-workload:runtime-test"


def _docker(*arguments: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["docker", *arguments],
        cwd=ROOT,
        capture_output=True,
        check=False,
        text=True,
        timeout=120,
    )


@pytest.fixture(scope="module", autouse=True)
def _build_fake_workload_image() -> None:
    build = _docker(
        "build",
        "--file",
        "infra/fake-agent-workload.Dockerfile",
        "--tag",
        IMAGE,
        ".",
    )
    assert build.returncode == 0, "fake workload image build failed"


def _workload_document(run_id: UUID, *, delay_ms: int) -> dict[str, object]:
    return {
        "protocol_version": 1,
        "run": {
            "id": str(run_id),
            "task_title": "Exercise Docker runtime",
            "task_description": "Verify the isolation contract.",
            "instructions": "Emit deterministic events.",
        },
        "behavior": {"delay_ms": delay_ms, "failure": "none"},
    }


def _remove_owned_container(container_name: str, run_id: UUID) -> None:
    inspect = _docker("inspect", container_name)
    if inspect.returncode != 0:
        return
    container = json.loads(inspect.stdout)[0]
    labels = container["Config"]["Labels"]
    if labels.get("io.circular.managed") == "true" and labels.get("io.circular.run_id") == str(
        run_id
    ):
        _docker("rm", "--force", container["Id"])


async def test_real_docker_runtime_runs_workload_and_enforces_inspected_policy(
    tmp_path: Path,
) -> None:
    run_id = uuid4()
    worktree_root = tmp_path / "worktrees"
    worktree = worktree_root / str(run_id)
    worktree.mkdir(parents=True)
    document = _workload_document(run_id, delay_ms=0)
    runtime = DockerRuntime(worktree_root)
    plan = runtime.resolve(
        ContainerSpec(
            run_id=run_id,
            image=IMAGE,
            worktree=worktree,
            command=(),
            stdin=json.dumps(document, separators=(",", ":")).encode("utf-8"),
            cpu_limit=1.0,
            memory_limit_mb=256,
        )
    )

    try:
        handle = await runtime.start(
            ContainerSpec(
                run_id=run_id,
                image=IMAGE,
                worktree=worktree,
                command=(),
                stdin=json.dumps(document, separators=(",", ":")).encode("utf-8"),
                cpu_limit=1.0,
                memory_limit_mb=256,
            )
        )
        result = await runtime.wait(handle)
        output = [chunk async for chunk in runtime.output(handle)]

        assert result == RuntimeResult.exited(0)
        assert b"".join(
            chunk.data for chunk in output if chunk.stream is OutputStream.STDOUT
        ).splitlines() == [
            (
                b'{"data":{"delta":"Fake container workload completed: "},'
                + f'"protocol_version":1,"run_id":"{run_id}",'.encode()
                + b'"source":"fake-container-workload","type":"agent.message.delta"}'
            ),
            (
                b'{"data":{"delta":"Exercise Docker runtime"},'
                + f'"protocol_version":1,"run_id":"{run_id}",'.encode()
                + b'"source":"fake-container-workload","type":"agent.message.delta"}'
            ),
            (
                b'{"data":{"content":"Fake container workload completed: '
                b'Exercise Docker runtime"},'
                + f'"protocol_version":1,"run_id":"{run_id}",'.encode()
                + b'"source":"fake-container-workload",'
                b'"type":"agent.message.completed"}'
            ),
            (
                b'{"data":{"input_tokens":10,"output_tokens":7},'
                + f'"protocol_version":1,"run_id":"{run_id}",'.encode()
                + b'"source":"fake-container-workload","type":"usage.updated"}'
            ),
        ]
        assert all(chunk.stream is not OutputStream.STDERR for chunk in output)

        inspect = _docker("inspect", plan.container_name)
        assert inspect.returncode == 0, "Docker inspect failed"
        container = json.loads(inspect.stdout)[0]
        mounts = container["Mounts"]
        assert len(mounts) == 1
        assert mounts[0]["Type"] == "bind"
        assert mounts[0]["Source"] == str(worktree)
        assert mounts[0]["Destination"] == "/workspace"
        assert mounts[0]["RW"] is True
        assert "docker.sock" not in mounts[0]["Source"]
        assert "/.ssh" not in mounts[0]["Source"]

        host_config = container["HostConfig"]
        assert host_config["NetworkMode"] == "none"
        assert host_config["ReadonlyRootfs"] is True
        assert host_config["CapDrop"] == ["ALL"]
        assert any(option.startswith("no-new-privileges") for option in host_config["SecurityOpt"])
        assert host_config["NanoCpus"] == 1_000_000_000
        assert host_config["Memory"] == 256 * 1024 * 1024
        assert container["Config"]["User"] == "65532:65532"
        assert container["Config"]["WorkingDir"] == "/workspace"
        assert container["Config"]["Labels"] == dict(plan.labels)

        environment_names = {entry.partition("=")[0] for entry in container["Config"]["Env"]}
        assert "DATABASE_URL" not in environment_names
        assert "SSH_AUTH_SOCK" not in environment_names
        assert not any(name.startswith("CIRCULAR_PLATFORM_") for name in environment_names)
    finally:
        _remove_owned_container(plan.container_name, run_id)


async def test_real_docker_runtime_preserves_a_fast_nonzero_exit_code(
    tmp_path: Path,
) -> None:
    run_id = uuid4()
    worktree_root = tmp_path / "invalid-input-worktrees"
    worktree = worktree_root / str(run_id)
    worktree.mkdir(parents=True)
    runtime = DockerRuntime(worktree_root)
    spec = ContainerSpec(
        run_id=run_id,
        image=IMAGE,
        worktree=worktree,
        command=(),
        stdin=b"not-json",
        cpu_limit=1.0,
        memory_limit_mb=256,
    )
    plan = runtime.resolve(spec)

    try:
        handle = await runtime.start(spec)
        result = await runtime.wait(handle)
        output = [chunk async for chunk in runtime.output(handle)]

        assert result == RuntimeResult.exited(2)
        assert b"invalid_input" in b"".join(
            chunk.data for chunk in output if chunk.stream is OutputStream.STDERR
        )
        inspect = _docker("inspect", plan.container_name)
        assert inspect.returncode == 0, "Docker inspect failed"
        assert json.loads(inspect.stdout)[0]["State"]["ExitCode"] == 2
    finally:
        _remove_owned_container(plan.container_name, run_id)


async def test_real_docker_runtime_can_stop_immediately_after_start(
    tmp_path: Path,
) -> None:
    async with asyncio.timeout(45):
        for index in range(3):
            run_id = uuid4()
            worktree_root = tmp_path / f"immediate-stop-{index}"
            worktree = worktree_root / str(run_id)
            worktree.mkdir(parents=True)
            runtime = DockerRuntime(worktree_root, stop_timeout_seconds=1)
            spec = ContainerSpec(
                run_id=run_id,
                image=IMAGE,
                worktree=worktree,
                command=(),
                stdin=json.dumps(
                    _workload_document(run_id, delay_ms=10_000),
                    separators=(",", ":"),
                ).encode("utf-8"),
                cpu_limit=1.0,
                memory_limit_mb=256,
            )
            plan = runtime.resolve(spec)
            try:
                handle = await runtime.start(spec)
                await runtime.stop(handle)
                assert await runtime.wait(handle) == RuntimeResult.stopped()
            finally:
                _remove_owned_container(plan.container_name, run_id)
