import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Build before serving; signals then target the actual Go stack, never go run.
const root = await mkdtemp(join(tmpdir(), "circular-e2e-build-"));
let child;
let stopping = false;
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => { stopping = true; child?.kill(signal); });
}
function run(command, args) {
  if (stopping) return Promise.reject(new Error("test stack stopped"));
  return new Promise((resolve, reject) => {
    child = spawn(command, args, { stdio: "inherit" });
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      child = undefined;
      if (code === 0 || stopping) resolve();
      else reject(new Error(`${command} exited with ${code ?? signal}`));
    });
  });
}
try {
  const binary = join(root, "circular-e2e-stack");
  await run("go", ["build", "-o", binary, "./cmd/circular-e2e-stack"]);
  await run("docker", ["build", "-f", "infra/fake-agent-workload.Dockerfile", "-t", "circular-isq162-runner:test", "."]);
  await run(binary, []);
} catch (error) {
  if (!stopping) { console.error(error.message); process.exitCode = 1; }
} finally {
  await rm(root, { recursive: true, force: true });
}
