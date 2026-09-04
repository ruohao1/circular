import asyncio
import hashlib
import json
import math
import os
import re
import secrets
import shutil
import signal
from collections.abc import AsyncIterator
from contextlib import suppress
from dataclasses import dataclass, field
from pathlib import Path
from typing import Final
from uuid import UUID

from circular.runtimes.runtime import (
    ContainerHandle,
    ContainerSpec,
    OutputStream,
    RuntimeOutput,
    RuntimeResult,
)

_ENVIRONMENT_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
_IMAGE_REFERENCE = re.compile(
    r"[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?"
    r"(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*"
    r"(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[0-9a-f]{64})?\Z"
)
_CONTAINER_USER = re.compile(r"[1-9][0-9]*:[1-9][0-9]*\Z")
_CONTAINER_ID = re.compile(r"[0-9a-f]{64}\Z")
_CREATE_NONCE_LABEL: Final = "io.circular.create_nonce"
_MIN_CREATE_SETTLE_SECONDS: Final = 0.25
_MAX_CREATE_SETTLE_SECONDS: Final = 1.0
_SENSITIVE_ENVIRONMENT_NAMES = frozenset(
    {
        "DATABASE_URL",
        "DOCKER_AUTH_CONFIG",
        "DOCKER_CERT_PATH",
        "DOCKER_CONFIG",
        "DOCKER_HOST",
        "DOCKER_TLS_VERIFY",
        "GITHUB_TOKEN",
        "GODEBUG",
        "GOMAXPROCS",
        "HOME",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "NO_PROXY",
        "PATH",
        "SSLKEYLOGFILE",
        "SSH_AUTH_SOCK",
    }
)


class DockerRuntimeError(RuntimeError):
    """Base class for sanitized Docker runtime failures."""


class InvalidDockerConfiguration(DockerRuntimeError, ValueError):
    """Docker runtime configuration cannot preserve the isolation policy."""


class InvalidContainerSpec(DockerRuntimeError, ValueError):
    """A container request violates the Docker runtime policy."""


class ContainerStartError(DockerRuntimeError):
    """Docker could not safely start the requested Run container."""


class ContainerNameConflictError(ContainerStartError):
    """A deterministic Run container name is already occupied."""


class ContainerStopError(DockerRuntimeError):
    """Docker could not stop an owned Run container."""


class ContainerDiscardError(DockerRuntimeError):
    """Docker could not permanently release an uncommitted Run container."""


class UnknownContainerHandleError(DockerRuntimeError, LookupError):
    """A handle was not issued by this Docker runtime adapter instance."""


class ContainerOutputAlreadyConsumedError(DockerRuntimeError):
    """A Run container's one-shot output stream already has a consumer."""


class DockerOperationError(DockerRuntimeError):
    """A bounded Docker CLI operation failed without exposing daemon output."""


@dataclass(frozen=True, slots=True)
class DockerContainerPlan:
    """Resolved, side-effect-free Docker policy for one Run container.

    Environment values are deliberately excluded. ``DockerRuntime`` holds them
    only long enough to pass explicitly allowed names to the Docker client.
    """

    run_id: UUID
    container_name: str
    labels: tuple[tuple[str, str], ...]
    policy_digest: str
    image: str
    command: tuple[str, ...]
    stdin: bytes = field(repr=False)
    environment_names: tuple[str, ...]
    worktree_source: Path
    worktree_destination: str
    worktree_read_only: bool
    working_directory: str
    container_user: str
    network_mode: str
    root_read_only: bool
    cap_drop: tuple[str, ...]
    security_options: tuple[str, ...]
    cpu_limit: float
    memory_limit_mb: int


@dataclass(frozen=True, slots=True)
class _ResolvedLaunch:
    plan: DockerContainerPlan
    environment: tuple[tuple[str, str], ...] = field(repr=False)


@dataclass(frozen=True, slots=True)
class _ContainerState:
    status: str
    exit_code: int


@dataclass(frozen=True, slots=True)
class _UnresolvedCreate:
    nonce: str
    ambiguous: bool


_OUTPUT_EOF: Final = object()
_DEFAULT_DEADLINE: Final = object()


@dataclass(slots=True)
class _Execution:
    handle: ContainerHandle
    container_id: str
    launch: _ResolvedLaunch
    attach_process: asyncio.subprocess.Process
    ready: asyncio.Future[None]
    result: asyncio.Future[RuntimeResult]
    output_queue: asyncio.Queue[RuntimeOutput | object]
    monitor_task: asyncio.Task[None] | None = None
    output_claimed: bool = False
    stop_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    stop_decision: asyncio.Future[bool] | None = None
    stop_task: asyncio.Task[None] | None = None
    discard_task: asyncio.Task[None] | None = None
    discard_failure: DockerRuntimeError | None = None
    terminal_observed: bool = False


