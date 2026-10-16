import { test, expect, type Page } from "@playwright/test";
import { A, B, T, fixture } from "./task-fixture";

const ENGINEER = "55555555-5555-4555-8555-555555555555";
const REVIEWER = "66666666-6666-4666-8666-666666666666";
const FOREIGN = "77777777-7777-4777-8777-777777777777";

async function agentsFixture(page: Page) {
  const state = await fixture(page);
  Object.assign(state.agents[0], {
    backend: "codex",
    instructions: "Implement reliable features.\nKeep changes small.",
    backend_config: { model: "gpt-5.2-codex", reasoning_effort: "high" },
  });
  state.agents.push(
    {
      ...state.agents[0],
      id: REVIEWER,
      name: "Reviewer",
      instructions: "Inspect code and assess accessibility.",
      enabled: false,
    },
    {
      ...state.agents[0],
      id: FOREIGN,
      project_id: B,
      name: "Foreign Agent",
      instructions: "Private Project B instructions",
    },
  );
  const writes: string[] = [];
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (!["GET", "HEAD", "OPTIONS"].includes(request.method()))
      writes.push(`${request.method()} ${request.url()}`);
  });
  return { ...state, writes, errors };
}

async function selectProject(page: Page, name: string) {
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name, exact: true }).click();
}

async function runFixture(
  page: Page,
  state: Awaited<ReturnType<typeof agentsFixture>>,
) {
  const records = [
    {
      id: "88888888-8888-4888-8888-888888888888",
      title: "Actual Engineer execution",
      agent: ENGINEER,
      status: "failed",
    },
    {
      id: "99999999-9999-4999-8999-999999999999",
      title: "Older Engineer execution",
      agent: ENGINEER,
      status: "succeeded",
    },
    {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      title: "Preferred Engineer but Reviewer executed",
      agent: REVIEWER,
      status: "running",
    },
  ];
  await page.route("**/run-queue?**", async (route) => {
    const url = new URL(route.request().url());
    state.urls.push(url.pathname.replace("/api/v1", "") + url.search);
    const q = url.searchParams;
    const group = q.get("group");
    const filtered = records.filter(
      (run) =>
        (!q.get("agent_id") || q.get("agent_id") === run.agent) &&
        run.title.toLowerCase().includes((q.get("q") ?? "").toLowerCase()) &&
        (group === "failed"
          ? run.status === "failed"
          : group === "active"
            ? run.status === "running"
            : group === "finished"
              ? run.status !== "running"
              : true),
    );
    const start = q.get("cursor") ? 1 : 0;
    await route.fulfill({
      json: {
        items: filtered.slice(start, start + 1).map((item) => ({
          run: {
            id: item.id,
            task_id: T,
            agent_id: item.agent,
            status: item.status,
            backend: "codex",
            kind: "coding",
            attempt: start + 1,
            created_at: "2026-10-03T10:00:00Z",
            updated_at: "2026-10-03T10:00:00Z",
          },
          task_title: item.title,
          agent_name: item.agent === ENGINEER ? "Engineer" : "Reviewer",
          repository: null,
        })),
        next_cursor: !start && filtered.length > 1 ? "older" : "",
      },
    });
  });
}

