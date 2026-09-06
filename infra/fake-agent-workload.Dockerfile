FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/fakeworkload ./internal/fakeworkload
COPY cmd/circular-fake-workload ./cmd/circular-fake-workload
RUN CGO_ENABLED=0 go build -trimpath -o /circular-fake-workload ./cmd/circular-fake-workload

FROM scratch
COPY --from=build /circular-fake-workload /circular-fake-workload
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/circular-fake-workload"]