class DockerRuntime:
    """Docker CLI adapter enforcing Circular's per-Run isolation policy."""

    def __init__(
        self,
        worktree_root: Path,
        *,
        allowed_environment_names: frozenset[str] = frozenset(),
        docker_executable: str = "docker",
        container_user: str = "65532:65532",
        stop_timeout_seconds: float = 5.0,
        operation_timeout_seconds: float = 30.0,
    ) -> None:
        self._worktree_root = _validated_root(worktree_root)
        self._allowed_environment_names = _validated_allowlist(allowed_environment_names)
        if (
            not isinstance(docker_executable, str)
            or not docker_executable
            or "\0" in docker_executable
            or not _is_utf8(docker_executable)
        ):
            raise InvalidDockerConfiguration("Docker executable is invalid")
        if not isinstance(container_user, str) or _CONTAINER_USER.fullmatch(container_user) is None:
            raise InvalidDockerConfiguration(
                "container user must be a fixed non-root numeric UID:GID"
            )
        stop_timeout = _validated_timeout(stop_timeout_seconds, "stop timeout")
        operation_timeout = _validated_timeout(
            operation_timeout_seconds, "Docker operation timeout"
        )
        self._docker_executable = docker_executable
        self._container_user = container_user
        self._stop_timeout_seconds = stop_timeout
        self._operation_timeout_seconds = operation_timeout
        self._start_locks: dict[str, asyncio.Lock] = {}
        self._executions: dict[str, _Execution] = {}
        self._discarded_handles: set[ContainerHandle] = set()
        self._unresolved_creates: dict[str, _UnresolvedCreate] = {}

    def resolve(self, spec: ContainerSpec) -> DockerContainerPlan:
        """Validate a request and return the exact policy resolved for launch."""

        return self._resolve_launch(spec).plan

    async def start(self, spec: ContainerSpec) -> ContainerHandle:
        """Create and attach one container, idempotently within this adapter instance."""

        launch = self._resolve_launch(spec)
        start_lock = self._start_locks.setdefault(
            launch.plan.container_name,
            asyncio.Lock(),
        )
        async with start_lock:
            existing = self._executions.get(launch.plan.container_name)
            if existing is not None and existing.discard_task is not None:
                await asyncio.shield(existing.discard_task)
                existing = self._executions.get(launch.plan.container_name)
            if existing is not None:
                if existing.discard_failure is not None:
                    raise existing.discard_failure
                if existing.launch != launch:
                    raise ContainerNameConflictError(
                        "Run was already started with a different container specification"
                    )
                return existing.handle

            await self._cleanup_unresolved_create(launch.plan.container_name)

            created_container_id: str | None = None
            attach_process: asyncio.subprocess.Process | None = None
            execution: _Execution | None = None
            create_nonce = secrets.token_hex(16)
            self._unresolved_creates[launch.plan.container_name] = _UnresolvedCreate(
                nonce=create_nonce,
                ambiguous=True,
            )
            try:
                create_result, create_stdout = await self._create_container(launch, create_nonce)
                if create_result != 0:
                    self._unresolved_creates[launch.plan.container_name] = _UnresolvedCreate(
                        nonce=create_nonce,
                        ambiguous=False,
                    )
                    raise ContainerStartError("Docker could not create the Run container")
                created_container_id = _validated_container_id(create_stdout)
                self._unresolved_creates[launch.plan.container_name] = _UnresolvedCreate(
                    nonce=create_nonce,
                    ambiguous=False,
                )
                await self._verify_container_policy(
                    created_container_id,
                    launch.plan,
                    create_nonce,
                )
                attach_process = await self._spawn_cli(
                    (
                        "start",
                        "--attach",
                        "--interactive",
                        created_container_id,
                    ),
                    stdin=asyncio.subprocess.PIPE,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                )
                loop = asyncio.get_running_loop()
                execution = _Execution(
                    handle=ContainerHandle(
                        id=launch.plan.container_name,
                        resource_id=created_container_id,
                    ),
                    container_id=created_container_id,
                    launch=launch,
                    attach_process=attach_process,
                    ready=loop.create_future(),
                    result=loop.create_future(),
                    output_queue=asyncio.Queue(),
                )
                self._executions[execution.handle.id] = execution
                execution.monitor_task = asyncio.create_task(
                    self._monitor(execution),
                    name=f"docker-runtime:{launch.plan.run_id}",
                )
                await asyncio.shield(execution.ready)
                self._unresolved_creates.pop(launch.plan.container_name, None)
                return execution.handle
            except BaseException as failure:
                if execution is not None and execution.monitor_task is not None:
                    execution.monitor_task.cancel()
                    try:
                        await _await_task_despite_cancellation(execution.monitor_task)
                    except (asyncio.CancelledError, DockerRuntimeError):
                        pass
                    self._executions.pop(execution.handle.id, None)
                    _consume_future(execution.ready)
                    _consume_future(execution.result)
                elif attach_process is not None:
                    task = asyncio.create_task(_terminate_process(attach_process))
                    await _await_task_despite_cancellation(task)
                if created_container_id is None:
                    try:
                        unresolved = self._unresolved_creates[launch.plan.container_name]
                        reconciliation = asyncio.create_task(
                            self._reconcile_created_container(
                                launch.plan.container_name,
                                unresolved,
                            )
                        )
                        created_container_id = await _await_task_despite_cancellation(
                            reconciliation
                        )
                    except DockerRuntimeError as reconciliation_error:
                        raise reconciliation_error from failure
                if created_container_id is not None:
                    try:
                        await self._remove_new_container_safely(created_container_id)
                    except DockerRuntimeError as cleanup_error:
                        raise cleanup_error from failure
                self._unresolved_creates.pop(launch.plan.container_name, None)
                raise

    async def _cleanup_unresolved_create(self, container_name: str) -> None:
        unresolved = self._unresolved_creates.get(container_name)
        if unresolved is None:
            return
        container_id = await self._reconcile_created_container(
            container_name,
            unresolved,
        )
        if container_id is not None:
            cleanup = asyncio.create_task(self._remove_new_container(container_id))
            try:
                await asyncio.shield(cleanup)
            except asyncio.CancelledError as cancelled:
                try:
                    await _await_task_despite_cancellation(cleanup)
                except DockerRuntimeError as cleanup_error:
                    raise cleanup_error from cancelled
                self._unresolved_creates.pop(container_name, None)
                raise
        self._unresolved_creates.pop(container_name, None)

    async def output(self, handle: ContainerHandle) -> AsyncIterator[RuntimeOutput]:
        """Yield the one-shot merged stdout/stderr stream in observation order."""

        execution = self._execution(handle)
        if execution.output_claimed:
            raise ContainerOutputAlreadyConsumedError("container output can only be consumed once")
        execution.output_claimed = True
        while True:
            item = await execution.output_queue.get()
            if item is _OUTPUT_EOF:
                return
            if not isinstance(item, RuntimeOutput):
                raise AssertionError("Docker output queue contained an invalid item")
            yield item

    async def wait(self, handle: ContainerHandle) -> RuntimeResult:
        """Wait without allowing caller cancellation to cancel shared completion."""

        execution = self._execution(handle)
        return await asyncio.shield(execution.result)

    async def stop(self, handle: ContainerHandle) -> None:
        """Stop an owned container; repeated calls and completed Runs are no-ops."""

        execution = self._execution(handle)
        task = execution.stop_task
        if task is None:
            task = asyncio.create_task(self._stop_execution(execution))
            execution.stop_task = task
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError as cancelled:
            try:
                await _await_task_despite_cancellation(task)
            finally:
                raise cancelled

    async def discard(self, handle: ContainerHandle) -> None:
        """Permanently release an allocation that was not durably handed off."""

        if not isinstance(handle, ContainerHandle):
            raise UnknownContainerHandleError("container handle is invalid")
        if handle in self._discarded_handles:
            return
        execution = self._execution(handle)
        task = execution.discard_task
        if task is None:
            task = asyncio.create_task(self._discard_execution(execution))
            execution.discard_task = task
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError as cancelled:
            try:
                await _await_task_despite_cancellation(task)
            except DockerRuntimeError as error:
                raise error from cancelled
            raise

    async def release(self, run_id: UUID, resource_id: str | None) -> None:
        """Release a persisted allocation, including one left by another worker.

        Inspect ownership and the sole worktree mount before removing an immutable
        ID. With no persisted ID, the deterministic name reconciles a crash between
        Docker creation and the identity write.
        """
        run_id = _validate_run_id(run_id)
        name = f"circular-run-{run_id.hex}"
        if resource_id is not None and _CONTAINER_ID.fullmatch(resource_id) is None:
            raise ContainerDiscardError("persisted container ID is invalid")
        execution = self._executions.get(name)
        if execution is not None:
            if resource_id is not None and execution.container_id != resource_id:
                raise ContainerDiscardError("persisted container identity does not match")
            await self.discard(execution.handle)
            return
        result, stdout = await self._run_cli(
            ("container", "inspect", resource_id or name), capture_stdout=True
        )
        if result != 0:
            if await self._container_name_is_absent(name):
                return
            raise ContainerDiscardError("could not confirm that the Run container is absent")
        container = _decoded_container_inspection(stdout)
        config = container.get("Config", {})
        labels = config.get("Labels", {}) if isinstance(config, dict) else {}
        mounts = container.get("Mounts")
        identifier = container.get("Id")
        if not (
            isinstance(identifier, str)
            and _CONTAINER_ID.fullmatch(identifier)
            and (resource_id is None or resource_id == identifier)
            and container.get("Name") == f"/{name}"
            and isinstance(labels, dict)
            and labels.get("io.circular.managed") == "true"
            and labels.get("io.circular.run_id") == str(run_id)
            and isinstance(mounts, list)
            and len(mounts) == 1
            and mounts[0].get("Type") == "bind"
            and mounts[0].get("Source") == str(self._worktree_root / str(run_id))
            and mounts[0].get("Destination") == "/workspace"
        ):
            raise ContainerDiscardError("container ownership could not be verified")
        result, _ = await self._run_cli(("rm", "--force", "--volumes", identifier))
        if result != 0:
            raise ContainerDiscardError("Docker could not release the Run container")

    async def _discard_execution(self, execution: _Execution) -> None:
        stop_failure: DockerRuntimeError | None = None
        try:
            try:
                await self.stop(execution.handle)
            except DockerRuntimeError as error:
                # Forced removal is the final containment operation and may still
                # succeed when graceful observation or termination did not.
                stop_failure = error
            result, _ = await self._run_cli(("rm", "--force", "--volumes", execution.container_id))
            if result != 0:
                raise ContainerDiscardError(
                    "Docker could not discard the uncommitted Run container"
                ) from stop_failure
        except DockerRuntimeError as error:
            execution.discard_failure = error
            execution.discard_task = None
            raise
        except BaseException:
            execution.discard_task = None
            raise
        current = self._executions.get(execution.handle.id)
        if current is execution:
            self._executions.pop(execution.handle.id)
        self._discarded_handles.add(execution.handle)

    async def _stop_execution(self, execution: _Execution) -> None:
        async with execution.stop_lock:
            if not execution.terminal_observed:
                loop = asyncio.get_running_loop()
                decision = loop.create_future()
                execution.stop_decision = decision
                stopped_by_runtime = False
                try:
                    state: _ContainerState | None = None
                    failure: DockerRuntimeError | None = None
                    try:
                        state = await self._container_state(execution.container_id)
                    except DockerRuntimeError as error:
                        failure = error

                    if state is not None and state.status in {"exited", "dead"}:
                        execution.terminal_observed = True
                    elif state is not None and state.status == "created":
                        # Defensive handling for a daemon/client readiness race:
                        # prevent the attached start client from launching later,
                        # then remove only the immutable container identity we own.
                        await _terminate_process(execution.attach_process)
                        try:
                            removed, _ = await self._run_cli(
                                ("rm", "--force", execution.container_id)
                            )
                            stopped_by_runtime = removed == 0
                        except DockerRuntimeError as error:
                            failure = error
                    else:
                        stopped_by_runtime, stop_failure = await self._stop_running_container(
                            execution.container_id
                        )
                        if stop_failure is not None:
                            failure = stop_failure

                    if stopped_by_runtime:
                        execution.terminal_observed = True
                    if not execution.terminal_observed:
                        raise ContainerStopError(
                            "Docker could not stop the Run container"
                        ) from failure
                finally:
                    if not decision.done():
                        decision.set_result(stopped_by_runtime)

        await self._finish_observation_after_stop(execution)

    async def _stop_running_container(
        self, container_id: str
    ) -> tuple[bool, DockerRuntimeError | None]:
        failure: DockerRuntimeError | None = None
        timeout = max(1, math.ceil(self._stop_timeout_seconds))
        try:
            stopped, _ = await self._run_cli(
                ("stop", "--time", str(timeout), container_id),
                deadline_seconds=self._stop_timeout_seconds + self._operation_timeout_seconds,
            )
            if stopped == 0:
                return True, None
        except DockerRuntimeError as error:
            failure = error

        try:
            killed, _ = await self._run_cli(("kill", container_id))
            if killed == 0:
                return True, None
        except DockerRuntimeError as error:
            failure = error
        return False, failure

    async def _finish_observation_after_stop(self, execution: _Execution) -> None:
        try:
            async with asyncio.timeout(
                self._stop_timeout_seconds + self._operation_timeout_seconds
            ):
                try:
                    await asyncio.shield(execution.result)
                except DockerRuntimeError:
                    pass
                if execution.monitor_task is not None:
                    await asyncio.shield(execution.monitor_task)
        except TimeoutError:
            await _terminate_process(execution.attach_process)
            if execution.monitor_task is not None and not execution.monitor_task.done():
                execution.monitor_task.cancel()
                with suppress(asyncio.CancelledError):
                    await execution.monitor_task

    async def _create_container(
        self, launch: _ResolvedLaunch, create_nonce: str
    ) -> tuple[int, str]:
        task = asyncio.create_task(
            self._run_cli(
                self._create_arguments(launch.plan, create_nonce),
                environment=self._minimal_cli_environment(launch.environment),
                capture_stdout=True,
            )
        )
        try:
            return await asyncio.shield(task)
        except asyncio.CancelledError as cancelled:
            try:
                await _await_task_despite_cancellation(task)
            except DockerRuntimeError as error:
                raise cancelled from error
            finally:
                if task.cancelled():
                    raise cancelled
            raise cancelled

    def _resolve_launch(self, spec: ContainerSpec) -> _ResolvedLaunch:
        run_id = _validate_run_id(spec.run_id)
        worktree = _validated_worktree(self._worktree_root, spec.worktree, run_id)
        image = _validated_image(spec.image)
        command = _validated_command(spec.command)
        stdin = _validated_stdin(spec.stdin)
        cpu_limit = _validated_cpu_limit(spec.cpu_limit)
        memory_limit_mb = _validated_memory_limit(spec.memory_limit_mb)
        environment = _validated_environment(spec.environment, self._allowed_environment_names)
        if not isinstance(spec.network_enabled, bool):
            raise InvalidContainerSpec("network policy must be a boolean")

        digest = _policy_digest(
            run_id=run_id,
            image=image,
            command=command,
            environment=environment,
            worktree=worktree,
            network_enabled=spec.network_enabled,
            cpu_limit=cpu_limit,
            memory_limit_mb=memory_limit_mb,
            container_user=self._container_user,
        )
        plan = DockerContainerPlan(
            run_id=run_id,
            container_name=f"circular-run-{run_id.hex}",
            labels=(
                ("io.circular.managed", "true"),
                ("io.circular.run_id", str(run_id)),
                ("io.circular.policy_digest", digest),
            ),
            policy_digest=digest,
            image=image,
            command=command,
            stdin=stdin,
            environment_names=tuple(name for name, _ in environment),
            worktree_source=worktree,
            worktree_destination="/workspace",
            worktree_read_only=False,
            working_directory="/workspace",
            container_user=self._container_user,
            network_mode="bridge" if spec.network_enabled else "none",
            root_read_only=True,
            cap_drop=("ALL",),
            security_options=("no-new-privileges",),
            cpu_limit=cpu_limit,
            memory_limit_mb=memory_limit_mb,
        )
        return _ResolvedLaunch(plan=plan, environment=environment)

    def _create_arguments(self, plan: DockerContainerPlan, create_nonce: str) -> tuple[str, ...]:
        arguments: list[str] = ["create", "--name", plan.container_name]
        for name, value in plan.labels:
            arguments.extend(("--label", f"{name}={value}"))
        arguments.extend(("--label", f"{_CREATE_NONCE_LABEL}={create_nonce}"))
        arguments.extend(
            (
                "--network",
                plan.network_mode,
                "--read-only",
                "--cap-drop",
                "ALL",
                "--security-opt",
                "no-new-privileges",
                "--cpus",
                format(plan.cpu_limit, ".15g"),
                "--memory",
                f"{plan.memory_limit_mb}m",
                "--user",
                plan.container_user,
                "--workdir",
                plan.working_directory,
                "--restart",
                "no",
                "--mount",
                (f"type=bind,src={plan.worktree_source},dst={plan.worktree_destination}"),
                "--interactive",
            )
        )
        for name in plan.environment_names:
            arguments.extend(("--env", name))
        arguments.append(plan.image)
        arguments.extend(plan.command)
        return tuple(arguments)

    async def _verify_container_policy(
        self,
        container_id: str,
        plan: DockerContainerPlan,
        create_nonce: str,
    ) -> None:
        container = await self._inspect_container(container_id)
        if container.get("Id") != container_id:
            raise DockerOperationError("Docker returned an invalid container inspection")

        mounts = container.get("Mounts")
        config = container.get("Config")
        host_config = container.get("HostConfig")
        expected_labels = {
            **dict(plan.labels),
            _CREATE_NONCE_LABEL: create_nonce,
        }
        labels = config.get("Labels") if isinstance(config, dict) else None
        reserved_labels = (
            {
                name: value
                for name, value in labels.items()
                if isinstance(name, str) and name.startswith("io.circular.")
            }
            if isinstance(labels, dict)
            else None
        )
        expected_nano_cpus = int(float(format(plan.cpu_limit, ".15g")) * 1_000_000_000)
        policy_matches = (
            isinstance(mounts, list)
            and len(mounts) == 1
            and isinstance(mounts[0], dict)
            and mounts[0].get("Type") == "bind"
            and mounts[0].get("Source") == str(plan.worktree_source)
            and mounts[0].get("Destination") == plan.worktree_destination
            and mounts[0].get("RW") is (not plan.worktree_read_only)
            and isinstance(config, dict)
            and reserved_labels == expected_labels
            and config.get("User") == plan.container_user
            and config.get("WorkingDir") == plan.working_directory
            and isinstance(host_config, dict)
            and host_config.get("NetworkMode") == plan.network_mode
            and host_config.get("ReadonlyRootfs") is plan.root_read_only
            and host_config.get("CapDrop") == list(plan.cap_drop)
            and host_config.get("SecurityOpt") == list(plan.security_options)
            and host_config.get("NanoCpus") == expected_nano_cpus
            and host_config.get("Memory") == plan.memory_limit_mb * 1024 * 1024
            and host_config.get("RestartPolicy") == {"Name": "no", "MaximumRetryCount": 0}
        )
        if not policy_matches:
            raise ContainerStartError(
                "Docker container policy does not match the resolved Run plan"
            )

    async def _reconcile_created_container(
        self,
        container_name: str,
        unresolved: _UnresolvedCreate,
    ) -> str | None:
        if not unresolved.ambiguous:
            result, stdout = await self._run_cli(
                ("container", "inspect", container_name),
                capture_stdout=True,
            )
            if result == 0:
                return _validated_reconciled_container(
                    _decoded_container_inspection(stdout),
                    container_name,
                    unresolved.nonce,
                    nonce_mismatch_is_conflict=True,
                )
            container_id = await self._find_container_by_create_nonce(
                container_name,
                unresolved.nonce,
            )
            if container_id is not None:
                return container_id
            if await self._container_name_is_absent(container_name):
                return None
            raise DockerOperationError("Docker could not reconcile the new Run container")

        loop = asyncio.get_running_loop()
        settle_seconds = min(
            max(self._operation_timeout_seconds, _MIN_CREATE_SETTLE_SECONDS),
            _MAX_CREATE_SETTLE_SECONDS,
        )
        deadline = loop.time() + settle_seconds
        while True:
            container_id = await self._find_container_by_create_nonce(
                container_name,
                unresolved.nonce,
            )
            if container_id is not None:
                return container_id

            result, stdout = await self._run_cli(
                ("container", "inspect", container_name),
                capture_stdout=True,
            )
            if result == 0:
                return _validated_reconciled_container(
                    _decoded_container_inspection(stdout),
                    container_name,
                    unresolved.nonce,
                    nonce_mismatch_is_conflict=True,
                )
            if loop.time() >= deadline:
                if await self._container_name_is_absent(container_name):
                    return None
                raise DockerOperationError(
                    "Docker could not settle the ambiguous Run container creation"
                )
            await asyncio.sleep(0.01)  # noqa: ASYNC110 -- bounded daemon reconciliation

    async def _find_container_by_create_nonce(
        self,
        container_name: str,
        create_nonce: str,
    ) -> str | None:
        result, stdout = await self._run_cli(
            (
                "container",
                "ls",
                "--all",
                "--quiet",
                "--no-trunc",
                "--filter",
                f"label={_CREATE_NONCE_LABEL}={create_nonce}",
            ),
            capture_stdout=True,
        )
        if result != 0:
            raise DockerOperationError("Docker could not reconcile the new Run container")
        identities = stdout.splitlines()
        if len(identities) > 1:
            raise DockerOperationError("Docker returned an invalid reconciliation response")
        if not identities:
            return None
        container_id = _validated_container_id(identities[0])
        container = await self._inspect_container(container_id)
        return _validated_reconciled_container(
            container,
            container_name,
            create_nonce,
            nonce_mismatch_is_conflict=False,
        )

    async def _container_name_is_absent(self, container_name: str) -> bool:
        result, stdout = await self._run_cli(
            (
                "container",
                "ls",
                "--all",
                "--quiet",
                "--no-trunc",
                "--filter",
                f"name=^/{container_name}$",
            ),
            capture_stdout=True,
        )
        if result != 0:
            raise DockerOperationError("Docker could not reconcile the new Run container")
        identities = stdout.splitlines()
        if any(_CONTAINER_ID.fullmatch(identity) is None for identity in identities):
            raise DockerOperationError("Docker returned an invalid reconciliation response")
        return not identities

    async def _inspect_container(
        self,
        reference: str,
    ) -> dict[str, object]:
        result, stdout = await self._run_cli(
            ("container", "inspect", reference),
            capture_stdout=True,
        )
        if result != 0:
            raise DockerOperationError("Docker could not inspect the new Run container")
        return _decoded_container_inspection(stdout)

    async def _monitor(self, execution: _Execution) -> None:
        process = execution.attach_process
        if process.stdout is None or process.stderr is None or process.stdin is None:
            raise AssertionError("attached Docker process is missing a standard stream")

        stdout_task = asyncio.create_task(
            _pump_output(process.stdout, OutputStream.STDOUT, execution.output_queue)
        )
        stderr_task = asyncio.create_task(
            _pump_output(process.stderr, OutputStream.STDERR, execution.output_queue)
        )
        try:
            async with asyncio.timeout(self._operation_timeout_seconds):
                await _write_stdin(process, execution.launch.plan.stdin)
            initial_state = await self._await_container_started(execution)
            if initial_state.status in {"exited", "dead"}:
                execution.terminal_observed = True
            if not execution.ready.done():
                execution.ready.set_result(None)

            await process.wait()
            async with asyncio.timeout(self._operation_timeout_seconds):
                await asyncio.gather(stdout_task, stderr_task)

            final_state = await self._container_state(execution.container_id)
            if final_state.status in {"exited", "dead"}:
                execution.terminal_observed = True
            elif final_state.status == "running":
                await self._contain_lost_attachment(execution)
                raise ContainerStartError("Docker lost the Run container attachment")
            else:
                raise ContainerStartError("Docker could not start the Run container")

            stopped = False
            if execution.stop_decision is not None:
                stopped = await asyncio.shield(execution.stop_decision)
            result = (
                RuntimeResult.stopped() if stopped else RuntimeResult.exited(final_state.exit_code)
            )
            if not execution.result.done():
                execution.result.set_result(result)
        except asyncio.CancelledError:
            await _terminate_process(process)
            error = ContainerStartError("Docker Run observation was cancelled")
            _set_exception_if_pending(execution.ready, error)
            _set_exception_if_pending(execution.result, error)
            raise
        except DockerRuntimeError as error:
            await _terminate_process(process)
            _set_exception_if_pending(execution.ready, error)
            _set_exception_if_pending(execution.result, error)
        except (OSError, TimeoutError, ValueError):
            await _terminate_process(process)
            error = ContainerStartError("Docker could not observe Run completion")
            _set_exception_if_pending(execution.ready, error)
            _set_exception_if_pending(execution.result, error)
        finally:
            tasks = (stdout_task, stderr_task)
            for task in tasks:
                if not task.done():
                    task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            await execution.output_queue.put(_OUTPUT_EOF)

    async def _await_container_started(self, execution: _Execution) -> _ContainerState:
        try:
            async with asyncio.timeout(self._operation_timeout_seconds):
                created_after_attach_exit = 0
                while True:
                    state = await self._container_state(execution.container_id)
                    if state.status in {"running", "exited", "dead"}:
                        return state
                    if execution.attach_process.returncode is not None:
                        created_after_attach_exit += 1
                        if created_after_attach_exit >= 3:
                            raise ContainerStartError("Docker could not start the Run container")
                    else:
                        created_after_attach_exit = 0
                    # Docker's create/start transition is external state; the
                    # adapter must not expose a handle while it remains created.
                    await asyncio.sleep(0.01)  # noqa: ASYNC110
        except TimeoutError as error:
            raise ContainerStartError("Docker did not start the Run container") from error

    async def _container_state(self, container_id: str) -> _ContainerState:
        result, stdout = await self._run_cli(
            (
                "container",
                "inspect",
                "--format",
                "{{.State.Status}} {{.State.ExitCode}}",
                container_id,
            ),
            capture_stdout=True,
        )
        if result != 0:
            raise DockerOperationError("Docker could not inspect the Run container")
        fields = stdout.split()
        if len(fields) != 2 or fields[0] not in {"created", "running", "exited", "dead"}:
            raise DockerOperationError("Docker returned an invalid Run container state")
        try:
            exit_code = int(fields[1])
        except ValueError as error:
            raise DockerOperationError("Docker returned an invalid Run container state") from error
        if exit_code < 0 or exit_code > 255:
            raise DockerOperationError("Docker returned an invalid Run container state")
        return _ContainerState(status=fields[0], exit_code=exit_code)

    async def _contain_lost_attachment(self, execution: _Execution) -> None:
        async with execution.stop_lock:
            if execution.terminal_observed:
                return
            stopped, failure = await self._stop_running_container(execution.container_id)
            if not stopped:
                raise ContainerStopError(
                    "Docker could not contain a detached Run container"
                ) from failure
            execution.terminal_observed = True

    async def _remove_new_container(self, container_id: str) -> None:
        result, _ = await self._run_cli(("rm", "--force", "--volumes", container_id))
        if result != 0:
            raise ContainerStartError("Docker could not remove the new Run container")

    async def _remove_new_container_safely(self, container_id: str) -> None:
        task = asyncio.create_task(self._remove_new_container(container_id))
        await _await_task_despite_cancellation(task)

    async def _run_cli(
        self,
        arguments: tuple[str, ...],
        *,
        environment: dict[str, str] | None = None,
        capture_stdout: bool = False,
        deadline_seconds: float | None | object = _DEFAULT_DEADLINE,
    ) -> tuple[int, str]:
        process = await self._spawn_cli(
            arguments,
            stdin=asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.PIPE if capture_stdout else asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.DEVNULL,
            environment=environment,
        )
        resolved_deadline = (
            self._operation_timeout_seconds
            if deadline_seconds is _DEFAULT_DEADLINE
            else deadline_seconds
        )
        try:
            if resolved_deadline is None:
                stdout, _ = await process.communicate()
            else:
                async with asyncio.timeout(resolved_deadline):
                    stdout, _ = await process.communicate()
        except asyncio.CancelledError:
            await _terminate_process(process)
            raise
        except TimeoutError as error:
            await _terminate_process(process)
            raise DockerOperationError("Docker operation exceeded its deadline") from error
        decoded = ""
        if stdout is not None:
            try:
                decoded = stdout.decode("utf-8")
            except UnicodeDecodeError as error:
                raise DockerOperationError("Docker returned an invalid response") from error
        return process.returncode or 0, decoded

    async def _spawn_cli(
        self,
        arguments: tuple[str, ...],
        *,
        stdin: int | None = None,
        stdout: int | None = None,
        stderr: int | None = None,
        environment: dict[str, str] | None = None,
    ) -> asyncio.subprocess.Process:
        executable = _resolve_executable(self._docker_executable)
        task = asyncio.create_task(
            asyncio.create_subprocess_exec(
                executable,
                *arguments,
                stdin=stdin,
                stdout=stdout,
                stderr=stderr,
                env=environment or self._minimal_cli_environment(),
                start_new_session=True,
            )
        )
        try:
            return await asyncio.shield(task)
        except asyncio.CancelledError as cancelled:
            try:
                process = await _await_task_despite_cancellation(task)
                cleanup = asyncio.create_task(_terminate_process(process))
                await _await_task_despite_cancellation(cleanup)
            finally:
                raise cancelled
        except OSError as error:
            raise DockerOperationError("Docker CLI is unavailable") from error

    def _minimal_cli_environment(self, extra: tuple[tuple[str, str], ...] = ()) -> dict[str, str]:
        return {"PATH": os.defpath, **dict(extra)}

    def _execution(self, handle: ContainerHandle) -> _Execution:
        if not isinstance(handle, ContainerHandle):
            raise UnknownContainerHandleError("container handle is invalid")
        try:
            execution = self._executions[handle.id]
        except KeyError as error:
            raise UnknownContainerHandleError(
                "container handle is not owned by this adapter instance"
            ) from error
        if execution.handle != handle:
            raise UnknownContainerHandleError(
                "container handle resource identity does not match this execution"
            )
        return execution


