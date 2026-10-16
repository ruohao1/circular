import { describe, expect, it } from "vitest";
import {
  parseRunQueueSearch,
  runDurationSeconds,
  runQueueOptions,
} from "./run-queue";

describe("Run queue URL and elapsed execution", () => {
  it("keeps actual Agent filters separate in cached Run pages", () => {
    const options = runQueueOptions("project", {
      agent_id: "agent-a",
      task_id: "task",
    });
    expect(options.queryKey).toEqual([
      "run-queue",
      "project",
      {
        group: "all",
        q: "",
        cursor: "",
        limit: 50,
        task_id: "task",
        agent_id: "agent-a",
      },
    ]);
    expect(options.queryKey).not.toEqual(
      runQueueOptions("project", { agent_id: "agent-b", task_id: "task" })
        .queryKey,
    );
  });
  it("normalizes links without treating literal search symbols as wildcards", () => {
    expect(parseRunQueueSearch({ group: "unknown", q: "  Fix_%  " })).toEqual({
      group: "all",
      q: "Fix_%",
      cursor: "",
    });
    expect(
      parseRunQueueSearch({ group: "failed", q: ["wrong"], cursor: 7 }),
    ).toEqual({ group: "failed", q: "", cursor: "" });
    expect(
      parseRunQueueSearch({ q: "界".repeat(201), cursor: "a".repeat(2049) }),
    ).toEqual({ group: "all", q: "界".repeat(200), cursor: "" });
  });
  it("does not count queue wait as execution and freezes a finished duration", () => {
    expect(
      runDurationSeconds({ started_at: null, finished_at: null }, 10_000),
    ).toBeNull();
    expect(
      runDurationSeconds(
        {
          started_at: "2026-10-03T10:00:00Z",
          finished_at: "2026-10-03T10:00:05Z",
        },
        Date.parse("2026-10-03T11:00:00Z"),
      ),
    ).toBe(5);
    expect(
      runDurationSeconds(
        { started_at: "2026-10-03T10:00:00Z", finished_at: null },
        Date.parse("2026-10-03T10:00:12.999Z"),
      ),
    ).toBe(12);
    expect(
      runDurationSeconds(
        { started_at: "2026-10-03T10:00:00Z", finished_at: null },
        Date.parse("2026-10-03T09:59:00Z"),
      ),
    ).toBe(0);
  });
});