test("Project Agents directory opens each Agent without writing configuration", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  await page.goto("/agents");
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Reviewer", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Foreign Agent", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("link", { name: "Engineer", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/agents/${ENGINEER}`));
  await expect(
    page.getByRole("tab", { name: "Overview", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(
    page.getByRole("tab", { name: "Tasks", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("tab", { name: "Runs", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Implement reliable features.\nKeep changes small.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByText("gpt-5.2-codex", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Settings", exact: true }),
  ).toHaveAttribute("href", /\/setup\?section=agents/);
  await expect(page.getByRole("textbox")).toHaveCount(0);
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("directory presets and instruction search retain Project isolation", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  await page.goto("/agents");
  await page.getByRole("button", { name: "Enabled", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Reviewer", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Disabled", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Reviewer", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "All Agents", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Search Agents", exact: true })
    .fill("accessibility");
  await expect(
    page.getByRole("link", { name: "Reviewer", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Search Agents", exact: true })
    .fill("");
  await selectProject(page, "Project B");
  await expect(
    page.getByRole("link", { name: "Foreign Agent", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toHaveCount(0);
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("switching Project leaves the Agent detail and drops its scoped filters", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  await runFixture(page, state);
  await page.goto(
    `/agents/${ENGINEER}?tab=runs&group=finished&q=Older&cursor=older`,
  );
  await expect(
    page.getByRole("tab", { name: "Runs", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await selectProject(page, "Project B");
  await expect(page).toHaveURL(/\/agents$/);
  await expect(
    page.getByRole("link", { name: "Foreign Agent", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Implement reliable features.", { exact: false }),
  ).toHaveCount(0);
  expect(
    state.urls.some((url) => url.startsWith(`/projects/${B}/run-queue`)),
  ).toBe(false);
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("a foreign Agent link cannot expose another Project's instructions or history", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  await page.goto(`/agents/${FOREIGN}?tab=runs`);
  await expect(page.getByText(/Agent not found/i)).toBeVisible();
  await expect(
    page.getByText("Private Project B instructions", { exact: true }),
  ).toHaveCount(0);
  expect(
    state.urls.some(
      (url) => url.includes("run-queue") || url.includes("task-planning"),
    ),
  ).toBe(false);
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("Agent Tasks preview counts every preferred Task and links into all existing layouts", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  for (let i = 1; i < 8; i++)
    state.tasks.push({
      ...state.tasks[0],
      id: `preferred-${i}`,
      title: `Preferred Task ${i}`,
    });
  state.tasks.push({
    ...state.tasks[0],
    id: "cancelled-preferred",
    title: "Cancelled preferred Task",
    status: "cancelled",
  });
  state.tasks.push({
    ...state.tasks[0],
    id: "different-preference",
    title: "Another Agent's Task",
    preferred_agent_id: REVIEWER,
  });
  await page.goto(`/agents/${ENGINEER}?tab=tasks`);
  const backlog = page.getByRole("region", { name: "Backlog", exact: true });
  await expect(
    backlog.getByRole("link", { name: "Plan the console", exact: true }),
  ).toBeVisible();
  await expect(backlog).toContainText("8");
  await expect(backlog.getByRole("listitem")).toHaveCount(5);
  await expect(
    page.getByRole("heading", { name: "Preferred Tasks · 9", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Cancelled preferred Task", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Another Agent's Task", exact: true }),
  ).toHaveCount(0);
  await expect
    .poll(() =>
      state.urls.some((path) => {
        const url = new URL(path, "http://fixture");
        return (
          url.pathname === `/projects/${A}/task-planning` &&
          url.searchParams.get("scope") === "all" &&
          url.searchParams.get("preferred_agent_id") === ENGINEER &&
          url.searchParams.get("limit") === "5"
        );
      }),
    )
    .toBe(true);
  for (const [name, view] of [
    ["List", "list"],
    ["Kanban", "board"],
    ["Gantt", "timeline"],
  ]) {
    const href = await page
      .getByRole("link", { name, exact: true })
      .getAttribute("href");
    const url = new URL(href!, "http://fixture");
    expect(url.pathname).toBe("/tasks");
    expect(Object.fromEntries(url.searchParams)).toMatchObject({
      scope: "all",
      preferred_agent_id: ENGINEER,
    });
    expect(url.searchParams.get("view") ?? "list").toBe(view);
  }
  await page.getByRole("link", { name: "Gantt", exact: true }).click();
  await expect(
    page.getByRole("tab", { name: "Gantt", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  expect(new URL(page.url()).searchParams.get("preferred_agent_id")).toBe(
    ENGINEER,
  );
  await page.goBack();
  await expect(
    page.getByRole("tab", { name: "Tasks", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await backlog
    .getByRole("link", { name: "Plan the console", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Task details", exact: true }),
  ).toBeVisible();
  expect(Object.fromEntries(new URL(page.url()).searchParams)).toMatchObject({
    scope: "all",
    preferred_agent_id: ENGINEER,
    taskId: T,
  });
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("Agent Runs page filters actual execution, pages, and restores tab history", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  await runFixture(page, state);
  await page.goto(`/agents/${ENGINEER}`);
  await page.getByRole("tab", { name: "Runs", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Actual Engineer execution", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", {
      name: "Preferred Engineer but Reviewer executed",
      exact: true,
    }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Older Runs", exact: true }).click();
  await expect(page).toHaveURL(/cursor=older/);
  await expect(
    page.getByRole("link", { name: "Older Engineer execution", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Failed", exact: true }).click();
  await expect(page).not.toHaveURL(/cursor=older/);
  await expect(
    page.getByRole("link", { name: "Actual Engineer execution", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Older Engineer execution", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("tab", { name: "Overview", exact: true }).click();
  await page.goBack();
  await expect(
    page.getByRole("tab", { name: "Runs", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(
    page.getByRole("button", { name: "Failed", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("button", { name: "All", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Search Tasks", exact: true })
    .fill("Older");
  await expect(
    page.getByRole("link", { name: "Older Engineer execution", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Actual Engineer execution", exact: true }),
  ).toHaveCount(0);
  const reads = state.urls.filter((url) => url.includes("run-queue"));
  expect(reads.length).toBeGreaterThan(3);
  for (const read of reads) {
    const url = new URL(read, "http://fixture");
    expect(url.pathname).toBe(`/projects/${A}/run-queue`);
    expect(url.searchParams.get("agent_id")).toBe(ENGINEER);
    expect(url.searchParams.has("preferred_agent_id")).toBe(false);
    expect(url.searchParams.has("task_id")).toBe(false);
  }
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("Agents loading, error retry, and empty Project states stay recoverable", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  let fail = true;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/agents?**", async (route) => {
    await gate;
    if (fail)
      return route.fulfill({
        status: 503,
        json: { detail: "Agents unavailable" },
      });
    return route.fallback();
  });
  await page.goto("/agents");
  await expect(
    page.getByRole("status").filter({ hasText: "Loading Agents" }),
  ).toBeVisible();
  release();
  await expect(
    page.getByText("Could not load Agents.", { exact: false }),
  ).toBeVisible({ timeout: 10000 });
  fail = false;
  await page.getByRole("button", { name: "Retry Agents", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toBeVisible();
  state.agents.splice(0);
  await selectProject(page, "Project B");
  await expect(page.getByText(/No Agents/i).first()).toBeVisible();
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

for (const width of [390, 1024, 1440]) {
  test(`Agent directory and profile use the workspace at ${width}px without page overflow`, async ({
    page,
  }, info) => {
    await page.setViewportSize({ width, height: 900 });
    const state = await agentsFixture(page);
    state.agents[0].name = "Engineer with a long project-specific Agent name";
    await runFixture(page, state);
    await page.goto("/agents");
    const row = page.getByRole("link", {
      name: state.agents[0].name,
      exact: true,
    });
    await expect(row).toBeVisible();
    await page.screenshot({
      path: info.outputPath(`agents-directory-${width}.png`),
      fullPage: true,
    });
    await row.focus();
    await page.keyboard.press("Enter");
    await expect(
      page.getByRole("tab", { name: "Overview", exact: true }),
    ).toHaveAttribute("aria-selected", "true");
    const breadcrumb = page.getByRole("navigation", {
      name: "Breadcrumb",
      exact: true,
    });
    const project = breadcrumb.getByRole("combobox", {
      name: "Project",
      exact: true,
    });
    const separator = await breadcrumb.locator("li").nth(1).boundingBox();
    for (const element of [project, project.locator("svg")]) {
      const box = await element.boundingBox();
      expect(box!.x + box!.width).toBeLessThanOrEqual(separator!.x);
    }
    for (const tab of ["Overview", "Tasks", "Runs"]) {
      await page.getByRole("tab", { name: tab, exact: true }).click();
      await expect(
        page.getByRole("tab", { name: tab, exact: true }),
      ).toHaveAttribute("aria-selected", "true");
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        ),
      ).toBeLessThanOrEqual(1);
      await page.screenshot({
        path: info.outputPath(`agent-${tab.toLowerCase()}-${width}.png`),
        fullPage: true,
      });
    }
    expect(state.writes).toHaveLength(0);
    expect(state.errors).toHaveLength(0);
  });
}

test("late Agent responses cannot populate a newly selected Project", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(`**/api/v1/agents?project_id=${A}`, async (route) => {
    await gate;
    await route.fallback();
  });
  await page.goto("/agents");
  await expect(
    page.getByRole("status").filter({ hasText: "Loading Agents" }),
  ).toBeVisible();
  await selectProject(page, "Project B");
  await expect(
    page.getByRole("link", { name: "Foreign Agent", exact: true }),
  ).toBeVisible();
  const completed = page.waitForResponse((response) =>
    response.url().includes(`/agents?project_id=${A}`),
  );
  release();
  await completed;
  await expect(
    page.getByRole("link", { name: "Engineer", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Foreign Agent", exact: true }),
  ).toBeVisible();
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});

test("Agent Tasks and Runs recover independently from errors and show empty states", async ({
  page,
}) => {
  const state = await agentsFixture(page);
  state.tasks.splice(0);
  let failTasks = true;
  let failRuns = true;
  await page.route("**/task-planning?**", async (route) => {
    if (failTasks)
      return route.fulfill({
        status: 503,
        json: { detail: "Planning unavailable" },
      });
    return route.fallback();
  });
  await page.route("**/run-queue?**", async (route) => {
    if (failRuns)
      return route.fulfill({
        status: 503,
        json: { detail: "Runs unavailable" },
      });
    return route.fallback();
  });
  await page.goto(`/agents/${ENGINEER}?tab=tasks`);
  await expect(
    page.getByText("Could not load Tasks.", { exact: false }),
  ).toBeVisible();
  failTasks = false;
  await page.getByRole("button", { name: "Retry Tasks", exact: true }).click();
  await expect(
    page.getByText("No Tasks prefer this Agent yet.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "Runs", exact: true }).click();
  await expect(
    page.getByText("Could not load Runs.", { exact: false }),
  ).toBeVisible();
  failRuns = false;
  await page.getByRole("button", { name: "Retry Runs", exact: true }).click();
  await expect(
    page.getByText("No Runs match these filters.", { exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "Overview", exact: true }).click();
  await expect(
    page.getByRole("region", { name: "Agent instructions", exact: true }),
  ).toBeVisible();
  expect(state.writes).toHaveLength(0);
  expect(state.errors).toHaveLength(0);
});
