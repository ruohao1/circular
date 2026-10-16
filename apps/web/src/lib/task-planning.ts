import { parseCivilDate } from "./task-schedules";
import type { TimelineScale } from "./task-timeline";
import { queryOptions } from "@tanstack/react-query";
import {
  api,
  type Task,
  type TaskMove,
  type TaskPlanningPage,
  type TaskPlanningQuery,
  type TaskPriority,
} from "@/api";

export const taskStages = [
  "open",
  "ready",
  "in_progress",
  "in_review",
  "completed",
  "cancelled",
] as const;
export const stageLabels: Record<Task["status"], string> = {
  open: "Backlog",
  ready: "Ready",
  in_progress: "In progress",
  in_review: "Review",
  completed: "Done",
  cancelled: "Cancelled",
};
export const taskPriorities = [
  "none",
  "low",
  "normal",
  "high",
  "urgent",
] as const;
export const priorityLabels: Record<TaskPriority, string> = {
  none: "No priority",
  low: "Low",
  normal: "Normal",
  high: "High",
  urgent: "Urgent",
};
export const taskScopes = {
  active: { label: "Active", stages: ["ready", "in_progress", "in_review"] },
  backlog: { label: "Backlog", stages: ["open"] },
  all: { label: "All Tasks", stages: taskStages },
} as const;
export interface TaskPlanningSearch {
  view: "list" | "board" | "timeline";
  scope: keyof typeof taskScopes;
  scale: TimelineScale;
  date?: string;
  q: string;
  status: Task["status"] | "";
  priority: TaskPriority | "";
  repository_id: string;
  preferred_agent_id: string;
  taskId: string;
}
export function parseTaskPlanningSearch(
  search: Record<string, unknown>,
): TaskPlanningSearch {
  const text = (key: string) =>
    typeof search[key] === "string" ? search[key] : "";
  return {
    scope:
      search.scope === "active" || search.scope === "backlog"
        ? search.scope
        : "all",
    view:
      search.view === "timeline"
        ? "timeline"
        : search.view === "board"
          ? "board"
          : "list",
    scale:
      search.scale === "day" || search.scale === "month"
        ? search.scale
        : "week",
    date: parseCivilDate(text("date")) ? text("date") : undefined,
    q: Array.from(text("q").trim()).slice(0, 200).join(""),
    status: taskStages.find((s) => s === search.status) ?? "",
    priority: taskPriorities.find((s) => s === search.priority) ?? "",
    repository_id: text("repository_id"),
    preferred_agent_id: text("preferred_agent_id"),
    taskId: text("taskId"),
  };
}
export function taskPlanningOptions(
  projectId: string,
  query: TaskPlanningQuery = {},
) {
  const normalized: TaskPlanningQuery = {
    ...(query.scope ? { scope: query.scope } : {}),
    q: query.q?.trim() ?? "",
    ...(query.status ? { status: query.status } : {}),
    ...(query.priority ? { priority: query.priority } : {}),
    ...(query.repository_id ? { repository_id: query.repository_id } : {}),
    ...(query.preferred_agent_id
      ? { preferred_agent_id: query.preferred_agent_id }
      : {}),
    limit: query.limit ?? 50,
    ...(query.cursor ? { cursor: query.cursor } : {}),
  };
  return queryOptions({
    queryKey: ["task-planning", projectId, normalized],
    queryFn: () => api.taskPlanning(projectId, normalized),
    enabled: !!projectId,
    retry: false,
    refetchInterval: query.cursor ? false : 15000,
    refetchIntervalInBackground: false,
  });
}
export function taskPlanningDetailOptions(taskId: string) {
  return queryOptions({
    queryKey: ["task-planning-detail", taskId],
    queryFn: () => api.taskPlanningDetail(taskId),
    enabled: !!taskId,
    retry: false,
  });
}
export function mergeTaskPlanningPage(
  current: TaskPlanningPage,
  next: TaskPlanningPage,
): TaskPlanningPage | null {
  if (current.board_revision !== next.board_revision) return null;
  return {
    ...current,
    groups: current.groups.map((group) => {
      const more = next.groups.find((g) => g.status === group.status);
      if (!more) return group;
      const ids = new Set(group.items.map((item) => item.task.id));
      return {
        ...more,
        items: [
          ...group.items,
          ...more.items.filter((item) => !ids.has(item.task.id)),
        ],
      };
    }),
  };
}

// Resolve visible neighbors against the captured generation; the server owns
// the complete ordering, including filtered-out and unloaded Tasks.
export function taskDropIntent(
  page: TaskPlanningPage,
  taskId: string,
  targetStatus: Task["status"],
  targetIndex: number,
): TaskMove | null {
  const source = page.groups.find((g) =>
    g.items.some((i) => i.task.id === taskId),
  );
  const target = page.groups.find((g) => g.status === targetStatus);
  const task = source?.items.find((i) => i.task.id === taskId)?.task;
  if (
    !source ||
    !target ||
    !task ||
    !Number.isInteger(targetIndex) ||
    targetIndex < 0
  )
    return null;
  const items = target.items.filter((i) => i.task.id !== taskId);
  if (targetIndex > items.length) return null;
  if (
    source.status === targetStatus &&
    source.items.findIndex((i) => i.task.id === taskId) === targetIndex
  )
    return null;
  const next = items[targetIndex],
    previous = items[targetIndex - 1];
  return {
    expected_revision: task.revision,
    expected_board_revision: page.board_revision,
    status: targetStatus,
    ...(next
      ? { placement: "before", anchor_task_id: next.task.id }
      : previous
        ? { placement: "after", anchor_task_id: previous.task.id }
        : { placement: "end" }),
  };
}
