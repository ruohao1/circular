import { describe, expect, it } from "vitest";
import {
  completionLabel,
  completionAge,
  initialCompletionChecks,
  pullRequestNumber,
} from "./task-completion";
import type { TaskCompletionView } from "@/api";

describe("completion presentation", () => {
  it("uses the authoritative verdict, separate from planning Ready", () => {
    const verdict: TaskCompletionView["verdict"] = {
      next_stage: "in_review",
      merge_readiness: "blocked",
      can_complete: false,
      blockers: [],
      advisories: [],
    };
    expect(completionLabel(verdict)).toBe("Completion blocked");
    expect(completionLabel({ ...verdict, merge_readiness: "ready" })).toBe(
      "Ready to merge",
    );
    expect(
      completionLabel({
        ...verdict,
        merge_readiness: "ready",
        can_complete: true,
      }),
    ).toBe("Verified merge · ready for Done");
    expect(
      completionLabel({
        ...verdict,
        merge_readiness: "not_applicable",
        can_complete: true,
      }),
    ).toBe("Direct completion verified");
  });
  it("formats observation age without deciding readiness", () => {
    const now = Date.parse("2026-10-21T12:00:00Z");
    expect(completionAge(null, now)).toBe("Not checked");
    expect(completionAge("2026-10-21T11:59:30Z", now)).toBe("30 seconds ago");
    expect(completionAge("2026-10-21T11:58:00Z", now)).toBe("2 minutes ago");
    expect(completionAge("bad", now)).toBe("Unknown observation time");
  });
  it("only suggests Circular's three discovered jobs for Circular", () => {
    const checks = [
      "Go / PostgreSQL / Docker",
      "Frontend / contracts",
      "Browser / local and Compose",
      "Unrelated",
    ].map((name) => ({
      workflow_id: "123",
      app_id: "42",
      workflow_path: ".github/workflows/ci.yml",
      name,
    }));
    expect(
      initialCompletionChecks({
        repository_name: "ruohao1/circular",
        config: null,
        available: checks,
        reason: "",
      }),
    ).toEqual(checks.slice(0, 3));
    expect(
      initialCompletionChecks({
        repository_name: "someone/other",
        config: null,
        available: checks,
        reason: "",
      }),
    ).toEqual([]);
    expect(
      initialCompletionChecks({
        repository_name: "ruohao1/circular",
        config: { revision: 1, checks: [checks[3]] },
        available: checks,
        reason: "",
      }),
    ).toEqual([checks[3]]);
  });
  it("accepts only bounded PR numbers", () => {
    expect(pullRequestNumber("42")).toBe(42);
    for (const value of [
      "",
      "0",
      "-1",
      "1.5",
      "https://github.com/x/y/pull/1",
      "2147483648",
      "1e3",
    ])
      expect(pullRequestNumber(value)).toBeUndefined();
  });
});
