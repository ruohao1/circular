FROM circular-fake-agent-workload:runtime-test

VOLUME ["/unexpected-image-volume"]

ENTRYPOINT ["/circular-fake-workload", "--write-output"]
