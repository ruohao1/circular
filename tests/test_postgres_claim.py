from circular.storage import RunStore
from sqlalchemy.dialects import postgresql


def test_claim_uses_postgres_skip_locked() -> None:
    sql = str(
        RunStore.claim_statement().compile(
            dialect=postgresql.dialect(), compile_kwargs={"literal_binds": True}
        )
    )
    assert "FOR UPDATE SKIP LOCKED" in sql
    assert "runs.status = 'queued'" in sql
    assert "LIMIT 1" in sql