def _validated_root(root: Path) -> Path:
    candidate = Path(root)
    if not candidate.is_absolute():
        raise InvalidDockerConfiguration("Docker worktree root must be absolute")
    if candidate == Path(candidate.anchor):
        raise InvalidDockerConfiguration("Docker worktree root cannot be the filesystem root")
    if candidate.is_symlink():
        raise InvalidDockerConfiguration("Docker worktree root cannot be a symlink")
    resolved = candidate.resolve(strict=False)
    resolved_text = str(resolved)
    if not _is_utf8(resolved_text) or "," in resolved_text or "\0" in resolved_text:
        raise InvalidDockerConfiguration("Docker worktree root contains unsupported characters")
    return resolved


def _validated_container_id(stdout: str) -> str:
    container_id = stdout.strip()
    if _CONTAINER_ID.fullmatch(container_id) is None:
        raise ContainerStartError("Docker returned an invalid container identity")
    return container_id


def _decoded_container_inspection(stdout: str) -> dict[str, object]:
    try:
        response = json.loads(stdout)
    except json.JSONDecodeError as error:
        raise DockerOperationError("Docker returned an invalid container inspection") from error
    if not isinstance(response, list) or len(response) != 1 or not isinstance(response[0], dict):
        raise DockerOperationError("Docker returned an invalid container inspection")
    return response[0]


