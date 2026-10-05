import { expect, test, type Page } from "@playwright/test";

const A = "11111111-1111-4111-8111-111111111111";
const B = "22222222-2222-4222-8222-222222222222";
const TASK = "33333333-3333-4333-8333-333333333333";
const RUN = "44444444-4444-4444-8444-444444444444";
const dates = {
  created_at: "2026-10-03T10:00:00Z",
  updated_at: "2026-10-03T10:00:00Z",
};

async function fixture(page: Page) {
  const projects = [
    { id: A, name: "Project A", ...dates },
    { id: B, name: "Project B", ...dates },
  ];
  const repos = projects.map((p) => ({
    id: `${p.id.slice(0, 8)}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`,
    project_id: p.id,
    name: `${p.name} Repository`,
    clone_url: "/fixture",
    default_branch: "main",
    ...dates,
  }));
  const agents = projects.map((p) => ({
    id: `${p.id.slice(0, 8)}-bbbb-4bbb-8bbb-bbbbbbbbbbbb`,
    project_id: p.id,
    name: `${p.name} Engineer`,
    backend: "fake",
    enabled: true,
    preset: null,
    instructions: "",
    ...dates,
  }));
  const imported = {
    id: TASK,
    project_id: B,
    repository_id: repos[1].id,
    title: "Imported Task",
    description: "Keep imported instructions.",
    status: "open",
    external_refs: {
      circular: { kind: "repository-discovery", agent_id: agents[1].id },
    },
    ...dates,
  };
  const run = {
    id: RUN,
    task_id: TASK,
    agent_id: agents[0].id,
    parent_run_id: null,
    backend: "fake",
    status: "running",
    attempt: 1,
    worker_id: null,
    claimed_at: null,
    started_at: dates.created_at,
    finished_at: null,
    error: null,
    external_refs: {},
    request_key: null,
    kind: "task",
    ...dates,
  };
  const state = {
    projects,
    repos,
    agents,
    imported,
    run,
    taskPosts: [] as Record<string, unknown>[],
    runPosts: [] as Record<string, unknown>[],
    urls: [] as string[],
    failLaunch: false,
    failQueue: false,
    failRequests: false,
    requests: [] as Record<string, unknown>[],
    paged: false,
    importGate: undefined as Promise<void> | undefined,
    launchGate: undefined as Promise<void> | undefined,
    requestGate: undefined as Promise<void> | undefined,
    setupGate: undefined as Promise<void> | undefined,
    queueGate: undefined as Promise<void> | undefined,
  };
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace("/api/v1", "");
    state.urls.push(url.pathname + url.search);
    const json = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (path === "/projects" && req.method() === "POST") {
      if (state.setupGate) await state.setupGate;
      const item = {
        ...req.postDataJSON(),
        id: "55555555-5555-4555-8555-555555555555",
        ...dates,
      };
      state.projects.push(item);
      return json(item, 201);
    }
    if (path === "/projects") return json(state.projects);
    if (path === "/repositories")
      return json(
        repos.filter(
          (r) => r.project_id === url.searchParams.get("project_id"),
        ),
      );
    if (path === "/agents")
      return json(
        agents.filter(
          (a) => a.project_id === url.searchParams.get("project_id"),
        ),
      );
    if (path.endsWith("/run-queue")) {
      const project = path.split("/")[2];
      if (project === A && state.queueGate) await state.queueGate;
      if (state.failQueue) return json({ detail: "Queue unavailable" }, 503);
      const group = url.searchParams.get("group") || "all";
      const q = url.searchParams.get("q") || "";
      const title = project === A ? "Implement named queue" : "Project B work";
      const matches = !q || title.toLowerCase().includes(q.toLowerCase());
      const terminal = ["succeeded", "failed", "cancelled"].includes(
        run.status,
      );
      const inGroup =
        group === "all" ||
        (group === "active" && !terminal) ||
        (group === "failed" && run.status === "failed") ||
        (group === "finished" && terminal);
      const older = !!url.searchParams.get("cursor");
      const items =
        matches && inGroup
          ? [
              {
                run: {
                  ...run,
                  id:
                    project === A
                      ? RUN
                      : `${B.slice(0, 8)}-4444-4444-8444-444444444444`,
                },
                task_title: older ? "Older attempt" : title,
                agent_name: project === A ? agents[0].name : agents[1].name,
                repository:
                  project === A
                    ? { id: repos[0].id, name: repos[0].name }
                    : null,
              },
            ]
          : [];
      return json({
        items,
        next_cursor:
          state.paged && group === "all"
            ? !older
              ? "older-A"
              : url.searchParams.get("cursor") === "older-A"
                ? "oldest-A"
                : ""
            : "",
      });
    }
    if (path === "/runs" && req.method() === "GET") return json([run]);
    if (path === "/external-requests") {
      if (state.failRequests)
        return json({ detail: "Requests unavailable" }, 503);
      const unrouted = url.searchParams.get("unrouted") === "true";
      const items = state.requests.filter(
        (r) =>
          (unrouted
            ? !r.project_id
            : r.project_id === url.searchParams.get("project_id")) &&
          (!url.searchParams.has("attention") ||
            ["needs_routing", "awaiting_approval", "needs_access"].includes(
              String(r.status),
            )),
      );
      return json({
        items: items.slice(0, Number(url.searchParams.get("limit") || 20)),
        next_cursor: "",
      });
    }
    if (path.startsWith("/external-requests/")) {
      const item = state.requests.find((r) => r.id === path.split("/")[2]);
      if (!item) return json({ detail: "Request not found" }, 404);
      if (req.method() === "POST") {
        if (state.requestGate) await state.requestGate;
        item.status = "queued";
      }
      return json(item);
    }
    if (path.endsWith("/integrations/linear/request-routes")) {
      const project = path.split("/")[2];
      return json([
        {
          id: project,
          identity_id: A,
          enabled: true,
          scope_name: "Engineering",
          repository_name: project === A ? "A source" : "B source",
          mode: "approval",
        },
      ]);
    }
    if (path === `/tasks/${TASK}`) {
      if (state.importGate) await state.importGate;
      return json(imported);
    }
    if (path.startsWith("/tasks/") && req.method() === "GET")
      return json({ detail: "Task not found" }, 404);
    if (path === "/tasks" && req.method() === "POST") {
      const body = req.postDataJSON();
      state.taskPosts.push(body);
      return json(
        { ...body, id: TASK, status: "open", external_refs: {}, ...dates },
        201,
      );
    }
    if (path === "/runs" && req.method() === "POST") {
      const body = req.postDataJSON();
      state.runPosts.push(body);
      if (state.launchGate) await state.launchGate;
      if (state.failLaunch) {
        state.failLaunch = false;
        return json({ detail: "Worker unavailable" }, 503);
      }
      return json({ ...run, ...body }, 201);
    }
    if (path === `/runs/${RUN}/execution`)
      return json({
        run,
        task: { ...imported, project_id: A, repository_id: repos[0].id },
        agent: agents[0],
        workspace: null,
        artifacts: [],
        last_event_sequence: 0,
        usage: { input_tokens: 0, output_tokens: 0 },
      });
    if (path.endsWith("/events/stream"))
      return route.fulfill({ contentType: "text/event-stream", body: "" });
    if (path.endsWith("/events") || path.endsWith("/agent-proposals"))
      return json([]);
    return json({ detail: "Fixture endpoint unavailable" }, 404);
  });
  return state;
}
async function selectProject(page: Page, name: string) {
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name, exact: true }).click();
}

