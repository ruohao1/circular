import argparse

from circular.fake_agent_workload.cli import main

parser = argparse.ArgumentParser()
parser.add_argument("--write-output", action="store_true")
args = parser.parse_args()
raise SystemExit(main(write_output=args.write_output))
