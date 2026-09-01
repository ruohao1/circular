FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
WORKDIR /app
COPY . .
RUN uv sync --frozen --all-packages
EXPOSE 8000
