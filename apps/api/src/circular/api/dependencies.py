from collections.abc import AsyncIterator

from circular.api.config import get_settings
from circular.storage import create_engine, create_session_factory
from sqlalchemy.ext.asyncio import AsyncSession

engine = create_engine(get_settings().database_url)
session_factory = create_session_factory(engine)


async def get_session() -> AsyncIterator[AsyncSession]:
    async with session_factory() as session:
        yield session
