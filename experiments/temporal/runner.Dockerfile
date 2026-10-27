FROM scratch
COPY --chmod=0555 runner /circular-fake-workload
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/circular-fake-workload"]
