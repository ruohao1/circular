"""Milestone proof using real PostgreSQL, Git, Docker, and HTTP routes."""

import asyncio
import io
import json
import os
import subprocess
import tarfile
from contextlib import asynccontextmanager, suppress
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import UUID, uuid4

import httpx
import pytest
from circular.api.config import get_settings
from circular.api.dependencies import get_session
from circular.api.main import app
from circular.domain import RunStatus, Workspace
from circular.git import LocalRepositoryCache
from circular.runners import (
    ExecutionDirectories,
    FakeWorkloadSpecFactory,
)
from circular.runtimes import DockerRuntimeError
from circular.storage import (
    ProjectRecord,
    RunRecord,
    TaskRecord,
    WorkspaceStore,
    create_engine,
    create_session_factory,
)
from circular.worker.execution import build_supervisor
from sqlalchemy import delete, select

DATABASE_URL = os.getenv("TEST_DATABASE_URL")
pytestmark = pytest.mark.skipif(
    not DATABASE_URL or os.getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1",
    reason="TEST_DATABASE_URL and CIRCULAR_RUN_DOCKER_TESTS=1 are required",
)
ROOT = Path(__file__).resolve().parents[1]
IMAGE = "circular-isq162-runner:test"


def command(*argv: str) -> str:
    return subprocess.run(argv, check=True, capture_output=True, text=True, timeout=120).stdout


@pytest.fixture(scope="module", autouse=True)
def runner_image():
    command(
        "docker",
        "build",
        "-f",
        str(ROOT / "infra/fake-agent-workload.Dockerfile"),
        "-t",
        IMAGE,
        str(ROOT),
    )


def execution_system(engine, directories, worker_id="isq162-test-worker", image=IMAGE):
    supervisor = build_supervisor(
        engine,
        directories,
        worker_id,
        spec_factory=FakeWorkloadSpecFactory(image=image, cpu_limit=1, memory_limit_mb=256),
        poll_seconds=0.05,
    )
    return supervisor.store, supervisor, supervisor.runtime


@asynccontextmanager
async def system(tmp_path: Path, *, behavior=None, image=IMAGE):
    engine = create_engine(DATABASE_URL)
    sessions = create_session_factory(engine)

    async def session_override():
        async with sessions() as session:
            yield session

    app.dependency_overrides[get_session] = session_override
    directories = ExecutionDirectories(
        tmp_path / "repositories",
        tmp_path / "worktrees",
        tmp_path / "artifacts",
        tmp_path / "worktrees",
    )
    old_artifact_root = get_settings().artifact_root
    get_settings().artifact_root = directories.artifact_root
    source = tmp_path / "source"
    source.mkdir()
    command("git", "init", "--initial-branch=main", str(source))
    command("git", "-C", str(source), "config", "user.name", "Circular Test")
    command("git", "-C", str(source), "config", "user.email", "test@example.test")
    source.joinpath("README.md").write_text("initial\n")
    command("git", "-C", str(source), "add", ".")
    command("git", "-C", str(source), "commit", "-m", "initial")
    store, supervisor, runtime = execution_system(engine, directories, image=image)
    project_id = None
    run_id = None
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test/api/v1"
    ) as client:
        try:

            async def post(path, body):
                response = await client.post(path, json=body)
                assert response.status_code == 201, response.text
                return response.json()

            project = await post("projects", {"name": f"__isq162_test_{uuid4()}"})
            project_id = UUID(project["id"])
            repository = await post(
                "repositories",
                {"project_id": str(project_id), "name": "fixture", "clone_url": str(source)},
            )
            agent = await post(
                "agents",
                {
                    "project_id": str(project_id),
                    "name": "Engineer",
                    "backend_config": behavior or {},
                },
            )
            task = await post(
                "tasks",
                {
                    "project_id": str(project_id),
                    "repository_id": repository["id"],
                    "title": "Prove isolated execution",
                },
            )
            run = await post("runs", {"task_id": task["id"], "agent_id": agent["id"]})
            run_id = UUID(run["id"])
            yield client, sessions, store, supervisor, runtime, directories, run_id
        finally:
            if run_id is not None:
                await runtime.release(run_id, None)
            if project_id is not None:
                async with sessions.begin() as session:
                    tasks = select(TaskRecord.id).where(TaskRecord.project_id == project_id)
                    await session.execute(delete(RunRecord).where(RunRecord.task_id.in_(tasks)))
                    await session.execute(
                        delete(ProjectRecord).where(ProjectRecord.id == project_id)
                    )
            app.dependency_overrides.pop(get_session, None)
            get_settings().artifact_root = old_artifact_root
            await engine.dispose()


