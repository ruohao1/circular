import type { Task, TaskPlanningUpdate } from "@/api";
import type { TaskDraft } from "@/launch";

export type TaskPlanningDraft = TaskDraft & {
  planned_start_date: string;
  planned_finish_date: string;
};
type Schedule = Pick<Task, "planned_start_date" | "planned_finish_date">;
type ScheduleDraft = Pick<
  TaskPlanningDraft,
  "planned_start_date" | "planned_finish_date"
>;

function monthDays(year: number, month: number): number {
  if (month === 2)
    return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

export function parseCivilDate(
  value: string,
): { year: number; month: number; day: number } | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const [year, month, day] = value.split("-").map(Number);
  if (
    year < 1 ||
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > monthDays(year, month)
  )
    return null;
  return { year, month, day };
}

function daysBeforeYear(year: number): number {
  const previous = year - 1;
  return (
    365 * previous +
    Math.floor(previous / 4) -
    Math.floor(previous / 100) +
    Math.floor(previous / 400)
  );
}

// A day ordinal avoids local timezone/DST arithmetic and Date's year 0–99 remap.
export function addCivilDays(value: string, amount: number): string | null {
  const date = parseCivilDate(value);
  if (!date || !Number.isSafeInteger(amount)) return null;
  let ordinal = daysBeforeYear(date.year) + date.day - 1 + amount;
  for (let month = 1; month < date.month; month++)
    ordinal += monthDays(date.year, month);
  if (ordinal < 0 || ordinal >= daysBeforeYear(10000)) return null;
  let low = 1,
    high = 10000;
  while (low + 1 < high) {
    const middle = Math.floor((low + high) / 2);
    if (daysBeforeYear(middle) <= ordinal) low = middle;
    else high = middle;
  }
  let day = ordinal - daysBeforeYear(low) + 1,
    month = 1;
  while (day > monthDays(low, month)) day -= monthDays(low, month++);
  return `${String(low).padStart(4, "0")}-${String(month).padStart(2, "0")}-${String(day).padStart(2, "0")}`;
}

export function scheduleError(start: string, finish: string): string | null {
  if (start && !parseCivilDate(start))
    return "Enter a valid planned start date (YYYY-MM-DD).";
  if (finish && !parseCivilDate(finish))
    return "Enter a valid planned finish date (YYYY-MM-DD).";
  if (start && finish && finish < start)
    return "Planned finish must be on or after planned start.";
  return null;
}

export function shiftSchedule(
  start: string,
  finish: string,
  days: number,
): ScheduleDraft | null {
  if (!start || !finish || scheduleError(start, finish)) return null;
  const nextStart = addCivilDays(start, days),
    nextFinish = addCivilDays(finish, days);
  return nextStart && nextFinish
    ? { planned_start_date: nextStart, planned_finish_date: nextFinish }
    : null;
}

export function schedulePatch(
  saved: Schedule,
  draft: ScheduleDraft,
  boardRevision: number,
): Partial<TaskPlanningUpdate> {
  const patch: Partial<TaskPlanningUpdate> = {};
  for (const field of ["planned_start_date", "planned_finish_date"] as const) {
    if ((saved[field] ?? "") !== draft[field]) {
      patch[field] = draft[field] || null;
      patch.expected_board_revision = boardRevision;
    }
  }
  return patch;
}

export function scheduleLabel(schedule: Schedule): string {
  const start = schedule.planned_start_date,
    finish = schedule.planned_finish_date;
  if (start && finish)
    return start === finish
      ? `Planned ${start}`
      : `Planned ${start} – ${finish}`;
  if (start) return `Unscheduled · Starts ${start}`;
  if (finish) return `Unscheduled · Finishes ${finish}`;
  return "Unscheduled";
}
