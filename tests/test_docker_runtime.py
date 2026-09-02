import asyncio
import json
import os
import textwrap
from pathlib import Path
from uuid import UUID

import pytest
from circular.runtimes import (
    ContainerDiscardError,
    ContainerHandle,
    ContainerNameConflictError,
    ContainerOutputAlreadyConsumedError,
    ContainerSpec,
    ContainerStartError,
    DockerOperationError,
    DockerRuntime,
    InvalidContainerSpec,
    InvalidDockerConfiguration,
    OutputStream,
    Runtime,
    RuntimeOutput,
    RuntimeResult,
    UnknownContainerHandleError,
)

RUN_ID = UUID("00000000-0000-4000-8000-000000000171")
CONTAINER_ID = "a" * 64


def _worktree(tmp_path: Path) -> tuple[Path, Path]:
    root = tmp_path / "worktrees"
    child = root / str(RUN_ID)
    child.mkdir(parents=True)
    return root, child


def _spec(worktree: Path, **overrides: object) -> ContainerSpec:
    values = {
        "run_id": RUN_ID,
        "image": "circular-fake-agent-workload:test",
        "worktree": worktree,
        "command": (),
        "stdin": b'{"protocol_version":1}\n',
        "cpu_limit": 1.5,
        "memory_limit_mb": 384,
    }
    values.update(overrides)
    return ContainerSpec(**values)  # type: ignore[arg-type]


