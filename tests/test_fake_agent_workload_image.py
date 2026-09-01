import json
import os
import subprocess
from pathlib import Path

import pytest
from test_fake_agent_workload import _run_workload, _valid_input

pytestmark = pytest.mark.skipif(
    os.getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1",
    reason="CIRCULAR_RUN_DOCKER_TESTS is not set to 1",
)

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "circular-fake-agent-workload:test"


def _docker(*arguments: str, input_text: str | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["docker", *arguments],
        cwd=ROOT,
        input=input_text,
        capture_output=True,
        check=False,
        text=True,
    )


def test_image_contains_only_the_workload_and_runs_without_host_mounts() -> None:
    build = _docker(
        "build",
        "--file",
        "infra/fake-agent-workload.Dockerfile",
        "--tag",
        IMAGE,
        ".",
    )
    assert build.returncode == 0, build.stderr

    probe = _docker(
        "run",
        "--rm",
        "--network",
        "none",
        "--read-only",
        "--cap-drop",
        "ALL",
        "--security-opt",
        "no-new-privileges",
        "--entrypoint",
        "python",
        IMAGE,
        "-c",
        (
            "from pathlib import Path; import os; "
            "assert Path('/opt/circular-workload/src/circular/fake_agent_workload').is_dir(); "
            "assert not Path('/app/apps').exists(); "
            "assert not Path('/app/packages').exists(); "
            "assert 'DATABASE_URL' not in os.environ; "
            "assert not any(k.startswith('CIRCULAR_PLATFORM_') for k in os.environ)"
        ),
    )
    assert probe.returncode == 0, probe.stderr

    container_name = f"circular-fake-agent-workload-test-{os.getpid()}"
    create = _docker(
        "create",
        "--name",
        container_name,
        "--network",
        "none",
        "--read-only",
        "--cap-drop",
        "ALL",
        "--security-opt",
        "no-new-privileges",
        "--interactive",
        IMAGE,
    )
    assert create.returncode == 0, create.stderr
    try:
        mounts = _docker("inspect", "--format", "{{json .Mounts}}", container_name)
        assert mounts.returncode == 0, mounts.stderr
        assert json.loads(mounts.stdout) == []

        document = _valid_input()
        expected = _run_workload(document)
        actual = _docker(
            "start",
            "--attach",
            "--interactive",
            container_name,
            input_text=json.dumps(document),
        )

        state = _docker("inspect", "--format", "{{.State.ExitCode}}", container_name)
        assert state.returncode == 0, state.stderr
        assert int(state.stdout) == expected.returncode
        assert actual.stdout == expected.stdout
        assert actual.stderr == expected.stderr
    finally:
        _docker("rm", "--force", container_name)
