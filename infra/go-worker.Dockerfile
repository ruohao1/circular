FROM golang:1.27.1-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /circular-worker-go ./cmd/circular-worker-go

FROM docker:29-cli AS docker-cli
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
WORKDIR /app
COPY --from=docker-cli /usr/local/bin/docker /usr/bin/docker
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
# The Python execution module is a temporary migration bridge, not a second queue.
COPY . .
RUN uv sync --frozen --all-packages
COPY --from=go-builder /circular-worker-go /usr/local/bin/circular-worker-go
ENV CIRCULAR_EXECUTOR_PYTHON=/app/.venv/bin/python
CMD ["/usr/local/bin/circular-worker-go"]
