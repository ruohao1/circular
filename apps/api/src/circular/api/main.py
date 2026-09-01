from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from circular.api.config import get_settings
from circular.api.dependencies import engine
from circular.api.routes import router
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware


@asynccontextmanager
async def lifespan(_: FastAPI) -> AsyncIterator[None]:
    yield
    await engine.dispose()


app = FastAPI(title="Circular API", version="0.1.0", lifespan=lifespan)
app.add_middleware(
    CORSMiddleware,
    allow_origins=list(get_settings().cors_origins),
    allow_methods=["*"],
    allow_headers=["*"],
)
app.include_router(router, prefix="/api/v1")


def run() -> None:
    import uvicorn

    uvicorn.run("circular.api.main:app", host="0.0.0.0", port=8000, reload=False)