def _validated_reconciled_container(
    container: dict[str, object],
    container_name: str,
    create_nonce: str,
    *,
    nonce_mismatch_is_conflict: bool,
) -> str:
    container_id = container.get("Id")
    if not isinstance(container_id, str):
        raise DockerOperationError("Docker returned an invalid container inspection")
    immutable_id = _validated_container_id(container_id)
    config = container.get("Config")
    if not isinstance(config, dict):
        raise DockerOperationError("Docker returned an invalid container inspection")
    labels = config.get("Labels")
    if not isinstance(labels, dict) or labels.get(_CREATE_NONCE_LABEL) != create_nonce:
        if nonce_mismatch_is_conflict:
            raise ContainerNameConflictError(
                "the deterministic Run container name is already occupied"
            )
        raise DockerOperationError("Docker returned an invalid container inspection")
    if container.get("Name") != f"/{container_name}":
        raise DockerOperationError("Docker returned an invalid container inspection")
    return immutable_id


def _validated_allowlist(names: frozenset[str]) -> frozenset[str]:
    if not isinstance(names, frozenset):
        raise InvalidDockerConfiguration("environment allowlist must be a frozenset")
    for name in names:
        if not isinstance(name, str) or _ENVIRONMENT_NAME.fullmatch(name) is None:
            raise InvalidDockerConfiguration("environment allowlist contains an invalid name")
        if _is_sensitive_environment_name(name):
            raise InvalidDockerConfiguration(
                "control-plane environment names cannot be allowlisted"
            )
    return names


