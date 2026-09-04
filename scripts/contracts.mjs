import { spawnSync } from "node:child_process";
import {
  mkdtempSync,
  readFileSync,
  writeFileSync,
  mkdirSync,
  rmSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const check = process.argv.includes("--check");
const directory = mkdtempSync(join(tmpdir(), "circular-contracts-"));
function run(command, args) {
  const result = spawnSync(command, args, {
    encoding: "utf8",
    env: process.env,
  });
  if (result.status !== 0) throw new Error(result.stderr || result.stdout);
  return result.stdout;
}
try {
  const schema = run("uv", ["run", "python", "scripts/export_openapi.py"]);
  writeFileSync(join(directory, "openapi.json"), schema);
  run("corepack", [
    "pnpm",
    "exec",
    "openapi-typescript",
    join(directory, "openapi.json"),
    "--alphabetize",
    "-o",
    join(directory, "api.ts"),
  ]);
  for (const [source, target] of [
    ["openapi.json", "contracts/openapi.json"],
    ["api.ts", "apps/web/src/generated/api.ts"],
  ]) {
    const output = readFileSync(join(directory, source), "utf8");
    if (check) {
      if (readFileSync(target, "utf8") !== output)
        throw new Error(
          `Stale contract: ${target}. Run pnpm contracts:generate.`,
        );
    } else {
      mkdirSync(target.slice(0, target.lastIndexOf("/")), { recursive: true });
      writeFileSync(target, output);
    }
  }
  console.log(
    check ? "API contracts are current." : "API contracts generated.",
  );
} finally {
  rmSync(directory, { recursive: true, force: true });
}