async def claim(sessions, store, run_id, worker_id="isq162-test-worker"):
    async with sessions.begin() as session:
        run = await session.get(RunRecord, run_id)
        run.created_at = datetime(1700, 1, 1, tzinfo=UTC)
    async with sessions.begin() as session:
        claimed = await store.claim_next(session, worker_id)
        assert claimed.id == run_id
        assert claimed.status == "provisioning"


async def wait_for(client, run_id, statuses):
    async with asyncio.timeout(20):
        while True:
            response = await client.get(f"runs/{run_id}/execution")
            detail = response.json()
            if detail["run"]["status"] in statuses:
                return detail
            await asyncio.sleep(0.02)


async def test_success_retains_diff_artifacts_and_replay_after_real_cleanup(tmp_path):
    async with system(tmp_path, behavior={"delay_ms": 250}) as setup:
        client, sessions, store, supervisor, runtime, directories, run_id = setup
        await claim(sessions, store, run_id)
        job = asyncio.create_task(supervisor.run(run_id, asyncio.Event()))
        live = await wait_for(client, run_id, {"running"})
        assert live["workspace"]["status"] == "ready"
        container_id = live["workspace"]["container_id"]
        await asyncio.wait_for(job, 30)
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "succeeded"
        assert detail["workspace"]["status"] == "released"
        assert detail["usage"]["output_tokens"] > 0
        assert {a["kind"] for a in detail["artifacts"]} == {"diff", "workspace"}
        for artifact in detail["artifacts"]:
            response = await client.get(f"runs/{run_id}/artifacts/{artifact['id']}/content")
            assert response.status_code == 200
            if artifact["kind"] == "diff":
                assert (
                    b"+Fake container workload completed: Prove isolated execution"
                    in response.content
                )
            else:
                with tarfile.open(fileobj=io.BytesIO(response.content)) as archive:
                    assert f"circular-result-{run_id}.txt" in archive.getnames()
                    assert ".git" not in archive.getnames()
        events = (await client.get(f"runs/{run_id}/events")).json()
        assert [e["sequence"] for e in events] == list(range(1, len(events) + 1))
        types = [e["type"] for e in events]
        assert types.index("git.diff.updated") < types.index("run.completed")
        assert types.index("run.completed") < types.index("workspace.released")
        assert (await client.get(f"runs/{run_id}/events?after=3")).json() == events[3:]
        assert not directories.run_paths(run_id).worktree.exists()
        assert container_id not in command("docker", "ps", "-aq", "--no-trunc")
        assert (await client.get(f"runs?project_id={detail['task']['project_id']}")).json()[0][
            "id"
        ] == str(run_id)


async def test_large_base_checkout_retains_output_and_releases_worktree(tmp_path):
    async with system(tmp_path) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        source = tmp_path / "source"
        with (source / "large-base.bin").open("wb") as content:
            content.truncate(33 * 1024 * 1024)
        command("git", "-C", str(source), "add", "large-base.bin")
        command("git", "-C", str(source), "commit", "-m", "large base checkout")
        await claim(sessions, store, run_id)
        await supervisor.run(run_id, asyncio.Event())

        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "succeeded"
        assert detail["workspace"]["status"] == "released"
        artifact = next(item for item in detail["artifacts"] if item["kind"] == "workspace")
        assert artifact["metadata"]["size_bytes"] > 32 * 1024 * 1024
        response = await client.get(f"runs/{run_id}/artifacts/{artifact['id']}/content")
        assert response.status_code == 200
        with tarfile.open(fileobj=io.BytesIO(response.content)) as archive:
            assert archive.getmember("large-base.bin").size == 33 * 1024 * 1024
            assert f"circular-result-{run_id}.txt" in archive.getnames()
        assert not directories.run_paths(run_id).worktree.exists()


async def test_expired_provisioning_claim_reconciles_staging_before_workspace_release(tmp_path):
    async with system(tmp_path) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        await claim(sessions, store, run_id)
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        repository_id = UUID(detail["task"]["repository_id"])
        repository = await LocalRepositoryCache(directories).checkout(
            repository_id, str(tmp_path / "source")
        )
        target = directories.run_paths(run_id).worktree
        async with sessions.begin() as session:
            await WorkspaceStore().create(
                session,
                Workspace(id=uuid4(), run_id=run_id, worktree_path=str(target)),
                source="test-worker",
            )
        staging = target.with_name(f".{run_id}.worktree-crash")
        command(
            "git",
            "-C",
            str(repository),
            "worktree",
            "add",
            "-b",
            f"circular/run/{run_id}",
            str(staging),
            "main",
        )
        async with sessions.begin() as session:
            run = await session.get(RunRecord, run_id)
            run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)
        async with supervisor.sessions.begin() as session:
            recovered = await store.recover_expired(session, "isq162-test-worker")
            assert recovered.id == run_id
        await supervisor.run(run_id, asyncio.Event(), recovery=True)

        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert detail["workspace"]["status"] == "released"
        assert not staging.exists()
        assert str(staging) not in command("git", "-C", str(repository), "worktree", "list")
        async with sessions() as session:
            assert (await session.get(RunRecord, run_id)).worker_id is None