def _fake_docker(
    tmp_path: Path,
    *,
    waits_for_stop: bool = False,
    create_delay: float = 0,
    start_delay: float = 0,
    stop_delay: float = 0,
    attachment_fails: bool = False,
    inspect_fails_after_ready: bool = False,
    replace_name_on_create: bool = False,
    unexpected_mount: bool = False,
    invalid_create_id: bool = False,
    create_hangs_after_creation: bool = False,
    remove_fails: bool = False,
    policy_mismatch: str | None = None,
    create_returns_failure: bool = False,
    reconciliation_unavailable: bool = False,
    create_late_commit_delay: float = 0,
    remove_delay: float = 0,
    create_hangs_without_commit_once: bool = False,
    owned_nonce_with_foreign_name: bool = False,
) -> tuple[Path, Path]:
    state = tmp_path / "fake-docker-state"
    state.mkdir()
    executable = tmp_path / "fake-docker"
    executable.write_text(
        textwrap.dedent(
            f"""\
            #!{os.sys.executable}
            import json
            import os
            import subprocess
            import sys
            import time
            from pathlib import Path

            state = Path({str(state)!r})
            waits_for_stop = {waits_for_stop!r}
            create_delay = {create_delay!r}
            start_delay = {start_delay!r}
            stop_delay = {stop_delay!r}
            attachment_fails = {attachment_fails!r}
            inspect_fails_after_ready = {inspect_fails_after_ready!r}
            replace_name_on_create = {replace_name_on_create!r}
            unexpected_mount = {unexpected_mount!r}
            invalid_create_id = {invalid_create_id!r}
            create_hangs_after_creation = {create_hangs_after_creation!r}
            remove_fails = {remove_fails!r}
            policy_mismatch = {policy_mismatch!r}
            create_returns_failure = {create_returns_failure!r}
            reconciliation_unavailable = {reconciliation_unavailable!r}
            create_late_commit_delay = {create_late_commit_delay!r}
            remove_delay = {remove_delay!r}
            create_hangs_without_commit_once = {create_hangs_without_commit_once!r}
            owned_nonce_with_foreign_name = {owned_nonce_with_foreign_name!r}
            container_id = {CONTAINER_ID!r}
            operation = sys.argv[1]
            with (state / "calls.jsonl").open("a") as calls:
                calls.write(json.dumps({{"argv": sys.argv[1:]}}) + "\\n")

            if operation == "create":
                if (state / "created").exists():
                    raise SystemExit(17)
                (state / "create-started").touch()
                if (
                    create_hangs_without_commit_once
                    and not (state / "create-hung-once").exists()
                ):
                    (state / "create-hung-once").touch()
                    time.sleep(create_delay)
                    raise SystemExit(19)
                if create_late_commit_delay:
                    payload = json.dumps({{
                        "argv": sys.argv[1:],
                        "delay": create_late_commit_delay,
                        "state": str(state),
                    }})
                    code = (
                        "import json,sys,time; from pathlib import Path; "
                        "p=json.loads(sys.argv[1]); time.sleep(p['delay']); "
                        "s=Path(p['state']); (s/'created').touch(); "
                        "(s/'create-argv.json').write_text(json.dumps(p['argv']))"
                    )
                    subprocess.Popen(
                        [sys.executable, "-c", code, payload],
                        stdin=subprocess.DEVNULL,
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                        start_new_session=True,
                    )
                    time.sleep(create_delay)
                    raise SystemExit(19)
                if not create_hangs_after_creation and not create_hangs_without_commit_once:
                    time.sleep(create_delay)
                (state / "created").touch()
                if replace_name_on_create:
                    (state / "replacement").touch()
                if owned_nonce_with_foreign_name:
                    (state / "owned-with-foreign-name").touch()
                (state / "create-environment.json").write_text(json.dumps(dict(os.environ)))
                (state / "create-argv.json").write_text(json.dumps(sys.argv[1:]))
                if create_hangs_after_creation:
                    time.sleep(create_delay)
                if create_returns_failure:
                    raise SystemExit(17)
                print("not-a-container-id" if invalid_create_id else container_id)
            elif operation in {{"inspect", "container"}}:
                if "{{{{.State.Status}}}} {{{{.State.ExitCode}}}}" in sys.argv:
                    inspect_count_path = state / "inspect-count"
                    inspect_count = (
                        int(inspect_count_path.read_text())
                        if inspect_count_path.exists()
                        else 0
                    ) + 1
                    inspect_count_path.write_text(str(inspect_count))
                    if inspect_fails_after_ready and (state / "ready-inspected").exists():
                        raise SystemExit(1)
                    if (state / "exit-code").exists():
                        print("exited " + (state / "exit-code").read_text())
                    elif (state / "start-attached").exists():
                        print("running 0")
                        if inspect_fails_after_ready:
                            (state / "ready-inspected").touch()
                    else:
                        print("created 0")
                elif len(sys.argv) > 3 and sys.argv[1:3] == ["container", "ls"]:
                    if (
                        reconciliation_unavailable
                        and not (state / "reconciliation-available").exists()
                    ):
                        raise SystemExit(1)
                    if (state / "created").exists():
                        filter_value = sys.argv[sys.argv.index("--filter") + 1]
                        has_foreign_name = (
                            (state / "replacement").exists()
                            or (state / "owned-with-foreign-name").exists()
                        )
                        if filter_value.startswith("label="):
                            if not (state / "replacement").exists():
                                print(container_id)
                        else:
                            print("b" * 64 if has_foreign_name else container_id)
                    raise SystemExit(0)
                elif len(sys.argv) == 4 and sys.argv[1:3] == ["container", "inspect"]:
                    if (
                        reconciliation_unavailable
                        and sys.argv[-1].startswith("circular-run-")
                        and not (state / "reconciliation-available").exists()
                    ):
                        raise SystemExit(1)
                    create_argv_path = state / "create-argv.json"
                    create_argv = (
                        json.loads(create_argv_path.read_text())
                        if create_argv_path.exists()
                        else []
                    )
                    labels = {{}}
                    for index, argument in enumerate(create_argv):
                        if argument == "--label":
                            name, _, value = create_argv[index + 1].partition("=")
                            labels[name] = value
                    reported_id = container_id
                    if (
                        (
                            (state / "replacement").exists()
                            or (state / "owned-with-foreign-name").exists()
                        )
                        and sys.argv[-1].startswith("circular-run-")
                    ):
                        reported_id = "b" * 64
                        labels = {{}}
                    mount = next(
                        (
                            create_argv[index + 1]
                            for index, argument in enumerate(create_argv)
                            if argument == "--mount"
                        ),
                        "type=bind,src=/missing,dst=/workspace",
                    )
                    mount_parts = dict(
                        part.split("=", 1) for part in mount.split(",") if "=" in part
                    )
                    mounts = [
                        {{
                            "Type": mount_parts["type"],
                            "Source": mount_parts["src"],
                            "Destination": mount_parts["dst"],
                            "RW": True,
                        }}
                    ]
                    if unexpected_mount:
                        mounts.append(
                            {{
                                "Type": "volume",
                                "Source": "unexpected",
                                "Destination": "/image-volume",
                                "RW": True,
                            }}
                        )
                    def option(name, default):
                        try:
                            return create_argv[create_argv.index(name) + 1]
                        except ValueError:
                            return default
                    document = {{
                        "Id": reported_id,
                        "Name": "/" + next(
                            (
                                create_argv[index + 1]
                                for index, argument in enumerate(create_argv)
                                if argument == "--name"
                            ),
                            "missing",
                        ),
                        "Mounts": mounts,
                        "Config": {{
                            "Labels": labels,
                            "User": option("--user", ""),
                            "WorkingDir": option("--workdir", ""),
                        }},
                        "HostConfig": {{
                            "NetworkMode": option("--network", "default"),
                            "ReadonlyRootfs": "--read-only" in create_argv,
                            "CapDrop": [option("--cap-drop", "")],
                            "SecurityOpt": [option("--security-opt", "")],
                            "NanoCpus": int(float(option("--cpus", "0")) * 1_000_000_000),
                            "Memory": int(option("--memory", "0m")[:-1]) * 1024 * 1024,
                            "RestartPolicy": {{
                                "Name": option("--restart", ""),
                                "MaximumRetryCount": 0,
                            }},
                        }},
                    }}
                    if policy_mismatch == "mount_type":
                        document["Mounts"][0]["Type"] = "volume"
                    elif policy_mismatch == "mount_source":
                        document["Mounts"][0]["Source"] = "/different"
                    elif policy_mismatch == "mount_destination":
                        document["Mounts"][0]["Destination"] = "/different"
                    elif policy_mismatch == "mount_rw":
                        document["Mounts"][0]["RW"] = False
                    elif policy_mismatch == "network":
                        document["HostConfig"]["NetworkMode"] = "bridge"
                    elif policy_mismatch == "read_only":
                        document["HostConfig"]["ReadonlyRootfs"] = False
                    elif policy_mismatch == "cap_drop":
                        document["HostConfig"]["CapDrop"] = []
                    elif policy_mismatch == "security":
                        document["HostConfig"]["SecurityOpt"] = []
                    elif policy_mismatch == "cpu":
                        document["HostConfig"]["NanoCpus"] += 1
                    elif policy_mismatch == "memory":
                        document["HostConfig"]["Memory"] += 1
                    elif policy_mismatch == "user":
                        document["Config"]["User"] = "0:0"
                    elif policy_mismatch == "workdir":
                        document["Config"]["WorkingDir"] = "/"
                    elif policy_mismatch == "restart":
                        document["HostConfig"]["RestartPolicy"]["Name"] = "always"
                    elif policy_mismatch == "managed_label":
                        document["Config"]["Labels"]["io.circular.managed"] = "false"
                    elif policy_mismatch == "extra_label":
                        document["Config"]["Labels"]["org.opencontainers.image.source"] = "image"
                    elif policy_mismatch == "extra_circular_label":
                        document["Config"]["Labels"]["io.circular.unexpected"] = "unsafe"
                    print(json.dumps([document], ensure_ascii=False))
                raise SystemExit(0 if (state / "created").exists() else 1)
            elif operation == "start":
                (state / "start-invoked").touch()
                time.sleep(start_delay)
                (state / "stdin.bin").write_bytes(sys.stdin.buffer.read())
                (state / "start-attached").touch()
                sys.stdout.buffer.write(b"first\\n")
                sys.stdout.buffer.flush()
                if attachment_fails:
                    raise SystemExit(125)
                if waits_for_stop:
                    for _ in range(500):
                        if (state / "exit-code").exists():
                            raise SystemExit(int((state / "exit-code").read_text()))
                        time.sleep(0.01)
                    raise SystemExit(1)
                time.sleep(0.03)
                sys.stderr.buffer.write(b"warning\\n")
                sys.stderr.buffer.flush()
                time.sleep(0.03)
                sys.stdout.buffer.write(b"last\\n")
                sys.stdout.buffer.flush()
                (state / "exit-code").write_text("23")
                raise SystemExit(23)
            elif operation == "wait":
                if not (state / "start-attached").exists():
                    print("0")
                    raise SystemExit(0)
                for _ in range(500):
                    if (state / "exit-code").exists():
                        print((state / "exit-code").read_text())
                        raise SystemExit(0)
                    time.sleep(0.01)
                raise SystemExit(1)
            elif operation in {{"stop", "kill"}}:
                (state / f"{{operation}}-started").touch()
                time.sleep(stop_delay)
                (state / "exit-code").write_text("137")
            elif operation == "rm":
                (state / "rm-started").touch()
                time.sleep(remove_delay)
                if remove_fails:
                    raise SystemExit(1)
                target = sys.argv[-1]
                if target == container_id and (state / "owned-with-foreign-name").exists():
                    (state / "owned-removed").touch()
                    raise SystemExit(0)
                if target == container_id and (state / "replacement").exists():
                    raise SystemExit(1)
                (state / "created").unlink(missing_ok=True)
            else:
                raise SystemExit(2)
            """
        )
    )
    executable.chmod(0o755)
    return executable, state


