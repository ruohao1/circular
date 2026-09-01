from circular.api.main import app


def test_openapi_exposes_the_initial_resource_surface() -> None:
    paths = app.openapi()["paths"]
    assert {
        "/api/v1/projects",
        "/api/v1/repositories",
        "/api/v1/agents",
        "/api/v1/tasks",
        "/api/v1/runs",
        "/api/v1/runs/{run_id}",
        "/api/v1/runs/{run_id}/events",
        "/api/v1/runs/{run_id}/events/stream",
    } <= paths.keys()