@pytest.mark.parametrize("persistent", [False, True])
async def test_terminal_write_outage_never_abandons_a_nonterminal_claim(
    tmp_path, monkeypatch, persistent
):
    async with system(tmp_path) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        original = store.transition
        failures = 0

        async def outage(session, identifier, status, **kwargs):
            nonlocal failures
            if status in {RunStatus.SUCCEEDED, RunStatus.FAILED} and (persistent or failures < 2):
                failures += 1
                raise ConnectionError("injected terminal-write outage")
            return await original(session, identifier, status, **kwargs)

        monkeypatch.setattr(store, "transition", outage)
        await claim(sessions, store, run_id)
        await supervisor.run(run_id, asyncio.Event())
        if persistent:
            async with sessions.begin() as session:
                assert await store.release_claim(session, run_id) is False
                run = await session.get(RunRecord, run_id)
                assert run.status == "finalizing"
                assert run.worker_id == "isq162-test-worker"
                assert run.lease_expires_at is not None
                run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)
            assert directories.run_paths(run_id).worktree.exists()
            monkeypatch.setattr(store, "transition", original)
            async with supervisor.sessions.begin() as session:
                recovered = await store.recover_expired(session, "isq162-test-worker")
                assert recovered.id == run_id
            await supervisor.run(run_id, asyncio.Event(), recovery=True)

        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert detail["run"]["error"]
        assert detail["workspace"]["status"] == "released"
        async with sessions() as session:
            run = await session.get(RunRecord, run_id)
            assert run.worker_id is None
            assert run.lease_expires_at is None
        events = (await client.get(f"runs/{run_id}/events")).json()
        assert [event["type"] for event in events].count("run.failed") == 1
        assert not any(event["type"] == "run.completed" for event in events)
        assert not directories.run_paths(run_id).worktree.exists()


@pytest.mark.parametrize("failure", ["before_events", "after_first_event"])
async def test_backend_failure_is_auditable_and_leak_free(tmp_path, failure):
    async with system(tmp_path, behavior={"failure": failure}) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        await claim(sessions, store, run_id)
        await supervisor.run(run_id, asyncio.Event())
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert "injected_failure" in detail["run"]["error"]
        assert detail["workspace"]["status"] == "released"
        events = (await client.get(f"runs/{run_id}/events")).json()
        assert "run.completed" not in [e["type"] for e in events]
        assert not directories.run_paths(run_id).worktree.exists()


async def test_active_cancellation_stops_runtime_once_and_releases_resources(tmp_path):
    async with system(tmp_path, behavior={"delay_ms": 5000}) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        await claim(sessions, store, run_id)
        job = asyncio.create_task(supervisor.run(run_id, asyncio.Event()))
        await wait_for(client, run_id, {"running"})
        assert (await client.post(f"runs/{run_id}/cancel")).status_code == 200
        assert (await client.post(f"runs/{run_id}/cancel")).status_code == 200
        await asyncio.wait_for(job, 15)
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "cancelled"
        assert detail["workspace"]["status"] == "released"
        events = (await client.get(f"runs/{run_id}/events")).json()
        types = [e["type"] for e in events]
        assert types.count("run.cancelled") == 1
        assert "run.completed" not in types
        assert not directories.run_paths(run_id).worktree.exists()


async def test_queued_cancellation_never_provisions(tmp_path):
    async with system(tmp_path) as setup:
        client, sessions, store, _, _, directories, run_id = setup
        await client.post(f"runs/{run_id}/cancel")
        async with sessions.begin() as session:
            assert await store.claim_next(session, "other-worker") is None
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "cancelled"
        assert detail["workspace"] is None
        assert not directories.run_paths(run_id).worktree.exists()


async def test_provisioning_failure_releases_the_allocated_worktree(tmp_path):
    async with system(tmp_path, image="circular-missing-image:isq162") as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        await claim(sessions, store, run_id)
        await supervisor.run(run_id, asyncio.Event())
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert detail["run"]["error"]
        assert detail["workspace"]["status"] == "released"
        assert not directories.run_paths(run_id).worktree.exists()


