import type { Page } from "@playwright/test";
import type { components } from "../../apps/web/src/generated/api";

export const A = "11111111-1111-4111-8111-111111111111",
  B = "22222222-2222-4222-8222-222222222222",
  T = "33333333-3333-4333-8333-333333333333";
export const dates = {
  created_at: "2026-10-03T10:00:00Z",
  updated_at: "2026-10-03T10:00:00Z",
};
export async function fixture(page: Page) {
  const projects = [
    { id: A, name: "Project A", ...dates },
    { id: B, name: "Project B", ...dates },
  ];
  const repositories = [
    {
      id: "44444444-4444-4444-8444-444444444444",
      project_id: A,
      name: "Source",
      clone_url: "/fixture",
      default_branch: "main",
      external_refs: {},
      ...dates,
    },
  ];
  const agents = [
    {
      id: "55555555-5555-4555-8555-555555555555",
      project_id: A,
      name: "Engineer",
      enabled: true,
      backend: "fake",
      instructions: "",
      preset: null,
      backend_config: {},
      ...dates,
    },
  ];
  const tasks = [
    {
      id: T,
      project_id: A,
      repository_id: repositories[0].id as string | null,
      title: "Plan the console",
      description: "Keep my instructions",
      status: "open",
      priority: "normal",
      preferred_agent_id: agents[0].id as string | null,
      revision: 1,
      planned_start_date: null as string | null,
      planned_finish_date: null as string | null,
      external_refs: {} as Record<string, unknown>,
      ...dates,
    },
  ];
  const state = {
    projects,
    repositories,
    agents,
    tasks,
    board: 2,
    posts: [] as { path: string; body: Record<string, unknown> }[],
    urls: [] as string[],
    conflict: false,
    writeError: 0,
    writeGate: undefined as Promise<void> | undefined,
    active: false,
    latestRun: null as components["schemas"]["TaskPlanningRun"] | null,
    failRun: false,
    history: false,
    gate: undefined as Promise<void> | undefined,
    timelineGate: undefined as
      | { q?: string; from?: string; section?: string; wait: Promise<void> }
      | undefined,
    timelineError: 0,
  };
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request(),
      url = new URL(req.url()),
      path = url.pathname.replace("/api/v1", "");
    state.urls.push(path + url.search);
    const json = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (["POST", "PATCH"].includes(req.method()))
      state.posts.push({ path, body: req.postDataJSON() });
    if (path === "/projects") return json(projects);
    if (path === "/repositories")
      return json(
        repositories.filter(
          (r) => r.project_id === url.searchParams.get("project_id"),
        ),
      );
    if (path === "/agents")
      return json(
        agents.filter(
          (a) => a.project_id === url.searchParams.get("project_id"),
        ),
      );
    if (path === "/tasks" && req.method() === "POST") {
      if (state.writeGate) await state.writeGate;
      if (state.writeError)
        return json({ detail: "Task could not be saved" }, state.writeError);
      const body = req.postDataJSON();
      const task = {
        ...tasks[0],
        ...body,
        id: `bbbbbbbb-bbbb-4bbb-8bbb-${String(tasks.length).padStart(12, "0")}`,
        repository_id: body.repository_id ?? null,
        preferred_agent_id: body.preferred_agent_id ?? null,
        status: body.status ?? "open",
        planned_start_date: body.planned_start_date ?? null,
        planned_finish_date: body.planned_finish_date ?? null,
        revision: 1,
      };
      tasks.push(task);
      state.board++;
      return json(task, 201);
    }
    const scopeStages =
      url.searchParams.get("scope") === "active"
        ? ["ready", "in_progress", "in_review"]
        : url.searchParams.get("scope") === "backlog"
          ? ["open"]
          : [
              "open",
              "ready",
              "in_progress",
              "in_review",
              "completed",
              "cancelled",
            ];
    if (path.endsWith("/task-timeline")) {
      if (state.timelineError)
        return json({ detail: "Timeline unavailable" }, state.timelineError);
      const q = url.searchParams,
        section = q.get("section"),
        from = q.get("from")!,
        to = q.get("to")!;
      const filtered = tasks.filter(
        (t) =>
          t.project_id === path.split("/")[2] &&
          scopeStages.includes(t.status) &&
          (!q.get("status") || t.status === q.get("status")) &&
          (!q.get("priority") || t.priority === q.get("priority")) &&
          (!q.get("repository_id") ||
            t.repository_id === q.get("repository_id")) &&
          (!q.get("preferred_agent_id") ||
            t.preferred_agent_id === q.get("preferred_agent_id")) &&
          t.title.toLowerCase().includes((q.get("q") ?? "").toLowerCase()),
      );
      const complete = (t: (typeof tasks)[number]) =>
        !!t.planned_start_date && !!t.planned_finish_date;
      const overlap = (t: (typeof tasks)[number]) =>
        complete(t) &&
        t.planned_start_date! <= to &&
        t.planned_finish_date! >= from;
      const cursor = q.get("cursor"),
        limit = Number(q.get("limit") || 50);
      const scope = JSON.stringify([
        path,
        from,
        to,
        q.get("q"),
        q.get("scope") ?? "all",
        q.get("status"),
        q.get("priority"),
        q.get("repository_id"),
        q.get("preferred_agent_id"),
        limit,
      ]);
      const after = cursor
        ? JSON.parse(Buffer.from(cursor, "base64url").toString())
        : null;
      if (after && after.board !== state.board)
        return json(
          { detail: "Task Timeline changed; reload both sections" },
          409,
        );
      if (after && (after.scope !== scope || after.section !== section))
        return json({ detail: "Invalid cursor" }, 422);
      const groups = (section ? [section] : ["scheduled", "unscheduled"]).map(
        (section) => {
          const rows = filtered
            .filter((t) =>
              section === "scheduled" ? overlap(t) : !complete(t),
            )
            .sort((a, b) =>
              section === "scheduled"
                ? a.planned_start_date!.localeCompare(b.planned_start_date!) ||
                  a.id.localeCompare(b.id)
                : b.created_at.localeCompare(a.created_at) ||
                  b.id.localeCompare(a.id),
            );
          const start = after?.start ?? 0,
            end = start + limit;
          return {
            section,
            total: rows.length,
            items: rows.slice(start, end).map((task) => ({
              task: { ...task },
              repository:
                repositories.find((r) => r.id === task.repository_id) ?? null,
              preferred_agent:
                agents.find((a) => a.id === task.preferred_agent_id) ?? null,
              latest_run: state.latestRun,
              run_count: Number(!!state.latestRun) + Number(state.active),
              active_run_count: state.active ? 1 : 0,
            })),
            next_cursor:
              end < rows.length
                ? Buffer.from(
                    JSON.stringify({
                      board: state.board,
                      section,
                      scope,
                      start: end,
                    }),
                  ).toString("base64url")
                : "",
          };
        },
      );
      const result = {
        board_revision: state.board,
        from,
        to,
        outside_window_total: filtered.filter((t) => complete(t) && !overlap(t))
          .length,
        groups,
      };
      const gate = state.timelineGate;
      if (
        gate &&
        (gate.q === undefined || gate.q === (q.get("q") ?? "")) &&
        (gate.from === undefined || gate.from === from) &&
        (gate.section === undefined || gate.section === section)
      )
        await gate.wait;
      return json(result);
    }
    if (path.endsWith("/task-planning")) {
      if (state.gate) await state.gate;
      const q = url.searchParams,
        cursor = q.get("cursor"),
        status = q.get("status"),
        limit = Number(q.get("limit") || 50);
      const scope = JSON.stringify([
        path,
        q.get("scope") ?? "all",
        q.get("q"),
        q.get("priority"),
        q.get("repository_id"),
        q.get("preferred_agent_id"),
        limit,
      ]);
      const after = cursor
        ? JSON.parse(Buffer.from(cursor, "base64url").toString())
        : null;
      if (after && after.board !== state.board)
        return json({ detail: "Task list changed; reload." }, 409);
      if (after && (after.scope !== scope || after.status !== status))
        return json({ detail: "Invalid cursor" }, 422);
      const groups = scopeStages
        .filter((stage) => !status || stage === status)
        .map((status) => {
          const items = tasks
            .filter(
              (t) =>
                t.project_id === path.split("/")[2] &&
                t.status === status &&
                (!url.searchParams.get("priority") ||
                  t.priority === url.searchParams.get("priority")) &&
                (!url.searchParams.get("repository_id") ||
                  t.repository_id === url.searchParams.get("repository_id")) &&
                (!url.searchParams.get("preferred_agent_id") ||
                  t.preferred_agent_id ===
                    url.searchParams.get("preferred_agent_id")) &&
                t.title
                  .toLowerCase()
                  .includes((url.searchParams.get("q") ?? "").toLowerCase()),
            )
            .map((task) => ({
              task,
              repository:
                repositories.find((r) => r.id === task.repository_id) ?? null,
              preferred_agent:
                agents.find((a) => a.id === task.preferred_agent_id) ?? null,
              latest_run: state.latestRun,
              run_count: Number(!!state.latestRun) + Number(state.active),
              active_run_count: state.active ? 1 : 0,
            }));
          const start = after?.start ?? 0;
          const end = start + limit;
          return {
            status,
            total: items.length,
            items: items.slice(start, end),
            next_cursor:
              end < items.length
                ? Buffer.from(
                    JSON.stringify({
                      board: state.board,
                      scope,
                      status,
                      start: end,
                    }),
                  ).toString("base64url")
                : "",
          };
        });
      return json({ board_revision: state.board, groups });
    }
    if (path.startsWith("/tasks/")) {
      const task = tasks.find((t) => t.id === path.split("/")[2]);
      if (!task) return json({ detail: "Missing Task" }, 404);
      if (req.method() === "PATCH") {
        if (state.writeGate) await state.writeGate;
        if (state.writeError)
          return json(
            { detail: "Schedule service unavailable" },
            state.writeError,
          );
        if (state.conflict) return json({ detail: "Task changed" }, 409);
        const { expected_revision, expected_board_revision, ...fields } =
          req.postDataJSON();
        if (
          expected_revision !== task.revision ||
          (expected_board_revision !== undefined &&
            expected_board_revision !== state.board)
        )
          return json({ detail: "Task changed" }, 409);
        Object.assign(task, fields, { revision: task.revision + 1 });
        state.board++;
        return json({ task, board_revision: state.board });
      }
      if (path.endsWith("/move")) {
        const body = req.postDataJSON();
        if (state.conflict) return json({ detail: "Task changed" }, 409);
        task.status = body.status;
        task.revision++;
        state.board++;
        return json({ task, board_revision: state.board });
      }
      if (path.endsWith("/planning")) {
        const managed = !!task.external_refs.linear;
        return json({
          task,
          repository:
            repositories.find((r) => r.id === task.repository_id) ?? null,
          preferred_agent:
            agents.find((a) => a.id === task.preferred_agent_id) ?? null,
          board_revision: state.board,
          content_editable: !managed && !state.active,
          repository_editable: !managed && !state.active,
          content_lock_reason: managed
            ? "source_managed"
            : state.active
              ? "active_run"
              : null,
          repository_lock_reason: managed
            ? "source_managed"
            : state.active
              ? "has_runs"
              : null,
        });
      }
      return json(task);
    }
    if (path === "/runs" && req.method() === "POST")
      return json({ detail: "Run could not start" }, 503);
    if (path.endsWith("/run-queue")) {
      const older =
        url.searchParams.has("cursor") && !!url.searchParams.get("cursor");
      return json({
        items: state.history
          ? [
              {
                run: {
                  id: older ? B : A,
                  task_id: T,
                  agent_id: agents[0].id,
                  status: "succeeded",
                  backend: "fake",
                  kind: "coding",
                  attempt: older ? 1 : 21,
                  ...dates,
                },
                task_title: older ? "Older attempt" : "Latest attempt",
                agent_name: "Actual Engineer",
                repository: null,
              },
            ]
          : [],
        next_cursor: state.history && !older ? "older" : "",
      });
    }
    if (path === "/external-requests")
      return json({ items: [], next_cursor: "" });
    return json([]);
  });
  return state;
}
