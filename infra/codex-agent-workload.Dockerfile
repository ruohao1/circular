FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/codexworkload ./internal/codexworkload
COPY internal/codexauth ./internal/codexauth
COPY internal/codexconfig ./internal/codexconfig
COPY internal/agentproposals ./internal/agentproposals
COPY internal/agenttools ./internal/agenttools
COPY internal/prreviews ./internal/prreviews
COPY internal/runstate ./internal/runstate
COPY cmd/circular-codex-workload ./cmd/circular-codex-workload
RUN CGO_ENABLED=0 go build -trimpath -o /circular-codex-workload ./cmd/circular-codex-workload

FROM node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global @openai/codex@0.153.4 \
    && npm cache clean --force
COPY --from=build /circular-codex-workload /circular-codex-workload
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/circular-codex-workload"]