async def test_live_lease_is_not_stolen_and_expired_lease_is_recovered_once(tmp_path):
    async with system(tmp_path) as setup:
        client, sessions, store, supervisor, _, _, run_id = setup
        await claim(sessions, store, run_id)
        async with sessions.begin() as session:
            assert await store.recover_expired(session, "recovery") is None
            run = await session.get(RunRecord, run_id)
            run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)

        async def recover():
            async with sessions.begin() as session:
                run = await store.recover_expired(session, "isq162-test-worker")
                return run.id if run else None

        results = await asyncio.gather(recover(), recover())
        assert results.count(run_id) == 1
        await supervisor.run(run_id, asyncio.Event(), recovery=True)
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert detail["run"]["error"] == "worker lease expired"


async def test_two_workers_have_distinct_resources_and_cancellation_is_scoped(tmp_path):
    async with system(tmp_path, behavior={"delay_ms": 5000}) as setup:
        client, sessions, store, supervisor, _, directories, first_id = setup
        first = (await client.get(f"runs/{first_id}/execution")).json()
        response = await client.post(
            "runs",
            json={
                "task_id": first["task"]["id"],
                "agent_id": first["agent"]["id"],
            },
        )
        assert response.status_code == 201
        second_id = UUID(response.json()["id"])
        other_engine = create_engine(DATABASE_URL)
        _, other, other_runtime = execution_system(other_engine, directories, "second-worker")
        jobs = []
        try:
            await claim(sessions, store, first_id)
            await claim(sessions, store, second_id, "second-worker")
            jobs = [
                asyncio.create_task(worker.run(run_id, asyncio.Event()))
                for worker, run_id in [(supervisor, first_id), (other, second_id)]
            ]
            live = await asyncio.gather(
                *(wait_for(client, run_id, {"running"}) for run_id in [first_id, second_id])
            )
            identities = [item["workspace"]["container_id"] for item in live]
            assert identities[0] != identities[1]
            for run_id, container_id in zip([first_id, second_id], identities, strict=True):
                container = json.loads(command("docker", "inspect", container_id))[0]
                assert [
                    (mount["Source"], mount["Destination"]) for mount in container["Mounts"]
                ] == [(str(directories.run_paths(run_id).worktree), "/workspace")]
                assert container["HostConfig"]["NetworkMode"] == "none"
                assert container["HostConfig"]["ReadonlyRootfs"] is True
                assert container["Config"]["User"] not in {"", "0", "root", "0:0"}
                probe = command(
                    "docker",
                    "exec",
                    container_id,
                    "python",
                    "-c",
                    "import os; from pathlib import Path; "
                    "assert not Path('/var/run/docker.sock').exists(); "
                    "assert not os.path.exists('/root/.ssh'); "
                    "assert 'DATABASE_URL' not in os.environ; "
                    "print(*(p.name for p in Path('/workspace').glob('circular-result-*')))",
                )
                assert f"circular-result-{run_id}.txt" in probe
                other_id = second_id if run_id == first_id else first_id
                assert str(other_id) not in probe
            await client.post(f"runs/{first_id}/cancel")
            await asyncio.wait_for(jobs[0], 20)
            assert directories.run_paths(second_id).worktree.exists()
            assert command("docker", "inspect", identities[1])
            await asyncio.wait_for(jobs[1], 30)
            for run_id in [first_id, second_id]:
                detail = (await client.get(f"runs/{run_id}/execution")).json()
                assert detail["workspace"]["status"] == "released"
                assert not directories.run_paths(run_id).worktree.exists()
                artifact_id = detail["artifacts"][0]["id"]
                foreign_id = second_id if run_id == first_id else first_id
                assert (
                    await client.get(f"runs/{foreign_id}/artifacts/{artifact_id}/content")
                ).status_code == 404
        finally:
            for run_id in [first_id, second_id]:
                await client.post(f"runs/{run_id}/cancel")
            if jobs:
                await asyncio.wait_for(asyncio.gather(*jobs), 30)
            await other_runtime.release(second_id, None)
            await other_engine.dispose()


