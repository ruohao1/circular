FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY contracts ./contracts
RUN mkdir /out && CGO_ENABLED=0 go build -trimpath -o /out/ \
    ./cmd/circular-api ./cmd/circular-migrate ./cmd/circular-worker-go

FROM debian:bookworm-slim AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app

FROM base AS api
COPY --from=build /out/circular-api /usr/local/bin/circular-api
EXPOSE 8000
CMD ["circular-api"]

FROM base AS migrate
COPY --from=build /out/circular-migrate /usr/local/bin/circular-migrate
CMD ["circular-migrate"]

FROM docker:29-cli AS docker-cli
FROM base AS worker
# Only the trusted worker receives Git and the Docker CLI/socket. No runner does.
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=docker-cli /usr/local/bin/docker /usr/bin/docker
COPY --from=build /out/circular-worker-go /usr/local/bin/circular-worker-go
CMD ["circular-worker-go"]