async def _wait_for_path(path: Path) -> None:
    async with asyncio.timeout(2):
        while not path.exists():  # noqa: ASYNC110, ASYNC240 -- external process marker
            await asyncio.sleep(0.005)


def test_resolve_exposes_a_hardened_deterministic_container_plan(tmp_path: Path) -> None:
    root, worktree = _worktree(tmp_path)
    runtime = DockerRuntime(root)

    plan = runtime.resolve(_spec(worktree, command=("--behavior", "success")))

    assert plan.run_id == RUN_ID
    assert plan.container_name == "circular-run-00000000000040008000000000000171"
    assert plan.labels == (
        ("io.circular.managed", "true"),
        ("io.circular.run_id", str(RUN_ID)),
        ("io.circular.policy_digest", plan.policy_digest),
    )
    assert len(plan.policy_digest) == 64
    assert runtime.resolve(_spec(worktree, command=("--behavior", "success"))) == plan
    assert plan.image == "circular-fake-agent-workload:test"
    assert plan.command == ("--behavior", "success")
    assert plan.stdin == b'{"protocol_version":1}\n'
    assert plan.environment_names == ()
    assert plan.worktree_source == worktree
    assert plan.worktree_destination == "/workspace"
    assert plan.worktree_read_only is False
    assert plan.working_directory == "/workspace"
    assert plan.container_user == "65532:65532"
    assert plan.network_mode == "none"
    assert plan.root_read_only is True
    assert plan.cap_drop == ("ALL",)
    assert plan.security_options == ("no-new-privileges",)
    assert plan.cpu_limit == 1.5
    assert plan.memory_limit_mb == 384


