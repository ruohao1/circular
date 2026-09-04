import asyncio
import hashlib
import json
from collections.abc import AsyncIterator
from uuid import UUID

from circular.api.config import get_settings
from circular.api.dependencies import get_run_event_reader, get_session
from circular.api.schemas import (
    AgentCreate,
    AgentRead,
    EventRead,
    ProjectCreate,
    ProjectRead,
    RepositoryCreate,
    RepositoryRead,
    RunCreate,
    RunExecutionRead,
    RunRead,
    TaskCreate,
    TaskRead,
)
from circular.domain import RunStatus
from circular.storage import (
    AgentRecord,
    ArtifactContentError,
    ArtifactStore,
    EventRecord,
    LocalArtifactContentStore,
    ProjectRecord,
    RepositoryRecord,
    RunEventReader,
    RunRecord,
    TaskRecord,
)
from circular.storage.models import ArtifactRecord, WorkspaceRecord
from circular.storage.repositories import RunStore
from fastapi import APIRouter, Depends, Header, HTTPException, Query, Request, status
from fastapi.responses import Response, StreamingResponse
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

router = APIRouter()


async def _list(session: AsyncSession, model: type, project_id: UUID | None = None):
    statement = select(model).order_by(model.created_at.desc())
    if project_id is not None:
        statement = statement.where(model.project_id == project_id)
    return list(await session.scalars(statement))


@router.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@router.post("/projects", response_model=ProjectRead, status_code=status.HTTP_201_CREATED)
async def create_project(
    body: ProjectCreate, session: AsyncSession = Depends(get_session)
) -> ProjectRecord:
    record = ProjectRecord(**body.model_dump())
    session.add(record)
    await session.commit()
    await session.refresh(record)
    return record


@router.get("/projects", response_model=list[ProjectRead])
async def list_projects(session: AsyncSession = Depends(get_session)):
    return await _list(session, ProjectRecord)


@router.post("/repositories", response_model=RepositoryRead, status_code=status.HTTP_201_CREATED)
async def create_repository(
    body: RepositoryCreate, session: AsyncSession = Depends(get_session)
) -> RepositoryRecord:
    await _require(session, ProjectRecord, body.project_id, "project")
    record = RepositoryRecord(**body.model_dump())
    session.add(record)
    await session.commit()
    await session.refresh(record)
    return record


@router.get("/repositories", response_model=list[RepositoryRead])
async def list_repositories(
    project_id: UUID | None = None, session: AsyncSession = Depends(get_session)
):
    return await _list(session, RepositoryRecord, project_id)


@router.post("/agents", response_model=AgentRead, status_code=status.HTTP_201_CREATED)
async def create_agent(
    body: AgentCreate, session: AsyncSession = Depends(get_session)
) -> AgentRecord:
    await _require(session, ProjectRecord, body.project_id, "project")
    if body.backend != "fake":
        raise HTTPException(status_code=422, detail="only the fake backend is available")
    record = AgentRecord(**body.model_dump())
    session.add(record)
    await session.commit()
    await session.refresh(record)
    return record


@router.get("/agents", response_model=list[AgentRead])
async def list_agents(project_id: UUID | None = None, session: AsyncSession = Depends(get_session)):
    return await _list(session, AgentRecord, project_id)


@router.post("/tasks", response_model=TaskRead, status_code=status.HTTP_201_CREATED)
async def create_task(body: TaskCreate, session: AsyncSession = Depends(get_session)) -> TaskRecord:
    await _require(session, ProjectRecord, body.project_id, "project")
    if body.repository_id is not None:
        repository = await _require(session, RepositoryRecord, body.repository_id, "repository")
        if repository.project_id != body.project_id:
            raise HTTPException(status_code=422, detail="repository belongs to another project")
    record = TaskRecord(**body.model_dump())
    session.add(record)
    await session.commit()
    await session.refresh(record)
    return record


@router.get("/tasks", response_model=list[TaskRead])
async def list_tasks(project_id: UUID | None = None, session: AsyncSession = Depends(get_session)):
    return await _list(session, TaskRecord, project_id)


@router.post("/runs", response_model=RunRead, status_code=status.HTTP_201_CREATED)
async def create_run(body: RunCreate, session: AsyncSession = Depends(get_session)) -> RunRecord:
    task = await session.scalar(
        select(TaskRecord).where(TaskRecord.id == body.task_id).with_for_update()
    )
    if task is None:
        raise HTTPException(status_code=404, detail="task not found")
    agent = await _require(session, AgentRecord, body.agent_id, "agent")
    if task.project_id != agent.project_id:
        raise HTTPException(status_code=422, detail="task and agent belong to different projects")
    if not agent.enabled:
        raise HTTPException(status_code=422, detail="agent is disabled")
    attempt = await session.scalar(
        select(func.coalesce(func.max(RunRecord.attempt), 0)).where(RunRecord.task_id == task.id)
    )
    record = RunRecord(
        **body.model_dump(),
        backend=agent.backend,
        status=RunStatus.QUEUED.value,
        attempt=int(attempt or 0) + 1,
    )
    session.add(record)
    await session.commit()
    await session.refresh(record)
    return record


@router.post("/runs/{run_id}/cancel", response_model=RunRead)
async def cancel_run(run_id: UUID, session: AsyncSession = Depends(get_session)) -> RunRecord:
    store = RunStore()
    try:
        record = await store.cancel(session, run_id)
    except LookupError as error:
        raise HTTPException(status_code=404, detail="run not found") from error
    except ValueError as error:
        raise HTTPException(status_code=409, detail=str(error)) from error
    await session.commit()
    await session.refresh(record)
    return record