def _validate_run_id(run_id: UUID) -> UUID:
    if not isinstance(run_id, UUID):
        raise InvalidContainerSpec("Run identity must be a UUID")
    return run_id


def _validated_worktree(root: Path, worktree: Path, run_id: UUID) -> Path:
    candidate = Path(worktree)
    expected = root / str(run_id)
    if not candidate.is_absolute() or candidate.parent != root or candidate.name != str(run_id):
        raise InvalidContainerSpec("worktree must be the Run UUID child of the allowed root")
    if candidate.is_symlink():
        raise InvalidContainerSpec("worktree cannot be a symlink")
    resolved = candidate.resolve(strict=False)
    if resolved != expected:
        raise InvalidContainerSpec("worktree must remain inside the allowed root")
    resolved_text = str(resolved)
    if not _is_utf8(resolved_text) or "," in resolved_text or "\0" in resolved_text:
        raise InvalidContainerSpec("worktree path contains unsupported characters")
    return resolved


def _validated_image(image: str) -> str:
    if not isinstance(image, str) or _IMAGE_REFERENCE.fullmatch(image) is None:
        raise InvalidContainerSpec("container image reference is invalid")
    return image


def _validated_command(command: tuple[str, ...]) -> tuple[str, ...]:
    if not isinstance(command, tuple):
        raise InvalidContainerSpec("container command must be an immutable tuple")
    for argument in command:
        if not isinstance(argument, str) or "\0" in argument or not _is_utf8(argument):
            raise InvalidContainerSpec("container command contains an invalid argument")
    return command


