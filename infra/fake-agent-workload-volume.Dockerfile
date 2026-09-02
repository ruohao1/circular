FROM circular-fake-agent-workload:runtime-test

VOLUME ["/unexpected-image-volume"]

ENTRYPOINT ["python", "-c", "from pathlib import Path; Path('/workspace/container-started').touch(); import runpy; runpy.run_module('circular.fake_agent_workload', run_name='__main__')"]
