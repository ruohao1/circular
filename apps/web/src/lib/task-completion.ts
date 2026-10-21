import type { CompletionCheckSetup, TaskCompletionView } from "@/api";

export function completionLabel(verdict: TaskCompletionView["verdict"]) {
  if (verdict.merge_readiness === "ready")
    return verdict.can_complete
      ? "Verified merge · ready for Done"
      : "Ready to merge";
  if (verdict.can_complete) return "Direct completion verified";
  return "Completion blocked";
}
export function completionAge(checked: string | null, now = Date.now()) {
  if (!checked) return "Not checked";
  const elapsed = now - Date.parse(checked);
  if (!Number.isFinite(elapsed) || elapsed < 0)
    return "Unknown observation time";
  const seconds = Math.floor(elapsed / 1000);
  return seconds < 60
    ? `${seconds} seconds ago`
    : `${Math.floor(seconds / 60)} minutes ago`;
}
export function initialCompletionChecks(setup: CompletionCheckSetup) {
  if (setup.config) return setup.config.checks;
  if (setup.repository_name.toLowerCase() !== "ruohao1/circular") return [];
  const names = [
    "Go / PostgreSQL / Docker",
    "Frontend / contracts",
    "Browser / local and Compose",
  ];
  return setup.available.filter(
    (check) =>
      check.workflow_path === ".github/workflows/ci.yml" &&
      names.includes(check.name),
  );
}
export function pullRequestNumber(value: string) {
  if (!/^[1-9][0-9]*$/.test(value)) return undefined;
  const number = Number(value);
  return Number.isSafeInteger(number) && number <= 2147483647
    ? number
    : undefined;
}