test("Project context survives navigation and storage failure", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/");
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toHaveCount(1);
  await selectProject(page, "Project B");
  await page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "Runs", exact: true })
    .click();
  await expect(page).toHaveURL(/\/runs$/);
  await page.reload();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Project B");
  await page.addInitScript(() => {
    Storage.prototype.getItem = () => {
      throw new Error("blocked");
    };
    Storage.prototype.setItem = () => {
      throw new Error("blocked");
    };
  });
  await page.reload();
  await selectProject(page, "Project B");
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Project B");
});

test("launcher retains drafts by Project and partial launch retries the saved Task", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.goto("/");
  const open = page.getByRole("button", { name: "New Task", exact: true });
  await open.click();
  await page.getByLabel("Task title", { exact: true }).fill("Draft A");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(open).toBeFocused();
  await selectProject(page, "Project B");
  await open.click();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue("");
  await expect(
    page.getByRole("combobox", { name: "Repository", exact: true }),
  ).toContainText("Project B Repository");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await selectProject(page, "Project A");
  await open.click();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue(
    "Draft A",
  );
  state.failLaunch = true;
  await page.getByRole("button", { name: "Start Run", exact: true }).click();
  await expect(
    page.getByText(/Task created, but its Run could not start/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await open.click();
  await page
    .getByRole("button", { name: "Retry starting Run", exact: true })
    .click();
  await expect(page).toHaveURL(new RegExp(`/runs/${RUN}$`));
  expect(state.taskPosts).toHaveLength(1);
  expect(state.runPosts).toHaveLength(2);
  expect(state.runPosts[1]).toEqual({
    task_id: TASK,
    agent_id: state.agents[0].id,
  });
});

test("imported and discovery links open the launcher", async ({ page }) => {
  const state = await fixture(page);
  await page.goto(`/?taskId=${TASK}`);
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue(
    "Imported Task",
  );
  await expect(page.getByLabel("Task title", { exact: true })).toHaveAttribute(
    "readonly",
    "",
  );
  await expect(
    page.getByRole("combobox", { name: "Agent", exact: true }),
  ).toContainText("Project B Engineer");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page).not.toHaveURL(/taskId=/);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "New Task", exact: true }),
  ).toBeFocused();
  expect(state.taskPosts).toHaveLength(0);
  await page.goto("/?taskId=missing");
  await expect(page.getByText(/Task not found/)).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Start Run", exact: true }),
  ).toBeDisabled();
});