def _validated_stdin(stdin: bytes) -> bytes:
    if not isinstance(stdin, bytes):
        raise InvalidContainerSpec("container stdin must be immutable bytes")
    return stdin


def _validated_cpu_limit(limit: float) -> float:
    if isinstance(limit, bool) or not isinstance(limit, (int, float)):
        raise InvalidContainerSpec("CPU limit must be numeric")
    value = float(limit)
    if not math.isfinite(value) or value <= 0:
        raise InvalidContainerSpec("CPU limit must be a positive finite value")
    return value


def _validated_memory_limit(limit: int) -> int:
    if isinstance(limit, bool) or not isinstance(limit, int) or limit <= 0:
        raise InvalidContainerSpec("memory limit must be a positive integer")
    return limit


def _validated_timeout(value: float, name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise InvalidDockerConfiguration(f"{name} must be a positive finite value")
    resolved = float(value)
    if not math.isfinite(resolved) or resolved <= 0:
        raise InvalidDockerConfiguration(f"{name} must be a positive finite value")
    return resolved


def _validated_environment(
    environment: dict[str, str], allowlist: frozenset[str]
) -> tuple[tuple[str, str], ...]:
    if not isinstance(environment, dict):
        raise InvalidContainerSpec("container environment must be a dictionary")
    for name, value in environment.items():
        if not isinstance(name, str) or _ENVIRONMENT_NAME.fullmatch(name) is None:
            raise InvalidContainerSpec("container environment contains an invalid name")
        if _is_sensitive_environment_name(name):
            raise InvalidContainerSpec("control-plane environment is not permitted")
        if name not in allowlist:
            raise InvalidContainerSpec("container environment name is not allowlisted")
        if not isinstance(value, str) or "\0" in value or not _is_utf8(value):
            raise InvalidContainerSpec("container environment contains an invalid value")
    return tuple(sorted(environment.items()))


def _is_sensitive_environment_name(name: str) -> bool:
    normalized = name.upper()
    return (
        normalized in _SENSITIVE_ENVIRONMENT_NAMES
        or normalized.startswith("CIRCULAR_")
        or normalized.startswith("DOCKER_")
        or normalized.startswith("SSH_")
        or normalized.startswith("XDG_")
        or normalized.startswith("LD_")
        or normalized.startswith("DYLD_")
        or normalized.startswith("SSL_CERT_")
        or normalized.startswith("PYTHON")
    )


def _is_utf8(value: str) -> bool:
    try:
        value.encode("utf-8")
    except UnicodeEncodeError:
        return False
    return True


def _policy_digest(
    *,
    run_id: UUID,
    image: str,
    command: tuple[str, ...],
    environment: tuple[tuple[str, str], ...],
    worktree: Path,
    network_enabled: bool,
    cpu_limit: float,
    memory_limit_mb: int,
    container_user: str,
) -> str:
    document = {
        "command": command,
        "container_user": container_user,
        "cpu_limit": format(cpu_limit, ".15g"),
        # Values stay only in the adapter's in-memory launch snapshot. Persisted
        # labels must not become an offline oracle for scoped secrets.
        "environment_names": [name for name, _ in environment],
        "image": image,
        "memory_limit_mb": memory_limit_mb,
        "network_enabled": network_enabled,
        "run_id": str(run_id),
        "worktree": str(worktree),
    }
    encoded = json.dumps(
        document,
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("ascii")
    return hashlib.sha256(encoded).hexdigest()


def _resolve_executable(configured: str) -> str:
    if os.sep in configured:
        candidate = Path(configured)
        if not candidate.is_absolute():
            raise DockerOperationError("Docker CLI path must be absolute")
        resolved = candidate.resolve(strict=False)
        if not resolved.is_file() or not os.access(resolved, os.X_OK):
            raise DockerOperationError("Docker CLI is unavailable")
        return str(resolved)
    executable = shutil.which(configured, path=os.defpath)
    if executable is None:
        raise DockerOperationError("Docker CLI is unavailable")
    return executable


async def _pump_output(
    reader: asyncio.StreamReader,
    stream: OutputStream,
    queue: asyncio.Queue[RuntimeOutput | object],
) -> None:
    while data := await reader.read(64 * 1024):
        await queue.put(RuntimeOutput(stream=stream, data=data))


async def _write_stdin(process: asyncio.subprocess.Process, data: bytes) -> None:
    writer = process.stdin
    if writer is None:
        raise AssertionError("attached Docker process has no stdin")
    try:
        writer.write(data)
        await writer.drain()
    except (BrokenPipeError, ConnectionResetError):
        pass
    writer.close()
    with suppress(BrokenPipeError, ConnectionResetError):
        await writer.wait_closed()


async def _terminate_process(process: asyncio.subprocess.Process) -> None:
    if process.returncode is not None:
        return
    with suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGTERM)
    try:
        async with asyncio.timeout(1):
            await process.wait()
            return
    except TimeoutError:
        pass
    with suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGKILL)
    await process.wait()


async def _await_task_despite_cancellation[T](task: asyncio.Task[T]) -> T:
    """Finish owned cleanup even if the calling task receives repeated cancellation."""

    while not task.done():
        try:
            await asyncio.shield(task)
        except asyncio.CancelledError:
            continue
    return task.result()


def _set_exception_if_pending[T](future: asyncio.Future[T], error: DockerRuntimeError) -> None:
    if not future.done():
        future.set_exception(error)


def _consume_future[T](future: asyncio.Future[T]) -> None:
    if not future.done():
        future.cancel()
    elif not future.cancelled():
        future.exception()
