FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/prreviews ./internal/prreviews
COPY internal/codexconfig ./internal/codexconfig
COPY internal/runstate ./internal/runstate
COPY cmd/circular-review-fixture-workload ./cmd/circular-review-fixture-workload
RUN CGO_ENABLED=0 go build -trimpath -o /review-fixture ./cmd/circular-review-fixture-workload
FROM scratch
COPY --from=build /review-fixture /review-fixture
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/review-fixture"]
