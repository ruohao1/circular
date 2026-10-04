FROM golang:1.27.1-bookworm
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY contracts ./contracts
# Exercise the real workload launcher with a deterministic replacement for the
# external Codex CLI. No model, provider connection, or credentials are needed.
RUN CGO_ENABLED=0 go test -c -o /review-git-probe ./internal/codexworkload
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/review-git-probe", "review-git-container"]
