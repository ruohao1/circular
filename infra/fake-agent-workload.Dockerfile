FROM python:3.12-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/opt/circular-workload/src

WORKDIR /opt/circular-workload
COPY --chown=65532:65532 packages/fake-agent-workload/src ./src

USER 65532:65532
ENTRYPOINT ["python", "-m", "circular.fake_agent_workload"]
