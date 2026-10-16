import { describe, expect, it } from "vitest";
import {
  civilToUTC,
  utcToCivil,
  inclusiveEnd,
  localToday,
  timelineWindow,
  stepTimeline,
  scheduleIntent,
  ganttDates,
  type TaskScheduleSnapshot,
} from "./task-timeline";
import { parseTaskPlanningSearch } from "./task-planning";

describe("Timeline civil adapter", () => {
  it("round trips real civil dates, DST, leap years and extreme years", () => {
    for (const value of [
      "0001-01-01",
      "0099-12-31",
      "2024-02-29",
      "2026-03-08",
      "2026-10-25",
      "9999-12-31",
    ]) {
      expect(utcToCivil(civilToUTC(value))).toBe(value);
      expect(inclusiveEnd(value).getTime() - civilToUTC(value).getTime()).toBe(
        86400000,
      );
    }
    expect(inclusiveEnd("9999-12-31").toISOString()).toBe(
      "+010000-01-01T00:00:00.000Z",
    );
    expect(() => civilToUTC("2026-02-30")).toThrow();
  });
  it("uses the renderer's true ranges and calendar period navigation", () => {
    expect(timelineWindow("2026-10-07", "day")).toEqual({
      from: "2026-10-01",
      to: "2026-10-31",
    });
    expect(timelineWindow("2026-10-07", "week")).toEqual({
      from: "2026-09-28",
      to: "2027-01-03",
    });
    expect(timelineWindow("2024-06-01", "month")).toEqual({
      from: "2024-01-01",
      to: "2024-12-31",
    });
    expect(stepTimeline("2026-10-07", "week", 1)).toBe("2027-01-07");
    expect(stepTimeline("0001-01-01", "month", -1)).toBeNull();
    expect(stepTimeline("9999-12-31", "day", 1)).toBeNull();
    expect(timelineWindow("0001-01-01", "week").from).toBe("0001-01-01");
    expect(timelineWindow("9999-12-31", "week").to).toBe("9999-12-31");
  });
  it("reads local Today and normalizes only invalid anchors", () => {
    const today = new Date(2026, 9, 7, 23, 59);
    expect(localToday(today)).toBe("2026-10-07");
    expect(
      parseTaskPlanningSearch({
        view: "timeline",
        scale: "month",
        date: "0099-01-01",
      }),
    ).toMatchObject({ view: "timeline", scale: "month", date: "0099-01-01" });
    expect(
      parseTaskPlanningSearch({ scale: "bad", date: "2026-02-30" }),
    ).toMatchObject({ scale: "week", date: undefined });
  });
});

describe("schedule gesture intent", () => {
  const snapshot: TaskScheduleSnapshot = {
    taskId: "task",
    projectId: "project",
    revision: 2,
    boardRevision: 3,
    plannedStartDate: "2026-03-07",
    plannedFinishDate: "2026-03-09",
    scope: "day-page-1",
  };
  it("rejects stale snapshots, no-ops and invalid dates", () => {
    for (const key of Object.keys(snapshot) as Array<
      keyof TaskScheduleSnapshot
    >) {
      expect(
        scheduleIntent(
          snapshot,
          {
            ...snapshot,
            [key]: typeof snapshot[key] === "number" ? 99 : "changed",
          },
          "2026-03-08",
          "2026-03-10",
        ),
      ).toBeNull();
    }
    for (const [start, finish] of [
      ["2026-03-07", "2026-03-09"],
      ["2026-03-10", "2026-03-09"],
      ["2026-02-30", "2026-03-09"],
      ["0000-01-01", "0001-01-01"],
      ["9999-12-31", "10000-01-01"],
    ])
      expect(scheduleIntent(snapshot, snapshot, start, finish)).toBeNull();
    expect(
      scheduleIntent(snapshot, snapshot, "2026-03-08", "2026-03-10"),
    ).toMatchObject({
      nextStartDate: "2026-03-08",
      nextFinishDate: "2026-03-10",
      revision: 2,
      boardRevision: 3,
    });
  });
  it("converts only whole UTC days, preserving inclusive endpoints and year bounds", () => {
    for (const value of [
      "0001-01-01",
      "0099-12-31",
      "2024-02-29",
      "2026-03-08",
      "2026-10-25",
      "9999-12-31",
    ])
      expect(ganttDates(civilToUTC(value), inclusiveEnd(value))).toEqual({
        start: value,
        finish: value,
      });
    expect(
      ganttDates(civilToUTC("2026-03-07"), inclusiveEnd("2026-03-09")),
    ).toEqual({ start: "2026-03-07", finish: "2026-03-09" });
    expect(ganttDates(new Date("invalid"), new Date())).toBeNull();
    expect(
      ganttDates(new Date("2026-03-07T01:00Z"), inclusiveEnd("2026-03-09")),
    ).toBeNull();
    expect(
      ganttDates(civilToUTC("2026-03-09"), civilToUTC("2026-03-09")),
    ).toBeNull();
    expect(
      ganttDates(new Date("0000-12-31T00:00Z"), civilToUTC("0001-01-01")),
    ).toBeNull();
  });
});