async def test_start_streams_ordered_output_and_returns_one_stable_result(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path)
    secret = "run-scoped-do-not-print"
    monkeypatch.setenv("DATABASE_URL", "ambient-database-do-not-print")
    monkeypatch.setenv("CIRCULAR_PLATFORM_TOKEN", "ambient-platform-do-not-print")
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        allowed_environment_names=frozenset({"RUN_SCOPED_TOKEN"}),
    )
    spec = _spec(worktree, environment={"RUN_SCOPED_TOKEN": secret})
    assert isinstance(runtime, Runtime)

    handle = await runtime.start(spec)
    repeated_handle = await runtime.start(spec)
    first_result = await runtime.wait(handle)
    output = [chunk async for chunk in runtime.output(handle)]

    assert repeated_handle == handle
    assert handle.id == "circular-run-00000000000040008000000000000171"
    assert handle.resource_id == CONTAINER_ID
    assert output == [
        RuntimeOutput(OutputStream.STDOUT, b"first\n"),
        RuntimeOutput(OutputStream.STDERR, b"warning\n"),
        RuntimeOutput(OutputStream.STDOUT, b"last\n"),
    ]
    assert first_result == RuntimeResult.exited(23)
    assert await runtime.wait(handle) == first_result
    assert (state / "stdin.bin").read_bytes() == spec.stdin

    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls].count("create") == 1
    assert [call["argv"][0] for call in calls].count("start") == 1
    assert next(call["argv"] for call in calls if call["argv"][0] == "start")[-1] == (CONTAINER_ID)
    assert not any(call["argv"][0] == "wait" for call in calls)
    post_create_operations = {"start", "stop", "kill", "rm"}
    assert all(
        call["argv"][-1] == CONTAINER_ID
        for call in calls
        if call["argv"][0] in post_create_operations
    )
    assert all(
        call["argv"][-1] == CONTAINER_ID
        for call in calls
        if call["argv"][0] == "container" and "--format" in call["argv"]
    )
    assert all(secret not in argument for call in calls for argument in call["argv"])
    create_argv = next(call["argv"] for call in calls if call["argv"][0] == "create")
    assert create_argv.count("--mount") == 1
    assert f"type=bind,src={worktree},dst=/workspace" in create_argv
    assert ["--network", "none"] == create_argv[
        create_argv.index("--network") : create_argv.index("--network") + 2
    ]
    assert "--read-only" in create_argv
    assert ["--cap-drop", "ALL"] == create_argv[
        create_argv.index("--cap-drop") : create_argv.index("--cap-drop") + 2
    ]
    assert ["--security-opt", "no-new-privileges"] == create_argv[
        create_argv.index("--security-opt") : create_argv.index("--security-opt") + 2
    ]
    assert ["--cpus", "1.5"] == create_argv[
        create_argv.index("--cpus") : create_argv.index("--cpus") + 2
    ]
    assert ["--memory", "384m"] == create_argv[
        create_argv.index("--memory") : create_argv.index("--memory") + 2
    ]
    assert ["--user", "65532:65532"] == create_argv[
        create_argv.index("--user") : create_argv.index("--user") + 2
    ]
    assert ["--workdir", "/workspace"] == create_argv[
        create_argv.index("--workdir") : create_argv.index("--workdir") + 2
    ]
    assert ["--env", "RUN_SCOPED_TOKEN"] == create_argv[
        create_argv.index("--env") : create_argv.index("--env") + 2
    ]
    assert not any("docker.sock" in argument for argument in create_argv)
    assert not any(".ssh" in argument for argument in create_argv)
    create_environment = json.loads((state / "create-environment.json").read_text())
    assert create_environment["RUN_SCOPED_TOKEN"] == secret
    assert "DATABASE_URL" not in create_environment
    assert "CIRCULAR_PLATFORM_TOKEN" not in create_environment


async def test_start_rejects_and_removes_a_container_with_an_unexpected_mount(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, unexpected_mount=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerStartError, match="policy"):
        await runtime.start(_spec(worktree))

    assert not (state / "start-invoked").exists()
    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == ["create", "container", "rm"]
    assert calls[-1]["argv"] == ["rm", "--force", "--volumes", CONTAINER_ID]


@pytest.mark.parametrize(
    "policy_mismatch",
    [
        "mount_type",
        "mount_source",
        "mount_destination",
        "mount_rw",
        "network",
        "read_only",
        "cap_drop",
        "security",
        "cpu",
        "memory",
        "user",
        "workdir",
        "restart",
        "managed_label",
        "extra_circular_label",
    ],
)
async def test_start_rejects_every_effective_policy_mismatch_before_start(
    tmp_path: Path,
    policy_mismatch: str,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, policy_mismatch=policy_mismatch)
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerStartError, match="policy"):
        await runtime.start(_spec(worktree))

    assert not (state / "start-invoked").exists()
    assert not (state / "created").exists()


