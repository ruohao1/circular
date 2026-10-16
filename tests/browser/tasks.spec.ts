import { test, expect } from "@playwright/test";
import { A, B, T, dates, fixture } from "./task-fixture";

for (const width of [390, 1440]) {
  test(`compact List keeps task and execution metadata distinct at ${width}px`, async ({
    page,
  }, info) => {
    const state = await fixture(page);
    const title =
      "Compact planning task with a deliberately long title and an unbroken-reference-abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789";
    state.tasks[0].title = title;
    state.tasks[0].priority = "high";
    state.tasks[0].planned_start_date = "2026-10-16";
    state.tasks[0].planned_finish_date = "2026-10-20";
    state.agents[0].name = "Preferred specialist";
    state.agents[0].enabled = false;
    state.active = true;
    state.latestRun = {
      id: B,
      agent_id: "66666666-6666-4666-8666-666666666666",
      agent_name: "Actual executor",
      attempt: 3,
      created_at: dates.created_at,
      started_at: dates.created_at,
      finished_at: dates.updated_at,
      kind: "coding",
      status: "succeeded",
    };
    await page.setViewportSize({ width, height: 1000 });
    await page.goto(
      "/tasks?view=list&q=Compact&priority=high&date=2026-10-16&scale=month",
    );
    const group = page.getByRole("region", { name: "Backlog", exact: true });
    const row = group.getByRole("listitem");
    const link = row.getByRole("link", { name: title, exact: true });
    await expect(row).toHaveCount(1);
    await expect(link).toBeVisible();
    for (const value of [
      /Backlog/,
      /High/,
      /Source/,
      /Preferred specialist/,
      /disabled/i,
      /2026-10-16.*2026-10-20/,
      /succeeded/i,
      /Actual executor/,
      /Attempt 3/i,
      /1 active Run/,
    ]) {
      await expect(link).toHaveAccessibleDescription(value);
    }
    await expect(link.getByText("succeeded", { exact: true })).toBeVisible();
    await expect(link.getByText("1 active", { exact: true })).toBeVisible();
    if (width === 1440) {
      await expect(link.getByText(/Preferred specialist/)).toBeVisible();
      await expect(link.getByText("Disabled", { exact: true })).toBeVisible();
      const bounds = (await row.boundingBox())!;
      expect(bounds.height).toBeGreaterThanOrEqual(40);
      expect(bounds.height).toBeLessThanOrEqual(48);
    }
    const rowBounds = (await row.boundingBox())!;
    expect(rowBounds.height).toBeLessThanOrEqual(64);
    const linkBounds = (await link.boundingBox())!;
    expect(Math.abs(rowBounds.width - linkBounds.width)).toBeLessThanOrEqual(2);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath(`compact-list-${width}.png`),
    });
    await link.click();
    await expect(
      page.getByRole("dialog").getByLabel("Task title", { exact: true }),
    ).toHaveValue(title);
    const url = new URL(page.url());
    expect(url.searchParams.get("view") ?? "list").toBe("list");
    expect(Object.fromEntries(url.searchParams)).toMatchObject({
      q: "Compact",
      priority: "high",
      date: "2026-10-16",
      scale: "month",
      taskId: T,
    });
    expect(state.posts).toHaveLength(0);
    expect(state.tasks[0].status).toBe("open");
  });
}

test("compact List retains server totals while paging a filtered stage", async ({
  page,
}) => {
  const state = await fixture(page);
  for (let i = 0; i < 54; i++) {
    state.tasks.push({
      ...state.tasks[0],
      id: `compact-${i}`,
      title: `Compact ${i}`,
      priority: "high",
    });
  }
  await page.goto("/tasks?view=list&priority=high&q=Compact");
  const group = page.getByRole("region", { name: "Backlog", exact: true });
  await expect(group.getByRole("listitem")).toHaveCount(50);
  await expect(group.getByText("54", { exact: true })).toBeVisible();
  await group
    .getByRole("button", { name: "Load more Backlog Tasks", exact: true })
    .click();
  await expect(group.getByRole("listitem")).toHaveCount(54);
  await expect(group.getByText("54", { exact: true })).toBeVisible();
  expect(
    state.urls.some(
      (url) =>
        url.includes("cursor=2%3A50") &&
        url.includes("priority=high") &&
        url.includes("q=Compact"),
    ),
  ).toBe(true);
  expect(state.posts).toHaveLength(0);
});

