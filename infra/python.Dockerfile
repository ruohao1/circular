FROM docker:29-cli AS docker-cli
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
WORKDIR /app
COPY --from=docker-cli /usr/local/bin/docker /usr/bin/docker
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
COPY . .
RUN uv sync --frozen --all-packages
EXPOSE 8000