@router.get("/runs", response_model=list[RunRead])
async def list_runs(
    task_id: UUID | None = None,
    project_id: UUID | None = None,
    session: AsyncSession = Depends(get_session),
):
    statement = select(RunRecord).order_by(RunRecord.created_at.desc())
    if task_id is not None:
        statement = statement.where(RunRecord.task_id == task_id)
    if project_id is not None:
        statement = statement.join(TaskRecord, TaskRecord.id == RunRecord.task_id).where(
            TaskRecord.project_id == project_id
        )
    return list(await session.scalars(statement))


@router.get("/runs/{run_id}", response_model=RunRead)
async def get_run(run_id: UUID, session: AsyncSession = Depends(get_session)):
    return await _require(session, RunRecord, run_id, "run")


@router.get("/runs/{run_id}/execution", response_model=RunExecutionRead)
async def get_run_execution(run_id: UUID, session: AsyncSession = Depends(get_session)):
    # All writers lock the owning Run, so this shared lock keeps the projection
    # and its last-event cursor consistent through the end of this transaction.
    run = await session.scalar(
        select(RunRecord).where(RunRecord.id == run_id).with_for_update(read=True)
    )
    if run is None:
        raise HTTPException(status_code=404, detail="run not found")
    task = await _require(session, TaskRecord, run.task_id, "task")
    agent = await _require(session, AgentRecord, run.agent_id, "agent")
    workspace = await session.scalar(
        select(WorkspaceRecord).where(WorkspaceRecord.run_id == run_id)
    )
    artifacts = await ArtifactStore().list_for_run(session, run_id)
    usage_event = await session.scalar(
        select(EventRecord)
        .where(EventRecord.run_id == run_id, EventRecord.type == "usage.updated")
        .order_by(EventRecord.sequence.desc())
        .limit(1)
    )
    last = await session.scalar(
        select(func.coalesce(func.max(EventRecord.sequence), 0)).where(EventRecord.run_id == run_id)
    )
    usage = usage_event.data if usage_event is not None else {}
    return RunExecutionRead.model_validate(
        {
            "run": run,
            "task": task,
            "agent": agent,
            "workspace": workspace,
            "artifacts": list(artifacts),
            "usage": usage,
            "last_event_sequence": last,
        }
    )


@router.get(
    "/runs/{run_id}/artifacts/{artifact_id}/content",
    response_class=Response,
    responses={
        200: {
            "content": {
                "application/octet-stream": {"schema": {"type": "string", "format": "binary"}}
            }
        }
    },
)
async def get_artifact_content(
    run_id: UUID, artifact_id: UUID, session: AsyncSession = Depends(get_session)
) -> Response:
    artifact = await session.scalar(
        select(ArtifactRecord).where(
            ArtifactRecord.id == artifact_id, ArtifactRecord.run_id == run_id
        )
    )
    if artifact is None:
        raise HTTPException(status_code=404, detail="artifact not found")
    try:
        content = await LocalArtifactContentStore(get_settings().artifact_root.resolve()).read(
            run_id, artifact.uri
        )
    except ArtifactContentError as error:
        raise HTTPException(status_code=404, detail="artifact content unavailable") from error
    expected_hash = artifact.artifact_metadata.get("sha256")
    if expected_hash and hashlib.sha256(content).hexdigest() != expected_hash:
        raise HTTPException(status_code=409, detail="artifact integrity check failed")
    extension = "patch" if artifact.kind == "diff" else "tar"
    return Response(
        content,
        media_type="application/octet-stream",
        headers={
            "Content-Disposition": f'attachment; filename="{artifact.id}.{extension}"',
            "X-Content-Type-Options": "nosniff",
        },
    )


@router.get("/runs/{run_id}/events", response_model=list[EventRead])
async def list_run_events(
    run_id: UUID,
    after: int = Query(default=0, ge=0),
    limit: int = Query(default=200, ge=1, le=1000),
    session: AsyncSession = Depends(get_session),
):
    await _require(session, RunRecord, run_id, "run")
    statement = (
        select(EventRecord)
        .where(EventRecord.run_id == run_id, EventRecord.sequence > after)
        .order_by(EventRecord.sequence)
        .limit(min(limit, 1000))
    )
    return list(await session.scalars(statement))


async def _parse_last_event_id(
    last_event_id: str | None = Header(default=None, alias="Last-Event-ID"),
) -> int:
    if last_event_id is None:
        return 0
    try:
        cursor = int(last_event_id)
    except ValueError as error:
        raise HTTPException(
            status_code=400,
            detail="Last-Event-ID must be a non-negative integer",
        ) from error
    if cursor < 0:
        raise HTTPException(
            status_code=400,
            detail="Last-Event-ID must be a non-negative integer",
        )
    return cursor


@router.get("/runs/{run_id}/events/stream")
async def stream_run_events(
    run_id: UUID,
    request: Request,
    last_event_id: int = Depends(_parse_last_event_id),
    after: int = Query(default=0, ge=0),
    events: RunEventReader = Depends(get_run_event_reader),
) -> StreamingResponse:
    if not await events.run_exists(run_id):
        raise HTTPException(status_code=404, detail="run not found")

    async def stream() -> AsyncIterator[str]:
        cursor = max(last_event_id, after)
        while not await request.is_disconnected():
            records = await events.read_after(run_id, cursor)
            if not records:
                yield ": keep-alive\n\n"
                await asyncio.sleep(get_settings().sse_poll_interval_seconds)
                continue
            for event in records:
                cursor = event.sequence
                payload = EventRead.model_validate(event).model_dump(mode="json")
                yield f"id: {cursor}\nevent: {event.type}\ndata: {json.dumps(payload)}\n\n"

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


async def _require(session: AsyncSession, model: type, identifier: UUID, name: str):
    record = await session.get(model, identifier)
    if record is None:
        raise HTTPException(status_code=404, detail=f"{name} not found")
    return record