async def test_start_allows_benign_image_labels_outside_the_reserved_namespace(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, _ = _fake_docker(tmp_path, policy_mismatch="extra_label")
    runtime = DockerRuntime(root, docker_executable=str(executable))

    handle = await runtime.start(_spec(worktree))

    assert await runtime.wait(handle) == RuntimeResult.exited(23)


async def test_invalid_create_identity_is_reconciled_by_nonce_and_removed(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, invalid_create_id=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerStartError, match="invalid container identity"):
        await runtime.start(_spec(worktree))

    assert not (state / "start-invoked").exists()
    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == [
        "create",
        "container",
        "container",
        "rm",
    ]
    assert calls[1]["argv"][-1].startswith("label=io.circular.create_nonce=")
    assert calls[2]["argv"][-1] == CONTAINER_ID
    assert calls[-1]["argv"][-1] == CONTAINER_ID


async def test_nonzero_create_with_a_matching_nonce_is_removed_by_immutable_id(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, create_returns_failure=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerStartError, match="create"):
        await runtime.start(_spec(worktree))

    assert not (state / "start-invoked").exists()
    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == ["create", "container", "rm"]
    assert calls[-1]["argv"][-1] == CONTAINER_ID


async def test_timed_out_create_is_reconciled_by_nonce_and_removed(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.2,
        create_hangs_after_creation=True,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.05,
    )

    with pytest.raises(DockerOperationError, match="deadline"):
        await runtime.start(_spec(worktree))

    assert not (state / "start-invoked").exists()
    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == [
        "create",
        "container",
        "container",
        "rm",
    ]
    assert calls[-1]["argv"][-1] == CONTAINER_ID


async def test_timed_out_create_finds_and_removes_a_late_daemon_commit(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.2,
        create_late_commit_delay=0.08,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.05,
    )

    with pytest.raises(DockerOperationError, match="deadline"):
        await runtime.start(_spec(worktree))
    await asyncio.sleep(0.12)

    assert not (state / "created").exists()
    assert not (state / "start-invoked").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls].count("create") == 1
    assert [call["argv"][0] for call in calls].count("rm") == 1
    assert calls[-1]["argv"] == ["rm", "--force", "--volumes", CONTAINER_ID]


async def test_ambiguous_create_removes_nonce_owned_id_before_foreign_name_check(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.2,
        create_hangs_after_creation=True,
        owned_nonce_with_foreign_name=True,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.05,
    )

    with pytest.raises(DockerOperationError, match="deadline"):
        await runtime.start(_spec(worktree))

    assert (state / "owned-removed").exists()
    assert (state / "created").exists()
    assert not (state / "start-invoked").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert calls[1]["argv"][0:2] == ["container", "ls"]
    assert calls[1]["argv"][-1].startswith("label=io.circular.create_nonce=")
    assert calls[2]["argv"] == ["container", "inspect", CONTAINER_ID]
    assert calls[3]["argv"] == ["rm", "--force", "--volumes", CONTAINER_ID]


async def test_timed_out_create_without_a_commit_clears_after_settle_for_retry(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.3,
        create_hangs_without_commit_once=True,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.1,
    )

    with pytest.raises(DockerOperationError, match="deadline"):
        await runtime.start(_spec(worktree))
    handle = await runtime.start(_spec(worktree))

    assert await runtime.wait(handle) == RuntimeResult.exited(23)
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls].count("create") == 2
    assert [call["argv"][0] for call in calls].count("start") == 1


async def test_unavailable_reconciliation_is_surfaced_and_retried_before_create(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.2,
        create_hangs_after_creation=True,
        reconciliation_unavailable=True,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.05,
    )

    with pytest.raises(DockerOperationError, match="reconcile") as raised:
        await runtime.start(_spec(worktree))
    assert isinstance(raised.value.__cause__, DockerOperationError)
    assert "deadline" in str(raised.value.__cause__)
    assert (state / "created").exists()

    (state / "reconciliation-available").touch()
    with pytest.raises(DockerOperationError, match="deadline"):
        await runtime.start(_spec(worktree))

    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    operations = [call["argv"][0] for call in calls]
    assert operations.count("create") == 2
    assert operations.count("rm") == 2
    first_rm = operations.index("rm")
    assert operations.index("create", 1) > first_rm
    assert not (state / "created").exists()


async def test_cancelling_retained_create_cleanup_never_starts_a_second_create(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.2,
        create_hangs_after_creation=True,
        reconciliation_unavailable=True,
        remove_delay=0.05,
    )
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.1,
    )
    with pytest.raises(DockerOperationError, match="reconcile"):
        await runtime.start(_spec(worktree))
    (state / "reconciliation-available").touch()

    retry = asyncio.create_task(runtime.start(_spec(worktree)))
    await _wait_for_path(state / "rm-started")
    retry.cancel()
    with pytest.raises(asyncio.CancelledError):
        await retry

    assert not (state / "created").exists()
    assert not (state / "start-invoked").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls].count("create") == 1
    assert [call["argv"][0] for call in calls].count("rm") == 1


async def test_policy_inspection_accepts_a_utf8_worktree_path(tmp_path: Path) -> None:
    root = tmp_path / "wörktrees"
    worktree = root / str(RUN_ID)
    worktree.mkdir(parents=True)
    executable, state = _fake_docker(tmp_path)
    runtime = DockerRuntime(root, docker_executable=str(executable))

    handle = await runtime.start(_spec(worktree))

    assert await runtime.wait(handle) == RuntimeResult.exited(23)
    assert (state / "start-invoked").exists()


async def test_failed_rejection_cleanup_is_surfaced_with_the_policy_failure_retained(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        unexpected_mount=True,
        remove_fails=True,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerStartError, match="remove") as raised:
        await runtime.start(_spec(worktree))

    assert isinstance(raised.value.__cause__, ContainerStartError)
    assert "policy" in str(raised.value.__cause__)
    assert (state / "created").exists()
    assert not (state / "start-invoked").exists()