for (const width of [390, 1440]) {
  test(`Kanban quick add saves in its column and updates every view at ${width}px`, async ({
    page,
  }, info) => {
    const state = await fixture(page);
    await page.setViewportSize({ width, height: 1000 });
    await page.goto("/tasks?view=timeline&date=2026-10-14&scale=day&q=Launch");
    await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(0);
    await page.getByRole("tab", { name: "Kanban", exact: true }).click();
    const add = page.getByRole("button", {
      name: "Add Task to Ready",
      exact: true,
    });
    await add.click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("combobox", { name: "Workflow stage", exact: true }),
    ).toContainText("Ready");
    await expect(
      dialog.getByRole("button", { name: "Start Run", exact: true }),
    ).toHaveCount(0);
    await dialog.getByLabel("Task title", { exact: true }).fill("Launch plan");
    await dialog
      .getByLabel("Planned start", { exact: true })
      .fill("2026-10-14");
    await dialog
      .getByLabel("Planned finish", { exact: true })
      .fill("2026-10-13");
    await expect(
      dialog.getByRole("button", { name: "Save Task", exact: true }),
    ).toBeDisabled();
    expect(state.posts).toHaveLength(0);
    await dialog
      .getByLabel("Planned finish", { exact: true })
      .fill("2026-10-16");
    await page.screenshot({ path: info.outputPath(`quick-add-${width}.png`) });
    await dialog.getByLabel("Task title", { exact: true }).press("Enter");
    await expect(dialog).not.toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Ready", exact: true })
        .getByRole("link", { name: "Launch plan", exact: true }),
    ).toBeVisible();
    await expect(add).toBeFocused();
    await expect(page).toHaveURL(/view=board/);
    await expect(page).toHaveURL(/q=Launch/);
    await expect(page).not.toHaveURL(/taskId=/);
    expect(state.posts).toHaveLength(1);
    expect(state.posts[0]).toMatchObject({
      path: "/tasks",
      body: {
        title: "Launch plan",
        status: "ready",
        planned_start_date: "2026-10-14",
        planned_finish_date: "2026-10-16",
      },
    });
    await page.getByRole("tab", { name: "List", exact: true }).click();
    await expect(
      page.getByRole("link", { name: "Launch plan", exact: true }),
    ).toBeVisible();
    await page.getByRole("tab", { name: "Gantt", exact: true }).click();
    await expect(
      page
        .locator('[data-slot="gantt-bar"]')
        .filter({ hasText: "Launch plan" }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
}

test("Kanban quick add retains separate drafts and recovers from a failed save", async ({
  page,
}) => {
  const state = await fixture(page);
  state.repositories.length = 0;
  state.agents.length = 0;
  await page.goto("/tasks?view=board");
  const dialog = page.getByRole("dialog");
  const title = dialog.getByLabel("Task title", { exact: true });
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await title.fill("Global draft");
  await page.keyboard.press("Escape");
  const add = page.getByRole("button", {
    name: "Add Task to Ready",
    exact: true,
  });
  await add.click();
  await title.fill("Ready draft");
  await dialog.getByLabel("Planned start", { exact: true }).fill("2026-10-14");
  state.writeError = 503;
  await dialog.getByRole("button", { name: "Save Task", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("could not be saved");
  await expect(title).toHaveValue("Ready draft");
  await page.keyboard.press("Escape");
  await expect(add).toBeFocused();
  await page
    .getByRole("button", { name: "Add Task to Review", exact: true })
    .click();
  await expect(title).toHaveValue("");
  await expect(
    dialog.getByRole("combobox", { name: "Workflow stage", exact: true }),
  ).toContainText("Review");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await expect(title).toHaveValue("Global draft");
  await page.keyboard.press("Escape");
  await add.click();
  await expect(title).toHaveValue("Ready draft");
  await expect(dialog.getByLabel("Planned start", { exact: true })).toHaveValue(
    "2026-10-14",
  );
  state.writeError = 0;
  let release!: () => void;
  state.writeGate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await dialog.getByRole("button", { name: "Save Task", exact: true }).click();
  await expect(
    dialog.getByRole("button", { name: "Saving…", exact: true }),
  ).toBeDisabled();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeVisible();
  await expect(page.locator("#project")).toBeDisabled();
  release();
  await expect(dialog).not.toBeVisible();
  await expect(
    page
      .getByRole("region", { name: "Ready", exact: true })
      .getByRole("link", { name: "Ready draft", exact: true }),
  ).toBeVisible();
  expect(state.posts).toHaveLength(2);
  expect(state.posts.every((p) => p.path === "/tasks")).toBe(true);
  expect(state.tasks.filter((t) => t.title === "Ready draft")).toHaveLength(1);
  await add.click();
  await expect(title).toHaveValue("");
});

test("Save Task never starts execution, even without Repository or Agent", async ({
  page,
}) => {
  const state = await fixture(page);
  state.repositories.length = 0;
  state.agents.length = 0;
  await page.goto("/tasks");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Task title", { exact: true }).fill("Prepare work");
  await dialog.getByRole("button", { name: "Save Task", exact: true }).click();
  await expect(page).toHaveURL(/\/tasks.*taskId=/);
  await expect(
    page.getByRole("dialog").getByLabel("Task title", { exact: true }),
  ).toHaveValue("Prepare work");
  expect(state.posts.filter((p) => p.path === "/tasks")).toHaveLength(1);
  expect(state.posts.filter((p) => p.path === "/runs")).toHaveLength(0);
});
test("intentional empty launcher selections survive closing and reopening", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/tasks");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "Agent", exact: true }).click();
  await page
    .getByRole("option", { name: "No preferred Agent", exact: true })
    .click();
  await dialog
    .getByRole("combobox", { name: "Repository", exact: true })
    .click();
  await page
    .getByRole("option", { name: "No Repository", exact: true })
    .click();
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await expect(
    dialog.getByRole("combobox", { name: "Agent", exact: true }),
  ).toContainText("No preferred Agent");
  await expect(
    dialog.getByRole("combobox", { name: "Repository", exact: true }),
  ).toContainText("No Repository");
});
test("Task List keeps drafts on conflicts and requires explicit reload", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await dialog
    .getByLabel("Task title", { exact: true })
    .fill("My unsaved title");
  await expect(page.locator("#project")).toBeDisabled();
  state.conflict = true;
  await dialog
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(dialog.getByText(/changed elsewhere/)).toBeVisible();
  await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
    "My unsaved title",
  );
  state.conflict = false;
  state.tasks[0].title = "Other editor";
  state.tasks[0].revision++;
  await dialog
    .getByRole("button", { name: "Reload latest", exact: true })
    .click();
  await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
    "Other editor",
  );
});
test("Saved Task launch requires explicit replacement for a disabled preferred Agent", async ({
  page,
}) => {
  const state = await fixture(page);
  state.agents[0].enabled = false;
  state.agents.push({
    ...state.agents[0],
    id: "77777777-7777-4777-8777-777777777777",
    name: "Replacement",
    enabled: true,
  });
  await page.goto(`/?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("heading", { name: "Saved Task", exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Start Run", exact: true }),
  ).toBeDisabled();
  await dialog.getByRole("combobox", { name: "Agent", exact: true }).click();
  await page.getByRole("option", { name: /Replacement/ }).click();
  await dialog.getByRole("button", { name: "Start Run", exact: true }).click();
  await expect(dialog.getByText(/could not start/)).toBeVisible();
  expect(state.posts.filter((p) => p.path === "/tasks")).toHaveLength(0);
  expect(state.posts.find((p) => p.path === "/runs")?.body).toEqual({
    task_id: T,
    agent_id: state.agents[1].id,
  });
});
test("Task history passes its Task filter and keeps older attempts accessible", async ({
  page,
}) => {
  const state = await fixture(page);
  state.history = true;
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("link", { name: "Latest attempt", exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Older Runs", exact: true }).click();
  await expect(
    dialog.getByRole("link", { name: "Older attempt", exact: true }),
  ).toBeVisible();
  expect(
    state.urls.some(
      (u) => u.includes("task_id=" + T) && u.includes("limit=20"),
    ),
  ).toBe(true);
});
test("source content is readonly while local planning metadata stays editable", async ({
  page,
}) => {
  const state = await fixture(page);
  state.tasks[0].external_refs = {
    linear: { issue_id: "source", url: "https://linear.app/example" },
  };
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByLabel("Task title", { exact: true }),
  ).toHaveAttribute("readonly", "");
  await dialog.getByRole("combobox", { name: "Priority", exact: true }).click();
  await page.getByRole("option", { name: "Urgent", exact: true }).click();
  await dialog
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect.poll(() => state.tasks[0].priority).toBe("urgent");
});
test("dirty detail guards leaving and clean Project changes clear detail and resource filters", async ({
  page,
}) => {
  await fixture(page);
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Description", { exact: true }).fill("Unsaved");
  page.once("dialog", (d) => d.dismiss());
  await page.keyboard.press("Escape");
  await expect(dialog.getByLabel("Description", { exact: true })).toHaveValue(
    "Unsaved",
  );
  page.once("dialog", (d) => d.accept());
  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: "Project B", exact: true }).click();
  await expect(page).not.toHaveURL(/taskId=/);
  await expect(
    page.getByRole("link", { name: "Plan the console", exact: true }),
  ).not.toBeVisible();
});

test("discarding a detail draft does not resurrect it when reopened", async ({
  page,
}) => {
  await fixture(page);
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Task title", { exact: true }).fill("Discard me");
  page.once("dialog", (d) => d.accept());
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page
    .getByRole("link", { name: "Plan the console", exact: true })
    .click();
  await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
    "Plan the console",
  );
});

test("late planning responses cannot populate another Project", async ({
  page,
}) => {
  const state = await fixture(page);
  let release!: () => void;
  state.gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.goto("/tasks");
  await expect(
    page.getByRole("status", { name: "" }).filter({ hasText: "Loading Tasks" }),
  ).toBeVisible();
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: "Project B", exact: true }).click();
  release();
  await expect(
    page.getByRole("status").filter({ hasText: "Loading Tasks" }),
  ).not.toBeVisible();
  await expect(
    page.getByRole("link", { name: "Plan the console", exact: true }),
  ).not.toBeVisible();
});

test("Task detail deep links align Project and Back never revives a foreign selection", async ({
  page,
}) => {
  const state = await fixture(page);
  state.tasks[0].project_id = B;
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
    "Plan the console",
  );
  await expect(page.locator("#project")).toContainText("Project B");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page
    .getByRole("combobox", { name: "Priority filter", exact: true })
    .click();
  await page.getByRole("option", { name: "High", exact: true }).click();
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: "Project A", exact: true }).click();
  await page.goBack();
  await expect(page.locator("#project")).toContainText("Project A");
  await expect(dialog).not.toBeVisible();
});

test("Kanban supports ordinary moves and a shared Task detail across views", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.goto("/tasks?view=board");
  await expect(
    page.getByRole("button", { name: "Move Plan the console", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Move Plan the console", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: "Move to Ready", exact: true })
    .click();
  await expect.poll(() => state.tasks[0].status).toBe("ready");
  expect(state.posts.filter((p) => p.path.endsWith("/move"))).toHaveLength(1);
  expect(state.posts.filter((p) => p.path === "/runs")).toHaveLength(0);
  await page
    .getByRole("link", { name: "Plan the console", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "Show in List", exact: true })
    .click();
  await expect(page).toHaveURL(/taskId=/);
  await expect(page).not.toHaveURL(/view=board/);
  await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
    "Plan the console",
  );
});

test("keyboard card moves commit once and Escape cancels without a request", async ({
  page,
}) => {
  const state = await fixture(page);
  await page.goto("/tasks?view=board");
  const handle = page.getByRole("button", {
    name: "Drag Plan the console",
    exact: true,
  });
  await handle.focus();
  await page.keyboard.press("Space");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Plan the console.",
  );
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Ready.",
  );
  await page.keyboard.press("Escape");
  expect(state.posts.filter((p) => p.path.endsWith("/move"))).toHaveLength(0);
  await handle.focus();
  await page.keyboard.press("Space");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Plan the console.",
  );
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Ready.",
  );
  await page.keyboard.press("Space");
  await expect
    .poll(() => state.posts.filter((p) => p.path.endsWith("/move")).length)
    .toBe(1);
  await expect.poll(() => state.tasks[0].status).toBe("ready");
  await expect(handle).toBeFocused();
});

test("failed Kanban moves keep canonical placement and allow an explicit retry", async ({
  page,
}) => {
  const state = await fixture(page);
  state.conflict = true;
  await page.goto("/tasks?view=board");
  await page
    .getByRole("button", { name: "Move Plan the console", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: "Move to Ready", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "could not be saved" }),
  ).toBeVisible();
  expect(state.tasks[0].status).toBe("open");
  state.conflict = false;
  await page
    .getByRole("button", { name: "Move Plan the console", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: "Move to Ready", exact: true })
    .click();
  await expect.poll(() => state.tasks[0].status).toBe("ready");
});

async function dragTo(page: Page, title: string, x: number, y: number) {
  const handle = page.getByRole("button", {
    name: `Drag ${title}`,
    exact: true,
  });
  const box = (await handle.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + 12, box.y + box.height / 2, {
    steps: 3,
  });
  await expect(page.locator('[data-slot="kanban"]')).toHaveAttribute(
    "data-dragging",
    "true",
  );
  await page.mouse.move(x, y, { steps: 12 });
  await page.mouse.up();
}

test("pointer drops use visible anchors, column headers use global end, and outside cancels", async ({
  page,
}) => {
  const state = await fixture(page);
  state.tasks.push({
    ...state.tasks[0],
    id: B,
    title: "Visible target",
    status: "ready",
  });
  await page.goto("/tasks?view=board");
  await expect(
    page.getByRole("link", { name: "Visible target", exact: true }),
  ).toBeVisible();
  const card = (await page
    .locator(`[data-slot="kanban-item"][data-value="${B}"]`)
    .boundingBox())!;
  await dragTo(page, "Plan the console", card.x + 30, card.y + 25);
  await expect
    .poll(() => state.posts.filter((p) => p.path.endsWith("/move")).length)
    .toBe(1);
  expect(state.posts.at(-1)!.body).toMatchObject({
    status: "ready",
    placement: "before",
    anchor_task_id: B,
  });
  await expect(
    page.getByRole("button", { name: "Drag Plan the console", exact: true }),
  ).toBeEnabled();
  await dragTo(page, "Plan the console", 20, 10);
  expect(state.posts.filter((p) => p.path.endsWith("/move"))).toHaveLength(1);
  const header = (await page
    .getByRole("region", { name: "Backlog", exact: true })
    .getByRole("heading")
    .boundingBox())!;
  await dragTo(page, "Plan the console", header.x + 20, header.y + 10);
  await expect
    .poll(() => state.posts.filter((p) => p.path.endsWith("/move")).length)
    .toBe(2);
  expect(state.posts.at(-1)!.body).toMatchObject({
    status: "open",
    placement: "end",
  });
  expect(state.posts.filter((p) => p.path === "/runs")).toHaveLength(0);
});

test("paged and filtered boards preserve scopes and reload stale pages", async ({
  page,
}) => {
  const state = await fixture(page);
  for (let i = 0; i < 54; i++)
    state.tasks.push({
      ...state.tasks[0],
      id: `extra-${i}`,
      title: `Visible ${i}`,
      priority: "high",
    });
  await page.goto("/tasks?view=board&priority=high&q=Visible");
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(50);
  await page
    .getByRole("button", { name: "Load more Backlog Tasks", exact: true })
    .click();
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(54);
  await expect(
    page.getByRole("button", { name: "Load more Backlog Tasks", exact: true }),
  ).toHaveCount(0);
  expect(
    state.urls.some(
      (u) =>
        u.includes("cursor=2%3A50") &&
        u.includes("priority=high") &&
        u.includes("q=Visible"),
    ),
  ).toBe(true);
  await page.reload();
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(50);
  state.board++;
  await page
    .getByRole("button", { name: "Load more Backlog Tasks", exact: true })
    .click();
  await expect(page.locator('p[role="status"]')).toContainText("changed");
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(50);
  await page
    .getByRole("button", { name: "Load more Backlog Tasks", exact: true })
    .click();
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(54);
  await page
    .getByRole("button", { name: "Move Visible 1", exact: true })
    .click();
  await page.getByRole("menuitem", { name: "Move down", exact: true }).click();
  await expect.poll(() => state.posts.length).toBe(1);
  expect(state.posts[0].body).toMatchObject({
    placement: "down",
    expected_board_revision: 3,
  });
});

test("search debounce preserves a newly selected view and filters", async ({
  page,
}) => {
  await fixture(page);
  await page.goto("/tasks");
  await page.getByRole("textbox", { name: "Search Tasks" }).fill("Plan");
  await page.getByRole("tab", { name: "Kanban", exact: true }).click();
  await expect(page).toHaveURL(/view=board/);
  await expect(page).toHaveURL(/q=Plan/);
  await expect(
    page.getByRole("tab", { name: "Kanban", exact: true }),
  ).toHaveAttribute("data-state", "active");
});

test("List and Kanban use the wide workspace", async ({ page }, info) => {
  await fixture(page);
  await page.setViewportSize({ width: 2560, height: 1440 });
  await page.goto("/tasks?view=list");
  const main = await page.getByRole("main").boundingBox();
  const list = page.getByRole("tabpanel");
  await expect(
    list.getByText("Plan the console", { exact: true }),
  ).toBeVisible();
  expect((await list.boundingBox())!.width).toBeGreaterThan(main!.width * 0.95);
  await page.screenshot({ path: info.outputPath("wide-list.png") });
  await page.getByRole("tab", { name: "Kanban", exact: true }).click();
  const columns = page.locator('[data-slot="kanban-column"]');
  await expect(columns.first()).toBeVisible();
  const first = (await columns.first().boundingBox())!;
  const last = (await columns.last().boundingBox())!;
  expect(last.x + last.width - first.x).toBeGreaterThan(main!.width * 0.95);
  expect(first.height).toBeGreaterThan(1440 * 0.6);
  expect(first.y + first.height).toBeGreaterThan(1440 * 0.95);
  expect(last.x + last.width).toBeLessThanOrEqual(2560);
  await page.screenshot({ path: info.outputPath("wide-kanban.png") });
});

for (const width of [390, 1440])
  test(`long names, cancelled filter and conflicts fit ${width}px`, async ({
    page,
  }, info) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const state = await fixture(page);
    state.tasks[0].title = "Long task title ".repeat(12);
    state.agents[0].name = "Preferred Agent ".repeat(8);
    state.agents[0].enabled = false;
    state.repositories[0].name = "Repository ".repeat(10);
    state.tasks.push({
      ...state.tasks[0],
      id: B,
      title: "Cancelled planning item",
      status: "cancelled",
    });
    await page.setViewportSize({ width, height: 950 });
    await page.goto("/tasks?view=board");
    await expect(
      page.getByRole("link", {
        name: state.tasks[0].title.trim(),
        exact: true,
      }),
    ).toBeVisible();
    const overflow = () =>
      page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
    expect(await overflow()).toBe(true);
    await page.screenshot({
      path: info.outputPath(`board-${width}.png`),
      fullPage: true,
    });
    state.conflict = true;
    await page
      .getByRole("button", {
        name: `Move ${state.tasks[0].title.trim()}`,
        exact: true,
      })
      .click();
    await page
      .getByRole("menuitem", { name: "Move to Ready", exact: true })
      .click();
    await expect(page.locator('p[role="status"]')).toContainText(
      "could not be saved",
    );
    await page.screenshot({
      path: info.outputPath(`failed-move-${width}.png`),
      fullPage: true,
    });
    await page
      .getByRole("link", { name: state.tasks[0].title.trim(), exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(await overflow()).toBe(true);
    await page.screenshot({
      path: info.outputPath(`detail-${width}.png`),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Show in List", exact: true })
      .click();
    await page.keyboard.press("Escape");
    await page.screenshot({
      path: info.outputPath(`list-${width}.png`),
      fullPage: true,
    });
    expect(await overflow()).toBe(true);
    await page.goto("/tasks?view=board&status=cancelled");
    await expect(
      page.getByRole("region", { name: "Cancelled", exact: true }),
    ).toBeVisible();
    await expect(page.locator('[data-slot="kanban-column"]')).toHaveCount(1);
    await page.screenshot({
      path: info.outputPath(`cancelled-${width}.png`),
      fullPage: true,
    });
    expect(errors).toEqual([]);
  });

test("touch scrolling a card does not start a drag", async ({
  page,
  context,
}) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 390, height: 950 });
  await page.goto("/tasks?view=board");
  const card = page.locator('[data-slot="kanban-item"]').first();
  await expect(card).toBeVisible();
  const box = (await card.boundingBox())!;
  const cdp = await context.newCDPSession(page);
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [{ x: box.x + 180, y: box.y + 120 }],
  });
  for (let i = 1; i <= 8; i++)
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchMove",
      touchPoints: [{ x: box.x + 180 - i * 18, y: box.y + 120 }],
    });
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchEnd",
    touchPoints: [],
  });
  await expect
    .poll(() =>
      page
        .getByLabel("Task board", { exact: true })
        .evaluate((e) => e.scrollLeft),
    )
    .toBeGreaterThan(0);
  expect(state.posts).toHaveLength(0);
  await expect(page.locator('[data-slot="kanban"]')).toHaveAttribute(
    "data-dragging",
    "false",
  );
});

test("touch handle moves once and a refreshed revision cancels an older gesture", async ({
  page,
  context,
}) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 390, height: 950 });
  await page.goto("/tasks?view=board");
  const handle = page.getByRole("button", {
    name: "Drag Plan the console",
    exact: true,
  });
  await expect(handle).toBeEnabled();
  const start = (await handle.boundingBox())!;
  const header = (await page
    .getByRole("region", { name: "Ready", exact: true })
    .getByRole("heading")
    .boundingBox())!;
  const cdp = await context.newCDPSession(page);
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [{ x: start.x + 10, y: start.y + 10 }],
  });
  await expect(page.locator('[data-slot="kanban"]')).toHaveAttribute(
    "data-dragging",
    "true",
  );
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchMove",
    touchPoints: [{ x: header.x + 10, y: header.y + 10 }],
  });
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Ready.",
  );
  await cdp.send("Input.dispatchTouchEvent", {
    type: "touchEnd",
    touchPoints: [],
  });
  await expect.poll(() => state.posts.length).toBe(1);
  expect(state.posts[0].body).toMatchObject({
    status: "ready",
    placement: "end",
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(handle).toBeEnabled();
  await handle.focus();
  await page.keyboard.press("Space");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Plan the console.",
  );
  state.board++;
  await page.evaluate(() => {
    window.dispatchEvent(new Event("offline"));
    window.dispatchEvent(new Event("online"));
  });
  await expect(page.locator('[data-slot="kanban"]')).toHaveAttribute(
    "data-dragging",
    "false",
  );
  await page.keyboard.press("Escape");
  expect(state.posts).toHaveLength(1);
});

for (const explicit of [false, true])
  test(`reopened saved launcher refreshes Task inputs and ${explicit ? "keeps actual Agent override" : "updates its Agent default"}`, async ({
    page,
  }) => {
    const state = await fixture(page);
    state.tasks[0].repository_id = null;
    state.agents.push(
      { ...state.agents[0], id: B, name: "New preference" },
      { ...state.agents[0], id: T, name: "Actual override" },
    );
    await page.goto(`/tasks?taskId=${T}`);
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("link", { name: "Start Run", exact: true }).click();
    await expect(
      dialog.getByRole("heading", { name: "Saved Task", exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "Start Run", exact: true }),
    ).toBeDisabled();
    if (explicit) {
      await dialog
        .getByRole("combobox", { name: "Agent", exact: true })
        .click();
      await page
        .getByRole("option", { name: "Actual override · fake", exact: true })
        .click();
    }
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await page.getByRole("link", { name: "Tasks", exact: true }).click();
    await page
      .getByRole("link", { name: "Plan the console", exact: true })
      .click();
    await dialog
      .getByLabel("Task title", { exact: true })
      .fill("Updated instructions");
    await dialog
      .getByLabel("Description", { exact: true })
      .fill("Execute this new plan");
    await dialog
      .getByRole("combobox", { name: "Repository", exact: true })
      .click();
    await page.getByRole("option", { name: "Source", exact: true }).click();
    await dialog
      .getByRole("combobox", { name: "Preferred Agent", exact: true })
      .click();
    await page
      .getByRole("option", { name: "New preference", exact: true })
      .click();
    await dialog
      .getByRole("button", { name: "Save changes", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "Save changes", exact: true }),
    ).toBeDisabled();
    await dialog.getByRole("link", { name: "Start Run", exact: true }).click();
    await expect(
      dialog.getByRole("heading", { name: "Saved Task", exact: true }),
    ).toBeVisible();
    await expect(dialog.getByLabel("Task title", { exact: true })).toHaveValue(
      "Updated instructions",
    );
    await expect(dialog.getByLabel("Description", { exact: true })).toHaveValue(
      "Execute this new plan",
    );
    await expect(
      dialog.getByRole("combobox", { name: "Repository", exact: true }),
    ).toHaveText("Source");
    await expect(
      dialog.getByRole("combobox", { name: "Agent", exact: true }),
    ).toHaveText(explicit ? "Actual override · fake" : "New preference · fake");
    await expect(
      dialog.getByRole("button", { name: "Start Run", exact: true }),
    ).toBeEnabled();
    expect(state.posts.filter((p) => p.path === "/runs")).toHaveLength(0);
  });

test("loaded tails refresh Run summaries without changing board revision", async ({
  page,
}) => {
  const state = await fixture(page);
  for (let i = 1; i <= 51; i++)
    state.tasks.push({
      ...state.tasks[0],
      id: `tail-${i}`,
      title: `Task ${i}`,
    });
  await page.clock.install();
  await page.goto("/tasks?view=board");
  await page
    .getByRole("button", { name: "Load more Backlog Tasks", exact: true })
    .click();
  const tail = page.locator('[data-slot="kanban-item"][data-value="tail-51"]');
  await expect(tail).toContainText("No Runs yet");
  state.active = true;
  await page.clock.fastForward(16000);
  await expect(tail).toContainText("1 active Run");
  state.active = false;
  await page.clock.fastForward(16000);
  await expect(tail).not.toContainText("1 active Run");
  expect(state.board).toBe(2);
  await expect(page.locator('[data-slot="kanban-item"]')).toHaveCount(52);
});

test("detail explains movement blocked by a failed collection refresh", async ({
  page,
}) => {
  await fixture(page);
  await page.route("**/projects/*/task-planning*", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ detail: "Planning unavailable" }),
    }),
  );
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "Workflow", exact: true }).click();
  await page.getByRole("option", { name: "Ready", exact: true }).click();
  await expect(
    dialog.getByRole("button", { name: "Move to Ready", exact: true }),
  ).toBeDisabled();
  await expect(
    dialog.getByText("Task order could not refresh.", { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Retry Task order", exact: true }),
  ).toBeVisible();
});

test("shared detail saves and clears partial schedules", async ({ page }) => {
  const state = await fixture(page);
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  const start = dialog.getByLabel("Planned start", { exact: true });
  const finish = dialog.getByLabel("Planned finish", { exact: true });
  const save = dialog.getByRole("button", {
    name: "Save changes",
    exact: true,
  });
  await expect(save).toBeDisabled();
  await start.fill("2026-10-04");
  await save.click();
  await expect(save).toBeDisabled();
  expect(state.posts.at(-1)?.body).toEqual({
    expected_revision: 1,
    expected_board_revision: 2,
    planned_start_date: "2026-10-04",
  });
  await finish.fill("2026-10-03");
  await expect(dialog.getByText(/finish must be on or after/i)).toBeVisible();
  await expect(save).toBeDisabled();
  await finish.fill("2026-10-04");
  await save.click();
  await expect(save).toBeDisabled();
  await page.reload();
  await expect(start).toHaveValue("2026-10-04");
  await expect(finish).toHaveValue("2026-10-04");
  await page.screenshot({
    path: test.info().outputPath("planned-dates-desktop.png"),
  });
  await dialog
    .getByRole("button", { name: "Clear schedule", exact: true })
    .click();
  await expect(start).toHaveValue("");
  expect(state.posts).toHaveLength(2);
  await save.click();
  await expect(save).toBeDisabled();
  expect(state.posts.at(-1)?.body).toEqual({
    expected_revision: 3,
    expected_board_revision: 4,
    planned_start_date: null,
    planned_finish_date: null,
  });
  expect(state.posts.filter((p) => p.path === "/runs")).toHaveLength(0);
});

test("schedule drafts survive view switches and conflicts", async ({
  page,
}) => {
  const state = await fixture(page);
  state.tasks[0].planned_start_date = "2026-10-04";
  state.tasks[0].planned_finish_date = "2026-10-06";
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  const start = dialog.getByLabel("Planned start", { exact: true });
  await start.fill("2026-10-05");
  await dialog
    .getByRole("button", { name: "Show in Kanban", exact: true })
    .click();
  await expect(start).toHaveValue("2026-10-05");
  await expect(page.locator("#project")).toBeDisabled();
  state.conflict = true;
  await dialog
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(dialog.getByText(/changed elsewhere/)).toBeVisible();
  await expect(start).toHaveValue("2026-10-05");
  await expect(
    dialog.getByRole("button", { name: "Save changes", exact: true }),
  ).toBeDisabled();
  state.conflict = false;
  state.tasks[0].planned_start_date = "2026-10-03";
  state.tasks[0].revision++;
  state.board++;
  await dialog
    .getByRole("button", { name: "Reload latest", exact: true })
    .click();
  await expect(start).toHaveValue("2026-10-03");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(
    page.getByText("Planned 2026-10-03 – 2026-10-06", { exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "List", exact: true }).click();
  await expect(
    page.getByText("Planned 2026-10-03 – 2026-10-06", { exact: true }),
  ).toBeVisible();
});

test("schedule edits remain available with source or active Run locks", async ({
  page,
}) => {
  const state = await fixture(page);
  state.tasks[0].external_refs = { linear: { issue_id: "source" } };
  state.active = true;
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/tasks?taskId=${T}`);
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByLabel("Task title", { exact: true }),
  ).toHaveAttribute("readonly", "");
  await dialog.getByLabel("Planned finish", { exact: true }).fill("2026-10-06");
  await dialog
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(
    dialog.getByRole("button", { name: "Save changes", exact: true }),
  ).toBeDisabled();
  expect(state.posts.at(-1)?.body).toEqual({
    expected_revision: 1,
    expected_board_revision: 2,
    planned_finish_date: "2026-10-06",
  });
  await expect(
    dialog.getByLabel("Planned finish", { exact: true }),
  ).toHaveValue("2026-10-06");
  expect(
    await dialog.evaluate((node) => node.scrollWidth <= node.clientWidth),
  ).toBe(true);
  await page.screenshot({
    path: test.info().outputPath("planned-dates-mobile.png"),
  });
});