@pytest.mark.parametrize("stage", ["runtime", "retention", "worktree"])
async def test_cleanup_failure_preserves_primary_error_and_can_be_retried(
    tmp_path, monkeypatch, stage
):
    async with system(tmp_path, behavior={"failure": "after_first_event"}) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        cleaner = supervisor.cleaner
        target, method = {
            "runtime": (cleaner.runtime, "release"),
            "retention": (cleaner, "_retain_output"),
            "worktree": (cleaner.worktrees, "release"),
        }[stage]
        original = getattr(target, method)

        async def fail(*args, **kwargs):
            raise RuntimeError("injected cleanup failure")

        monkeypatch.setattr(target, method, fail)
        await claim(sessions, store, run_id)
        await supervisor.run(run_id, asyncio.Event())
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "failed"
        assert "injected_failure" in detail["run"]["error"]
        assert detail["workspace"]["status"] == "failed"
        assert directories.run_paths(run_id).worktree.exists()
        events = (await client.get(f"runs/{run_id}/events")).json()
        assert any(
            e["type"] == "workspace.failed" and e["data"].get("stage") == "cleanup" for e in events
        )
        monkeypatch.setattr(target, method, original)
        assert await cleaner.cleanup(run_id)
        assert await cleaner.cleanup(run_id)
        assert not directories.run_paths(run_id).worktree.exists()


async def test_crashed_worker_with_live_container_is_fenced_and_reconciled(tmp_path):
    from circular.storage.repositories import RunLeaseLostError

    async with system(tmp_path, behavior={"delay_ms": 5000}) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        await claim(sessions, store, run_id)
        provisioned = await supervisor.provisioner.provision(run_id)
        replacement_engine = create_engine(DATABASE_URL)
        _, replacement, replacement_runtime = execution_system(
            replacement_engine, directories, "replacement-worker"
        )
        try:
            # Keep a stale ORM object alive to verify the ownership check refreshes it.
            async with supervisor.sessions() as stale_session:
                stale = await stale_session.get(RunRecord, run_id)
                async with sessions.begin() as session:
                    run = await session.get(RunRecord, run_id)
                    run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)
                async with replacement.sessions.begin() as session:
                    recovered = await store.recover_expired(session, "replacement-worker")
                    assert recovered.id == run_id
                assert stale.worker_id == "isq162-test-worker"
                with pytest.raises(RunLeaseLostError):
                    await store.lock_for_execution(stale_session, run_id)
            assert await supervisor.cleaner.cleanup(run_id) is False
            assert command("docker", "inspect", provisioned.handle.resource_id)
            await replacement.run(run_id, asyncio.Event(), recovery=True)
            # A real crashed process is gone; consume this test's old observer.
            with suppress(DockerRuntimeError):
                await supervisor.runtime.wait(provisioned.handle)
            detail = (await client.get(f"runs/{run_id}/execution")).json()
            assert detail["run"]["status"] == "failed"
            assert detail["workspace"]["status"] == "released"
            assert not directories.run_paths(run_id).worktree.exists()
        finally:
            await replacement_runtime.release(run_id, provisioned.handle.resource_id)
            await replacement_engine.dispose()


async def test_recovery_attempts_are_bounded_and_do_not_duplicate_terminal_events(tmp_path):
    async with system(tmp_path) as setup:
        client, sessions, store, _, _, _, run_id = setup
        await claim(sessions, store, run_id)
        for attempt in range(1, 4):
            async with sessions.begin() as session:
                run = await session.get(RunRecord, run_id)
                run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)
            async with sessions.begin() as session:
                recovered = await store.recover_expired(session, f"recovery-{attempt}")
                assert recovered.id == run_id
                assert recovered.recovery_attempts == attempt
        async with sessions.begin() as session:
            run = await session.get(RunRecord, run_id)
            run.lease_expires_at = datetime.now(UTC) - timedelta(seconds=1)
        async with sessions.begin() as session:
            assert await store.recover_expired(session, "fourth-recovery") is None
        events = (await client.get(f"runs/{run_id}/events")).json()
        assert [e["type"] for e in events].count("run.failed") == 1


async def test_cancellation_during_provisioning_releases_pending_workspace(tmp_path, monkeypatch):
    async with system(tmp_path) as setup:
        client, sessions, store, supervisor, _, directories, run_id = setup
        entered = asyncio.Event()

        async def waiting_checkout(*args, **kwargs):
            entered.set()
            await asyncio.Event().wait()

        monkeypatch.setattr(supervisor.provisioner._repository_cache, "checkout", waiting_checkout)
        await claim(sessions, store, run_id)
        job = asyncio.create_task(supervisor.run(run_id, asyncio.Event()))
        await asyncio.wait_for(entered.wait(), 10)
        await client.post(f"runs/{run_id}/cancel")
        await asyncio.wait_for(job, 15)
        detail = (await client.get(f"runs/{run_id}/execution")).json()
        assert detail["run"]["status"] == "cancelled"
        assert detail["workspace"]["status"] == "released"
        assert detail["workspace"]["container_id"] is None
        assert not directories.run_paths(run_id).worktree.exists()