async def test_stop_is_idempotent_and_returns_a_stable_stopped_result(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, waits_for_stop=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    await _wait_for_path(state / "start-attached")

    await asyncio.gather(runtime.stop(handle), runtime.stop(handle))
    first_result = await runtime.wait(handle)
    await runtime.stop(handle)

    assert first_result == RuntimeResult.stopped()
    assert await runtime.wait(handle) == first_result
    output = [chunk async for chunk in runtime.output(handle)]
    assert output == [RuntimeOutput(OutputStream.STDOUT, b"first\n")]
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls].count("stop") == 1
    assert next(call["argv"] for call in calls if call["argv"][0] == "stop")[-1] == (CONTAINER_ID)


async def test_discard_permanently_removes_only_the_immutable_resource(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, waits_for_stop=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    await _wait_for_path(state / "start-attached")

    await asyncio.gather(runtime.discard(handle), runtime.discard(handle))
    await runtime.discard(handle)

    assert not (state / "created").exists()
    with pytest.raises(UnknownContainerHandleError):
        await runtime.wait(handle)
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    removals = [call["argv"] for call in calls if call["argv"][0] == "rm"]
    assert removals == [["rm", "--force", "--volumes", CONTAINER_ID]]


async def test_cancelling_discard_finishes_resource_removal_before_propagating(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        waits_for_stop=True,
        remove_delay=0.1,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    await _wait_for_path(state / "start-attached")
    discard = asyncio.create_task(runtime.discard(handle))
    await _wait_for_path(state / "rm-started")

    discard.cancel()
    with pytest.raises(asyncio.CancelledError):
        await discard

    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    removals = [call["argv"] for call in calls if call["argv"][0] == "rm"]
    assert removals == [["rm", "--force", "--volumes", CONTAINER_ID]]


async def test_discard_surfaces_a_typed_removal_failure(tmp_path: Path) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        waits_for_stop=True,
        remove_fails=True,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    await _wait_for_path(state / "start-attached")

    with pytest.raises(ContainerDiscardError, match="discard"):
        await runtime.discard(handle)

    assert (state / "created").exists()


async def test_cancelling_start_finishes_create_and_removes_only_its_new_container(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, create_delay=0.1)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    start_task = asyncio.create_task(runtime.start(_spec(worktree)))
    await _wait_for_path(state / "create-started")

    start_task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await start_task

    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == [
        "create",
        "container",
        "container",
        "rm",
    ]
    assert calls[-1]["argv"] == ["rm", "--force", "--volumes", CONTAINER_ID]


async def test_cancelling_stop_finishes_termination_before_propagating(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, waits_for_stop=True, stop_delay=0.1)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    await _wait_for_path(state / "start-attached")
    stop_task = asyncio.create_task(runtime.stop(handle))
    await _wait_for_path(state / "stop-started")

    stop_task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await stop_task

    assert await runtime.wait(handle) == RuntimeResult.stopped()
    assert (state / "exit-code").read_text() == "137"


async def test_cancelled_create_cleanup_cannot_remove_a_same_name_replacement(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.1,
        replace_name_on_create=True,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    start_task = asyncio.create_task(runtime.start(_spec(worktree)))
    await _wait_for_path(state / "create-started")

    start_task.cancel()
    with pytest.raises(ContainerNameConflictError, match="already occupied") as raised:
        await start_task

    assert isinstance(raised.value.__cause__, asyncio.CancelledError)
    assert (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == ["create", "container", "container"]
    assert calls[1]["argv"][1] == "ls"
    assert calls[-1]["argv"][-1] == "circular-run-00000000000040008000000000000171"


async def test_cancelled_create_surfaces_cleanup_failure_and_retains_cancellation(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        create_delay=0.1,
        remove_fails=True,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    start_task = asyncio.create_task(runtime.start(_spec(worktree)))
    await _wait_for_path(state / "create-started")

    start_task.cancel()
    with pytest.raises(ContainerStartError, match="remove") as raised:
        await start_task

    assert isinstance(raised.value.__cause__, asyncio.CancelledError)
    assert (state / "created").exists()
    assert not (state / "start-invoked").exists()


async def test_start_waits_until_the_container_is_no_longer_only_created(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        waits_for_stop=True,
        start_delay=0.1,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    start_task = asyncio.create_task(runtime.start(_spec(worktree)))
    await _wait_for_path(state / "start-invoked")

    assert not start_task.done()
    handle = await start_task
    assert (state / "start-attached").exists()
    await runtime.stop(handle)
    assert await runtime.wait(handle) == RuntimeResult.stopped()


async def test_unrelated_runs_do_not_wait_for_each_others_create_attempt(
    tmp_path: Path,
) -> None:
    root, first_worktree = _worktree(tmp_path)
    second_run_id = UUID("00000000-0000-4000-8000-000000000172")
    second_worktree = root / str(second_run_id)
    second_worktree.mkdir()
    executable, state = _fake_docker(tmp_path, create_delay=0.2)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    first = asyncio.create_task(runtime.start(_spec(first_worktree)))
    second = asyncio.create_task(runtime.start(_spec(second_worktree, run_id=second_run_id)))

    try:
        async with asyncio.timeout(0.15):
            while True:  # noqa: ASYNC110 -- polling an external process log
                calls_path = state / "calls.jsonl"
                calls = calls_path.read_text().splitlines() if calls_path.exists() else []
                if sum(json.loads(line)["argv"][0] == "create" for line in calls) == 2:
                    break
                await asyncio.sleep(0.005)
    finally:
        first.cancel()
        second.cancel()
        await asyncio.gather(first, second, return_exceptions=True)


async def test_start_bounds_stdin_delivery_and_removes_the_owned_container(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, start_delay=5)
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        operation_timeout_seconds=0.2,
    )

    async with asyncio.timeout(2):
        with pytest.raises(ContainerStartError, match="observe"):
            await runtime.start(_spec(worktree, stdin=b"x" * (4 * 1024 * 1024)))

    assert not (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert calls[-1]["argv"] == ["rm", "--force", "--volumes", CONTAINER_ID]


async def test_lost_attachment_is_an_error_and_stop_still_terminates_the_container(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, attachment_fails=True)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))

    async with asyncio.timeout(1):
        with pytest.raises(ContainerStartError, match="attachment"):
            await runtime.wait(handle)
    assert (state / "stop-started").exists()
    assert (state / "exit-code").read_text() == "137"
    await runtime.stop(handle)

    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert next(call["argv"] for call in calls if call["argv"][0] == "stop")[-1] == (CONTAINER_ID)


async def test_stop_terminates_a_running_container_after_observation_failed(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(
        tmp_path,
        attachment_fails=True,
        inspect_fails_after_ready=True,
    )
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))

    with pytest.raises(DockerOperationError, match="inspect"):
        await runtime.wait(handle)
    await runtime.stop(handle)

    assert (state / "exit-code").read_text() == "137"
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert next(call["argv"] for call in calls if call["argv"][0] == "stop")[-1] == (CONTAINER_ID)


@pytest.mark.parametrize(
    "name",
    [
        "PATH",
        "HOME",
        "DATABASE_URL",
        "CIRCULAR_PLATFORM_TOKEN",
        "DOCKER_HOST",
        "SSH_AUTH_SOCK",
        "XDG_CONFIG_HOME",
        "LD_PRELOAD",
        "DYLD_INSERT_LIBRARIES",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "NO_PROXY",
        "SSL_CERT_FILE",
        "GODEBUG",
    ],
)
def test_process_control_environment_cannot_be_allowlisted(tmp_path: Path, name: str) -> None:
    root, _ = _worktree(tmp_path)

    with pytest.raises(InvalidDockerConfiguration, match="cannot be allowlisted"):
        DockerRuntime(root, allowed_environment_names=frozenset({name}))


def test_environment_values_are_redacted_and_snapshotted_before_launch(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    runtime = DockerRuntime(root, allowed_environment_names=frozenset({"RUN_SCOPED_TOKEN"}))
    secret = "do-not-print-one"
    plan = runtime.resolve(_spec(worktree, environment={"RUN_SCOPED_TOKEN": secret}))

    assert plan.environment_names == ("RUN_SCOPED_TOKEN",)
    assert secret not in repr(plan)
    assert secret not in plan.policy_digest
    assert all(secret not in value for _, value in plan.labels)


@pytest.mark.parametrize(
    "relative_path",
    [
        Path("."),
        Path("not-a-run-id"),
        Path(f"{RUN_ID}/nested"),
        Path(str(UUID("00000000-0000-4000-8000-000000000172"))),
    ],
)
def test_resolve_rejects_any_path_except_the_matching_direct_run_uuid_child(
    tmp_path: Path, relative_path: Path
) -> None:
    root, _ = _worktree(tmp_path)
    runtime = DockerRuntime(root)

    with pytest.raises(InvalidContainerSpec, match="worktree"):
        runtime.resolve(_spec(root / relative_path))


def test_resolve_rejects_a_worktree_symlink(tmp_path: Path) -> None:
    root = tmp_path / "worktrees"
    outside = tmp_path / "outside"
    root.mkdir()
    outside.mkdir()
    worktree = root / str(RUN_ID)
    worktree.symlink_to(outside, target_is_directory=True)

    with pytest.raises(InvalidContainerSpec, match="symlink"):
        DockerRuntime(root).resolve(_spec(worktree))


@pytest.mark.parametrize(
    ("override", "message"),
    [
        ({"image": "--privileged"}, "image"),
        ({"command": ["not", "immutable"]}, "command"),
        ({"command": ("bad\0argument",)}, "command"),
        ({"command": ("\ud800",)}, "command"),
        ({"stdin": bytearray(b"mutable")}, "stdin"),
        ({"cpu_limit": 0}, "CPU"),
        ({"cpu_limit": float("inf")}, "CPU"),
        ({"memory_limit_mb": 0}, "memory"),
        ({"memory_limit_mb": True}, "memory"),
        ({"network_enabled": "false"}, "network"),
        ({"run_id": str(RUN_ID)}, "identity"),
    ],
)
def test_resolve_rejects_values_that_cannot_form_safe_docker_argv(
    tmp_path: Path, override: dict[str, object], message: str
) -> None:
    root, worktree = _worktree(tmp_path)

    with pytest.raises(InvalidContainerSpec, match=message):
        DockerRuntime(root).resolve(_spec(worktree, **override))


def test_resolve_rejects_non_allowlisted_or_unencodable_environment(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)

    with pytest.raises(InvalidContainerSpec, match="not allowlisted"):
        DockerRuntime(root).resolve(_spec(worktree, environment={"TOKEN": "value"}))

    runtime = DockerRuntime(root, allowed_environment_names=frozenset({"RUN_SCOPED_TOKEN"}))
    with pytest.raises(InvalidContainerSpec, match="invalid value"):
        runtime.resolve(_spec(worktree, environment={"RUN_SCOPED_TOKEN": "\ud800"}))


def test_docker_root_must_be_absolute_managed_and_not_a_symlink(
    tmp_path: Path,
) -> None:
    with pytest.raises(InvalidDockerConfiguration, match="absolute"):
        DockerRuntime(Path("relative/worktrees"))
    with pytest.raises(InvalidDockerConfiguration, match="filesystem root"):
        DockerRuntime(Path("/"))

    target = tmp_path / "target"
    target.mkdir()
    symlink = tmp_path / "worktrees"
    symlink.symlink_to(target, target_is_directory=True)
    with pytest.raises(InvalidDockerConfiguration, match="symlink"):
        DockerRuntime(symlink)


def test_daemon_visible_worktree_does_not_need_to_exist_in_worker_namespace(
    tmp_path: Path,
) -> None:
    root = tmp_path / "docker-host-worktrees"
    root.mkdir()
    worktree = root / str(RUN_ID)

    plan = DockerRuntime(root).resolve(_spec(worktree))

    assert plan.worktree_source == worktree


async def test_preexisting_deterministic_name_is_a_typed_conflict_and_is_untouched(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path)
    (state / "created").touch()
    runtime = DockerRuntime(root, docker_executable=str(executable))

    with pytest.raises(ContainerNameConflictError, match="already occupied"):
        await runtime.start(_spec(worktree))

    assert (state / "created").exists()
    calls = [json.loads(line) for line in (state / "calls.jsonl").read_text().splitlines()]
    assert [call["argv"][0] for call in calls] == ["create", "container"]


async def test_same_instance_rejects_changed_environment_without_disclosing_values(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, _ = _fake_docker(tmp_path)
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        allowed_environment_names=frozenset({"RUN_SCOPED_TOKEN"}),
    )
    first_secret = "first-do-not-print"
    second_secret = "second-do-not-print"
    handle = await runtime.start(_spec(worktree, environment={"RUN_SCOPED_TOKEN": first_secret}))

    with pytest.raises(ContainerNameConflictError) as raised:
        await runtime.start(_spec(worktree, environment={"RUN_SCOPED_TOKEN": second_secret}))

    assert first_secret not in str(raised.value)
    assert second_secret not in str(raised.value)
    assert await runtime.wait(handle) == RuntimeResult.exited(23)


async def test_stdin_is_not_persisted_in_policy_digest_but_changes_launch_identity(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, _ = _fake_docker(tmp_path)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    first = _spec(worktree, stdin=b"confidential-a")
    second = _spec(worktree, stdin=b"confidential-b")

    assert runtime.resolve(first).policy_digest == runtime.resolve(second).policy_digest
    handle = await runtime.start(first)
    with pytest.raises(ContainerNameConflictError):
        await runtime.start(second)
    assert await runtime.wait(handle) == RuntimeResult.exited(23)


async def test_environment_is_snapshotted_before_the_first_docker_await(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, state = _fake_docker(tmp_path, create_delay=0.1)
    runtime = DockerRuntime(
        root,
        docker_executable=str(executable),
        allowed_environment_names=frozenset({"RUN_SCOPED_TOKEN"}),
    )
    environment = {"RUN_SCOPED_TOKEN": "original"}
    start_task = asyncio.create_task(runtime.start(_spec(worktree, environment=environment)))
    await _wait_for_path(state / "create-started")

    environment["RUN_SCOPED_TOKEN"] = "mutated"
    handle = await start_task
    await runtime.wait(handle)

    create_environment = json.loads((state / "create-environment.json").read_text())
    assert create_environment["RUN_SCOPED_TOKEN"] == "original"


async def test_output_has_one_consumer_and_unknown_handles_are_rejected(
    tmp_path: Path,
) -> None:
    root, worktree = _worktree(tmp_path)
    executable, _ = _fake_docker(tmp_path)
    runtime = DockerRuntime(root, docker_executable=str(executable))
    handle = await runtime.start(_spec(worktree))
    first = [chunk async for chunk in runtime.output(handle)]
    assert first

    with pytest.raises(ContainerOutputAlreadyConsumedError):
        _ = [chunk async for chunk in runtime.output(handle)]
    unknown = ContainerHandle(id="circular-run-unowned", resource_id="b" * 64)
    with pytest.raises(UnknownContainerHandleError):
        await runtime.wait(unknown)

    forged = ContainerHandle(id=handle.id, resource_id="b" * 64)
    with pytest.raises(UnknownContainerHandleError):
        await runtime.wait(forged)
