import { describe, expect, it } from "vitest";
import type { TaskPlanningPage } from "@/api";
import {
  taskDropIntent,
  parseTaskPlanningSearch,
  mergeTaskPlanningPage,
  taskPlanningOptions,
} from "./task-planning";
import { runQueueOptions } from "./run-queue";

describe("Task planning scope and pagination", () => {
  it("normalizes URL values and limits search by Unicode code points", () => {
    expect(
      parseTaskPlanningSearch({
        view: "bad",
        status: "bad",
        priority: "bad",
        q: "  fix  ",
        repository_id: 5,
        taskId: [],
      }),
    ).toEqual({
      view: "list",
      scope: "all",
      scale: "week",
      date: undefined,
      status: "",
      priority: "",
      q: "fix",
      repository_id: "",
      preferred_agent_id: "",
      taskId: "",
    });
    expect(
      Array.from(parseTaskPlanningSearch({ q: "🌍".repeat(201) }).q),
    ).toHaveLength(200);
  });
  it("normalizes presets and keeps their server queries in separate caches", () => {
    expect(parseTaskPlanningSearch({ scope: "bad" }).scope).toBe("all");
    expect(parseTaskPlanningSearch({ scope: "active" }).scope).toBe("active");
    expect(parseTaskPlanningSearch({ scope: "backlog" }).scope).toBe("backlog");
    const all = taskPlanningOptions("project", { scope: "all" });
    const active = taskPlanningOptions("project", { scope: "active" });
    expect(all.queryKey).not.toEqual(active.queryKey);
    expect(active.queryKey).toEqual([
      "task-planning",
      "project",
      { scope: "active", q: "", limit: 50 },
    ]);
  });
  it("keeps unrelated Run queue scopes compatible and retains Task history filters", () => {
    expect(
      runQueueOptions("project", { task_id: "task", limit: 20 }).queryKey,
    ).toEqual([
      "run-queue",
      "project",
      { group: "all", q: "", cursor: "", limit: 20, task_id: "task" },
    ]);
    expect(runQueueOptions("project").queryKey).toEqual([
      "run-queue",
      "project",
      { group: "all", q: "", cursor: "", limit: 50 },
    ]);
  });
  it("merges group pages only within the same board revision", () => {
    const first: TaskPlanningPage = {
      board_revision: 4,
      groups: [{ status: "open", total: 0, items: [], next_cursor: "" }],
    };
    expect(
      mergeTaskPlanningPage(first, { ...first, board_revision: 5 }),
    ).toBeNull();
    expect(mergeTaskPlanningPage(first, first)).toEqual(first);
  });
});

describe("canonical drop intents", () => {
  const item = (
    id: string,
    status: "open" | "ready" = "open",
  ): TaskPlanningPage["groups"][number]["items"][number] => ({
    task: {
      id,
      project_id: "p",
      repository_id: null,
      parent_task_id: null,
      title: id,
      status,
      priority: "none",
      preferred_agent_id: null,
      revision: 3,
      planned_start_date: null,
      planned_finish_date: null,
      created_at: "",
      updated_at: "",
    },
    repository: null,
    preferred_agent: null,
    latest_run: null,
    run_count: 0,
    active_run_count: 0,
  });
  const page: TaskPlanningPage = {
    board_revision: 9,
    groups: [
      {
        status: "open",
        total: 4,
        items: [item("A"), item("B")],
        next_cursor: "more",
      },
      {
        status: "ready",
        total: 1,
        items: [item("X", "ready")],
        next_cursor: "",
      },
    ],
  };
  it("anchors before visible B without treating a filtered index as saved position", () => {
    expect(taskDropIntent(page, "X", "open", 1)).toEqual({
      expected_revision: 3,
      expected_board_revision: 9,
      status: "open",
      placement: "before",
      anchor_task_id: "B",
    });
  });
  it("anchors after the last loaded card and uses global end only for empty columns", () => {
    expect(taskDropIntent(page, "X", "open", 2)).toMatchObject({
      placement: "after",
      anchor_task_id: "B",
    });
    expect(
      taskDropIntent(
        {
          ...page,
          groups: [
            page.groups[1],
            { status: "open", total: 5, items: [], next_cursor: "" },
          ],
        },
        "X",
        "open",
        0,
      ),
    ).toEqual({
      expected_revision: 3,
      expected_board_revision: 9,
      status: "open",
      placement: "end",
    });
  });
  it("resolves same-stage target indexes after removing the active Task", () => {
    expect(taskDropIntent(page, "A", "open", 1)).toMatchObject({
      placement: "after",
      anchor_task_id: "B",
    });
    expect(taskDropIntent(page, "A", "open", 0)).toBeNull();
    expect(taskDropIntent(page, "missing", "open", 0)).toBeNull();
  });
});
