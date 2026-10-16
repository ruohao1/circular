import { describe, expect, it } from "vitest";
import {
  addCivilDays,
  parseCivilDate,
  shiftSchedule,
  scheduleError,
  scheduleLabel,
  schedulePatch,
} from "./task-schedules";

describe("civil date round trips", () => {
  it("validates real four-digit Gregorian dates without timezone parsing", () => {
    expect(parseCivilDate("0099-12-31")).toEqual({
      year: 99,
      month: 12,
      day: 31,
    });
    expect(parseCivilDate("0001-01-01")).toEqual({ year: 1, month: 1, day: 1 });
    expect(parseCivilDate("9999-12-31")).toEqual({
      year: 9999,
      month: 12,
      day: 31,
    });
    expect(parseCivilDate("2000-02-29")).toEqual({
      year: 2000,
      month: 2,
      day: 29,
    });
    for (const value of [
      "",
      "0000-01-01",
      "10000-01-01",
      "1900-02-29",
      "2026-02-29",
      "2026-13-01",
      "2026-01-00",
      "2026-1-01",
      " 2026-10-04",
      "2026-10-04\n",
      "2026-10-04\r",
      "2026-10-04T00:00:00Z",
    ])
      expect(parseCivilDate(value), value).toBeNull();
  });
  it("adds calendar days across leap years, DST and the first century", () => {
    for (const [from, days, to] of [
      ["2024-02-28", 1, "2024-02-29"],
      ["2024-02-29", 1, "2024-03-01"],
      ["2026-03-28", 2, "2026-03-30"],
      ["2026-10-31", 2, "2026-11-02"],
      ["0099-12-31", 1, "0100-01-01"],
      ["0100-01-01", -1, "0099-12-31"],
      ["0001-01-01", 3652058, "9999-12-31"],
    ] as const)
      expect(addCivilDays(from, days)).toBe(to);
    expect(addCivilDays("0001-01-01", -1)).toBeNull();
    expect(addCivilDays("9999-12-31", 1)).toBeNull();
    expect(addCivilDays("2026-10-04", 0.5)).toBeNull();
    expect(addCivilDays("invalid", 1)).toBeNull();
    expect(shiftSchedule("2026-03-28", "2026-03-30", 1)).toEqual({
      planned_start_date: "2026-03-29",
      planned_finish_date: "2026-03-31",
    });
    expect(shiftSchedule("", "2026-10-04", 1)).toBeNull();
    expect(shiftSchedule("2026-10-06", "2026-10-04", 1)).toBeNull();
  });
  it("accepts partial and one-day schedules but identifies reversed ranges", () => {
    expect(scheduleError("", "2026-10-04")).toBeNull();
    expect(scheduleError("2026-10-04", "")).toBeNull();
    expect(scheduleError("2026-10-04", "2026-10-04")).toBeNull();
    expect(scheduleError("2026-10-06", "2026-10-04")).toMatch(
      /finish.*on or after/i,
    );
    expect(scheduleError("2026-02-29", "")).toMatch(/valid.*start/i);
  });
});

describe("schedule patch keeps omission distinct from clearing", () => {
  const saved = {
    planned_start_date: "2026-10-04",
    planned_finish_date: "2026-10-06",
  };
  it("only includes changed endpoints and the frozen planning revision", () => {
    expect(schedulePatch(saved, saved, 7)).toEqual({});
    expect(
      schedulePatch(saved, { ...saved, planned_start_date: "" }, 7),
    ).toEqual({ planned_start_date: null, expected_board_revision: 7 });
    expect(
      schedulePatch(
        saved,
        { planned_start_date: "", planned_finish_date: "" },
        7,
      ),
    ).toEqual({
      planned_start_date: null,
      planned_finish_date: null,
      expected_board_revision: 7,
    });
    expect(
      schedulePatch(
        { planned_start_date: null, planned_finish_date: null },
        { planned_start_date: "", planned_finish_date: "" },
        7,
      ),
    ).toEqual({});
  });
  it("labels saved complete and partial dates without changing their day", () => {
    expect(scheduleLabel(saved)).toBe("Planned 2026-10-04 – 2026-10-06");
    expect(
      scheduleLabel({
        planned_start_date: "2026-10-04",
        planned_finish_date: "2026-10-04",
      }),
    ).toBe("Planned 2026-10-04");
    expect(
      scheduleLabel({
        planned_start_date: "2026-10-04",
        planned_finish_date: null,
      }),
    ).toBe("Unscheduled · Starts 2026-10-04");
    expect(
      scheduleLabel({
        planned_start_date: null,
        planned_finish_date: "2026-10-06",
      }),
    ).toBe("Unscheduled · Finishes 2026-10-06");
    expect(
      scheduleLabel({ planned_start_date: null, planned_finish_date: null }),
    ).toBe("Unscheduled");
  });
});
