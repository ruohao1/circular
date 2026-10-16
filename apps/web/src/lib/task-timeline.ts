import {
  getGanttDateRange,
  stepGanttDate,
} from "@/components/reui/gantt/gantt-lib";
import type { Task } from "@/api";
import { parseCivilDate, scheduleError } from "./task-schedules";

export type TimelineScale = "day" | "week" | "month";
export type TimelineSection = "scheduled" | "unscheduled";
export const rendererScale = {
  day: "month",
  week: "quarter",
  month: "year",
} as const;
const dayMs = 86400000;
export function civilToUTC(value: string): Date {
  if (!parseCivilDate(value)) throw new Error("Invalid calendar date");
  return new Date(`${value}T00:00:00.000Z`);
}
export function utcToCivil(value: Date): string | null {
  const year = value.getUTCFullYear();
  return year >= 1 && year <= 9999 ? value.toISOString().slice(0, 10) : null;
}
// The exclusive year-10000 sentinel is renderer-only, never an API date.
export function inclusiveEnd(finish: string): Date {
  return new Date(civilToUTC(finish).getTime() + dayMs);
}
export function localToday(now = new Date()): string {
  return `${String(now.getFullYear()).padStart(4, "0")}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}
export function timelineWindow(anchor: string, scale: TimelineScale) {
  const { visibleRange } = getGanttDateRange(
    rendererScale[scale],
    civilToUTC(anchor),
    { timeZone: "UTC", weekStartsOn: 1 },
  );
  return {
    from: utcToCivil(visibleRange.start) ?? "0001-01-01",
    to:
      utcToCivil(new Date(visibleRange.end.getTime() - dayMs)) ?? "9999-12-31",
  };
}
export function stepTimeline(
  anchor: string,
  scale: TimelineScale,
  direction: 1 | -1,
) {
  return utcToCivil(
    stepGanttDate(rendererScale[scale], civilToUTC(anchor), direction, {
      timeZone: "UTC",
    }),
  );
}

export type TaskScheduleSnapshot = {
  taskId: string;
  projectId: string;
  revision: number;
  boardRevision: number;
  plannedStartDate: string;
  plannedFinishDate: string;
  scope: string;
};
export type TaskScheduleIntent = TaskScheduleSnapshot & {
  nextStartDate: string;
  nextFinishDate: string;
};
export function scheduleSnapshot(
  task: Pick<
    Task,
    | "id"
    | "project_id"
    | "revision"
    | "planned_start_date"
    | "planned_finish_date"
  >,
  scope: string,
  boardRevision: number,
): TaskScheduleSnapshot | null {
  if (!task.planned_start_date || !task.planned_finish_date) return null;
  return {
    taskId: task.id,
    projectId: task.project_id,
    revision: task.revision,
    boardRevision,
    plannedStartDate: task.planned_start_date,
    plannedFinishDate: task.planned_finish_date,
    scope,
  };
}
export function scheduleIntent(
  snapshot: TaskScheduleSnapshot,
  current: TaskScheduleSnapshot | null,
  nextStartDate: string,
  nextFinishDate: string,
): TaskScheduleIntent | null {
  if (
    !current ||
    (
      [
        "taskId",
        "projectId",
        "revision",
        "boardRevision",
        "plannedStartDate",
        "plannedFinishDate",
        "scope",
      ] as const
    ).some((key) => snapshot[key] !== current[key]) ||
    !nextStartDate ||
    !nextFinishDate ||
    scheduleError(nextStartDate, nextFinishDate) ||
    (nextStartDate === snapshot.plannedStartDate &&
      nextFinishDate === snapshot.plannedFinishDate)
  )
    return null;
  return { ...snapshot, nextStartDate, nextFinishDate };
}
export function ganttDates(
  start: Date,
  end: Date,
): { start: string; finish: string } | null {
  if (
    !Number.isFinite(start.getTime()) ||
    !Number.isFinite(end.getTime()) ||
    start.getTime() % dayMs !== 0 ||
    end.getTime() % dayMs !== 0
  )
    return null;
  const first = utcToCivil(start),
    last = utcToCivil(new Date(end.getTime() - dayMs));
  return first && last && !scheduleError(first, last)
    ? { start: first, finish: last }
    : null;
}
