FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY contracts ./contracts
RUN mkdir /out && CGO_ENABLED=0 go build -trimpath -o /out/ \
    ./cmd/circular-api ./cmd/circular-webhooks ./cmd/circular-migrate ./cmd/circular-worker-go ./cmd/circular-codex-auth ./cmd/circular-mcp

FROM debian:bookworm-slim AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app

FROM base AS api
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/circular-api /usr/local/bin/circular-api
EXPOSE 8000
CMD ["circular-api", "--listen", ":8000"]

FROM base AS webhooks
COPY --from=build /out/circular-webhooks /usr/local/bin/circular-webhooks
USER 65532:65532
EXPOSE 8001
CMD ["circular-webhooks", "--listen", ":8001"]

FROM base AS migrate
COPY --from=build /out/circular-migrate /usr/local/bin/circular-migrate
CMD ["circular-migrate"]

FROM base AS mcp
COPY --from=build /out/circular-mcp /usr/local/bin/circular-mcp
USER 65532:65532
ENTRYPOINT ["circular-mcp"]

FROM docker:29-cli AS docker-cli
FROM base AS worker
# The trusted worker uses Git and the Docker CLI; only it receives the Docker socket.
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/bin/docker
COPY --from=build /out/circular-worker-go /usr/local/bin/circular-worker-go
COPY --from=build /out/circular-codex-auth /usr/local/bin/circular-codex-auth
CMD ["circular-worker-go"]
