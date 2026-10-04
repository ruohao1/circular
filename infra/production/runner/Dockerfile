ARG CIRCULAR_CODEX_RUNNER_IMAGE=circular-codex-runner:dev
ARG CIRCULAR_GO_BUILD_IMAGE=circular-go-build:local
FROM ${CIRCULAR_CODEX_RUNNER_IMAGE} AS runner
FROM ${CIRCULAR_GO_BUILD_IMAGE} AS go
COPY module/go.mod module/go.sum /tmp/circular-test-module/
RUN cd /tmp/circular-test-module && GOPROXY=https://proxy.golang.org go mod download
FROM postgres:17-bookworm@sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652 AS database
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 circular \
    && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /tmp/circular circular

# Copy the filesystem into a fresh image so PostgreSQL's inherited VOLUME and
# entrypoint cannot request mounts or start a server in an agent workload.
FROM scratch
COPY --from=database / /
COPY --from=runner /usr/local/bin/ /usr/local/bin/
COPY --from=runner /usr/local/lib/node_modules/ /usr/local/lib/node_modules/
COPY --from=runner /opt/yarn-v1.22.22/ /opt/yarn-v1.22.22/
COPY --from=runner /circular-codex-workload /circular-codex-workload
COPY --from=go /usr/local/go/ /usr/local/go/
COPY --from=go /go/pkg/mod/ /opt/circular-go-mod/
COPY --chmod=755 circular-go-test /usr/local/bin/circular-go-test
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
ENV PATH=/usr/local/go/bin:/usr/lib/postgresql/17/bin:/usr/local/bin:/usr/bin:/bin \
    LANG=C.UTF-8 CGO_ENABLED=0
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/circular-codex-workload"]