test("launcher and navigation work with keyboard on mobile", async ({
  page,
}, testInfo) => {
  await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "New Task", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog")).toBeVisible();
  await page
    .getByLabel("Task title", { exact: true })
    .fill("A long Task name ".repeat(20));
  await page.screenshot({
    path: testInfo.outputPath("launcher-mobile.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "New Task", exact: true }),
  ).toBeFocused();
  await page
    .getByRole("button", { name: "Open navigation", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("link", { name: "Runs", exact: true })
    .click();
  await expect(page).toHaveURL(/\/runs$/);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("Run queue preserves filters in navigation", async ({ page }) => {
  const state = await fixture(page);
  await page.goto("/runs");
  await expect(
    page.getByRole("link", { name: "Implement named queue", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Project A Engineer", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("Search Tasks", { exact: true }).fill("missing_%");
  await expect(page).toHaveURL(/q=missing/);
  await expect(page.getByText(/No Runs match/)).toBeVisible();
  expect(state.urls.some((u) => u.startsWith(`/api/v1/tasks/`))).toBe(false);
});

test("Run queue handles Project changes during delayed requests", async ({
  page,
}) => {
  const state = await fixture(page);
  let release!: () => void;
  state.queueGate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.goto("/runs");
  await selectProject(page, "Project B");
  await expect(
    page.getByRole("link", { name: "Project B work", exact: true }),
  ).toBeVisible();
  release();
  await expect(
    page.getByRole("link", { name: "Implement named queue", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("No Repository", { exact: true })).toBeVisible();
});

test("Overview handles independent failures and recovery", async ({ page }) => {
  const state = await fixture(page);
  state.failRequests = true;
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Active Runs", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Implement named queue", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alert").first()).toContainText(
    "Requests unavailable",
  );
  await expect(
    page.getByText("No requests need attention.", { exact: true }),
  ).toHaveCount(0);
  state.failRequests = false;
  await page
    .getByRole("button", { name: "Retry", exact: true })
    .first()
    .click();
  await expect(
    page.getByText("No requests need attention.", { exact: true }).first(),
  ).toBeVisible();
});

function incoming(id: string, project: string, status: string, title: string) {
  return {
    id,
    project_id: project,
    identity_id: A,
    status,
    title,
    reason: "Review these inputs.",
    requester_name: "Alex",
    run_id: "",
    agent_id: "",
    prompt: "Saved instructions",
    input_fingerprint: "a".repeat(64),
    delivery_status: "delivered",
    messages: [],
    ...dates,
  };
}

test("launcher prevents dismissal during a pending launch", async ({
  page,
}) => {
  const state = await fixture(page);
  let release!: () => void;
  state.launchGate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.goto("/");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await page.getByLabel("Task title", { exact: true }).fill("One attempt");
  await page.getByRole("button", { name: "Start Run", exact: true }).click();
  await expect.poll(() => state.runPosts.length).toBe(1);
  await expect(
    page.getByRole("button", { name: "Starting…", exact: true }),
  ).toBeDisabled();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.locator("#project")).toBeDisabled();
  release();
  await expect(page).toHaveURL(new RegExp(`/runs/${RUN}$`));
  expect(state.runPosts).toHaveLength(1);
});

test("a late imported Task response cannot reopen a dismissed dialog", async ({
  page,
}) => {
  const state = await fixture(page);
  let release!: () => void;
  state.importGate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.goto(`/?taskId=${TASK}`);
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page).not.toHaveURL(/taskId=/);
  release();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Project A");
});

test("detail scope follows the record and unassigned routing stays in place", async ({
  page,
}) => {
  const state = await fixture(page);
  state.requests.push(
    incoming(TASK, B, "awaiting_approval", "Assigned request"),
  );
  await page.goto(`/requests/${TASK}`);
  await expect(
    page.getByRole("heading", { name: "Assigned request", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Project B");
  await selectProject(page, "Project A");
  await expect(page).toHaveURL(/\/$/);
  state.requests[0] = incoming(TASK, "", "needs_routing", "Unassigned work");
  await page.goto(`/requests/${TASK}`);
  await page
    .getByRole("combobox", { name: "Destination route", exact: true })
    .click();
  await page
    .getByRole("option", {
      name: "Engineering → A source · approval",
      exact: true,
    })
    .click();
  await selectProject(page, "Project B");
  await expect(page).toHaveURL(new RegExp(`/requests/${TASK}$`));
  await expect(
    page.getByRole("combobox", { name: "Destination route", exact: true }),
  ).toContainText("Configure a matching route");
  await page.goto(`/runs/${RUN}`);
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Project A");
  await selectProject(page, "Project B");
  await expect(page).toHaveURL(/\/$/);
});

test("Setup and request saves hold their Project", async ({ page }) => {
  const state = await fixture(page);
  let releaseSetup!: () => void;
  let releaseRequest!: () => void;
  state.setupGate = new Promise<void>((resolve) => {
    releaseSetup = resolve;
  });
  await page.goto("/setup?section=projects");
  await page.getByLabel("Project name", { exact: true }).fill("Project C");
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeDisabled();
  releaseSetup();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeEnabled();
  state.requests.push({
    ...incoming(TASK, A, "awaiting_approval", "Approved inputs"),
    agent_id: state.agents[0].id,
    agent_name: state.agents[0].name,
  });
  state.requestGate = new Promise<void>((resolve) => {
    releaseRequest = resolve;
  });
  await page.goto(`/requests/${TASK}`);
  await page
    .getByRole("button", { name: "Start reviewed request", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeDisabled();
  releaseRequest();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeEnabled();
});

test("Run queue keeps newest and older pages separate", async ({ page }) => {
  const state = await fixture(page);
  state.paged = true;
  await page.clock.install();
  await page.goto("/runs");
  await page.getByRole("button", { name: "Older Runs", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Older attempt", exact: true }),
  ).toBeVisible();
  const requests = state.urls.filter((u) => u.includes("run-queue")).length;
  await page.clock.fastForward(16_000);
  expect(state.urls.filter((u) => u.includes("run-queue"))).toHaveLength(
    requests,
  );
  await selectProject(page, "Project B");
  await expect(page).not.toHaveURL(/cursor=/);
  await expect(
    page.getByRole("link", { name: "Project B work", exact: true }),
  ).toBeVisible();
  expect(
    state.urls.filter((u) => u.includes(B) && u.includes("cursor=older-A")),
  ).toHaveLength(0);
});

test("Run queue updates statuses and preserves last data on refresh failure", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.clock.install();
  await page.goto("/runs?group=active");
  await expect(
    page.getByRole("link", { name: "Implement named queue", exact: true }),
  ).toBeVisible();
  state.failQueue = true;
  await page.clock.fastForward(2_100);
  await expect(page.getByRole("alert")).toContainText(
    "Showing the last update.",
  );
  await expect(
    page.getByRole("link", { name: "Implement named queue", exact: true }),
  ).toBeVisible();
  state.failQueue = false;
  state.run.status = "succeeded";
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(
    page.getByText("No Runs match these filters.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Finished", exact: true }).click();
  await expect(page.getByText("succeeded", { exact: true })).toBeVisible();
});

test("Overview shows execution and request attention honestly", async ({
  page,
}, testInfo) => {
  const state = await fixture(page);
  state.requests.push(
    incoming(TASK, A, "awaiting_approval", "Review Project A request"),
    incoming(RUN, "", "needs_routing", "Route unassigned work"),
    incoming(B, B, "awaiting_approval", "Project B approval"),
  );
  await page.goto("/");
  await expect(
    page.getByRole("region", { name: "Project requests", exact: true }),
  ).toContainText("Review Project A request");
  await expect(
    page.getByRole("region", {
      name: "Unrouted requests · all Projects",
      exact: true,
    }),
  ).toContainText("Route unassigned work");
  await expect(
    page.getByRole("link", { name: "Project B approval", exact: true }),
  ).toHaveCount(0);
  expect(
    state.urls
      .filter((u) => u.includes("external-requests"))
      .every((u) => u.includes("attention=true") && u.includes("limit=5")),
  ).toBe(true);
  expect(
    state.urls.some(
      (u) => u.includes("group=active") && u.includes("limit=10"),
    ),
  ).toBe(true);
  await expect(
    page.getByRole("button", { name: "Start reviewed request", exact: true }),
  ).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("overview-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("overview-mobile.png"),
    fullPage: true,
  });
});

test("Overview retains unrouted attention without Projects and refreshes reviewed requests", async ({
  page,
}, testInfo) => {
  const state = await fixture(page);
  state.projects.splice(0);
  state.requests.push(
    incoming(TASK, "", "needs_routing", "Find a destination"),
  );
  await page.clock.install();
  await page.goto("/");
  await expect(
    page.getByRole("link", { name: "Open setup", exact: true }),
  ).toBeVisible();
  const section = page.getByRole("region", {
    name: "Unrouted requests · all Projects",
    exact: true,
  });
  await expect(
    section.getByRole("link", { name: "Find a destination", exact: true }),
  ).toBeVisible();
  state.failRequests = true;
  await page.clock.fastForward(15_100);
  await expect(section.getByRole("alert")).toContainText(
    "Showing the last update.",
  );
  await expect(
    section.getByRole("link", { name: "Find a destination", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("attention-error-desktop.png"),
    fullPage: true,
  });
  state.failRequests = false;
  state.requests[0].status = "queued";
  await section.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(
    section.getByText("No requests need attention.", { exact: true }),
  ).toBeVisible();
  await expect(
    section.getByRole("link", { name: "Find a destination", exact: true }),
  ).toHaveCount(0);
});

test("Run queue exposes distinct attempts and long names at both screen sizes", async ({
  page,
}, testInfo) => {
  const state = await fixture(page);
  const title =
    "Investigate repository synchronization and preserve reviewed instructions across the full execution lifecycle";
  await page.route("**/api/v1/projects/*/run-queue?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            run: {
              ...state.run,
              status: "waiting_for_input",
              started_at: null,
            },
            task_title: title,
            agent_name: "Repository investigation and verification specialist",
            repository: null,
          },
          {
            run: {
              ...state.run,
              id: TASK,
              attempt: 2,
              status: "succeeded",
              kind: "pr_review",
              finished_at: "2026-10-03T10:00:05Z",
            },
            task_title: title,
            agent_name: "Review specialist",
            repository: {
              id: A,
              name: "A repository with a descriptive and unusually long name",
            },
          },
        ],
        next_cursor: "",
      },
    }),
  );
  await page.goto("/runs");
  const links = page.getByRole("link", { name: title, exact: true });
  await expect(links).toHaveCount(2);
  await expect(links.nth(0)).toHaveAttribute("href", `/runs/${RUN}`);
  await expect(links.nth(1)).toHaveAttribute("href", `/runs/${TASK}`);
  await expect(page.getByText("No Repository", { exact: true })).toBeVisible();
  await expect(page.getByText("PR review", { exact: true })).toBeVisible();
  await expect(page.getByText("Not started", { exact: true })).toBeVisible();
  await expect(page.getByText("5s", { exact: true })).toBeVisible();
  await expect(page.getByText(/Attempt 2/)).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("queue-long-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await links.first().focus();
  await expect(links.first()).toBeFocused();
  await page.screenshot({
    path: testInfo.outputPath("queue-long-mobile.png"),
    fullPage: true,
  });
});

test("Run queue restores URL filters through browser history", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/runs?group=active&q=Implement");
  await expect(
    page.getByRole("button", { name: "Active", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "Finished", exact: true }).click();
  await expect(page).toHaveURL(/group=finished/);
  await page.goBack();
  await expect(
    page.getByRole("button", { name: "Active", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("textbox", { name: "Search Tasks", exact: true }),
  ).toHaveValue("Implement");
  await page.goForward();
  await expect(
    page.getByRole("button", { name: "Finished", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
});

test("Run queue rejects previous Project cursors restored from history after remount", async ({
  page,
}) => {
  const state = await fixture(page);
  state.paged = true;
  await page.goto("/runs");
  await page.getByRole("button", { name: "Older Runs", exact: true }).click();
  await expect(page).toHaveURL(/cursor=older-A/);
  await page.getByRole("button", { name: "Older Runs", exact: true }).click();
  await expect(page).toHaveURL(/cursor=oldest-A/);
  await selectProject(page, "Project B");
  await expect(page).not.toHaveURL(/cursor=/);
  await page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "Overview", exact: true })
    .click();
  await page.goBack();
  await expect(page).toHaveURL(/\/runs$/);
  await page.goBack();
  await expect(
    page.getByRole("link", { name: "Project B work", exact: true }),
  ).toBeVisible();
  await expect(page).not.toHaveURL(/cursor=/);
  expect(
    state.urls.filter(
      (u) => u.includes(B) && /cursor=(older|oldest)-A/.test(u),
    ),
  ).toHaveLength(0);
});

test("imported launcher reports Project loading failure and can retry", async ({
  page,
}) => {
  await fixture(page);
  let failing = true;
  await page.route("**/api/v1/projects", (route) =>
    failing
      ? route.fulfill({ status: 503, json: { detail: "Projects unavailable" } })
      : route.fallback(),
  );
  await page.goto(`/?taskId=${TASK}`);
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("alert")).toContainText(
    "Projects unavailable",
    { timeout: 12_000 },
  );
  await expect(
    dialog.getByText("Loading Task and launch options…", { exact: true }),
  ).toHaveCount(0);
  failing = false;
  await dialog.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(
    dialog.getByRole("button", { name: "Start Run", exact: true }),
  ).toBeEnabled();
  await expect(
    dialog.getByRole("combobox", { name: "Repository", exact: true }),
  ).toContainText("Project B Repository");
});
