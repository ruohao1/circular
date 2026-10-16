import { test, expect, type Page } from "@playwright/test";
import { fixture, T } from "./task-fixture";

// Playwright 1.62 ClockDate drops new.target, breaking TZDate subclasses.
// Preserve Date.prototype and the fake clock while restoring native construction.
async function clock(page: Page, time: string) {
  await page.clock.install({ time: new Date(time) });
  await page.addInitScript(() => {
    globalThis.Date = new Proxy(Date, {
      construct(target, args, newTarget) {
        return Object.setPrototypeOf(
          Reflect.construct(target, args),
          newTarget.prototype,
        );
      },
    });
  });
}

async function timelineFixture(page: Page, count = 1) {
  const state = await fixture(page);
  state.tasks[0].planned_start_date = "2026-10-01";
  state.tasks[0].planned_finish_date = "2026-10-31";
  for (let i = 1; i < count; i++)
    state.tasks.push({
      ...state.tasks[0],
      id: `aaaaaaaa-aaaa-4aaa-8aaa-${String(i).padStart(12, "0")}`,
      title: `Task ${String(i).padStart(4, "0")}`,
      planned_start_date:
        i < count / 2 ? "2026-10-01" : i % 2 ? "2026-10-10" : null,
      planned_finish_date: i < count / 2 ? "2026-10-31" : null,
    });
  return state;
}
const timelineURL = "/tasks?view=timeline&date=2026-10-07";

for (const width of [390, 667, 1440]) {
  test(`compact toolbar keeps filters usable at ${width}px`, async ({
    page,
  }, info) => {
    await timelineFixture(page);
    const height = width === 667 ? 375 : 900;
    await page.setViewportSize({ width, height });
    await page.goto(timelineURL);
    await expect(page.locator('[data-slot="gantt-bar"]')).toBeVisible();
    const filters = page.getByRole("button", { name: /^Filters/ });
    if (width === 1440) {
      const controls = [
        page.getByRole("button", { name: "Active", exact: true }),
        page.getByRole("tablist", { name: "Task layouts" }),
        page.getByRole("textbox", { name: "Search Tasks", exact: true }),
        filters,
        page.getByRole("button", { name: "Show Task sidebar", exact: true }),
      ];
      const centers = await Promise.all(
        controls.map(async (control) => {
          const box = (await control.boundingBox())!;
          return box.y + box.height / 2;
        }),
      );
      expect(Math.max(...centers) - Math.min(...centers)).toBeLessThan(2);
      const breadcrumb = (await page
        .getByRole("navigation", { name: "Breadcrumb", exact: true })
        .boundingBox())!;
      expect(breadcrumb.y + breadcrumb.height).toBeLessThan(
        Math.min(...centers),
      );
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({ path: info.outputPath(`toolbar-${width}.png`) });
    await filters.focus();
    await page.keyboard.press("Enter");
    const panel = page.getByRole("dialog", {
      name: "Task filters",
      exact: true,
    });
    await expect(panel).toBeVisible();
    const panelBox = (await panel.boundingBox())!;
    expect(panelBox.y).toBeGreaterThanOrEqual(0);
    expect(panelBox.y + panelBox.height).toBeLessThanOrEqual(height);
    await panel
      .getByRole("combobox", { name: "Preferred Agent filter", exact: true })
      .scrollIntoViewIfNeeded();
    await expect(
      panel.getByRole("combobox", {
        name: "Preferred Agent filter",
        exact: true,
      }),
    ).toBeInViewport();
    await panel
      .getByRole("combobox", { name: "Priority filter", exact: true })
      .click();
    await page.getByRole("option", { name: "High", exact: true }).click();
    await expect(filters).toHaveAccessibleName("Filters (1)");
    await expect(page).toHaveURL(/priority=high/);
    await panel
      .getByRole("combobox", { name: "Workflow filter", exact: true })
      .click();
    await page.keyboard.press("Escape");
    await expect(panel).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(panel).not.toBeVisible();
    await expect(filters).toBeFocused();
    await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(0);
    await page.getByRole("tab", { name: "Kanban", exact: true }).click();
    await expect(filters).toHaveAccessibleName("Filters (1)");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
}

async function dragSchedule(
  page: Page,
  edge: "move" | "start" | "end",
  days: number,
  release = true,
) {
  const bar = page.locator('[data-slot="gantt-bar"]').first();
  await bar.scrollIntoViewIfNeeded();
  await bar.hover();
  const target = edge === "move" ? bar : bar.locator(`[data-edge="${edge}"]`);
  const box = await target.boundingBox();
  if (!box) throw new Error("Missing schedule target");
  const dayWidth = await page
    .locator("[data-gantt-axis]")
    .evaluate(
      (el) =>
        (el.getBoundingClientRect().width * 86400000) /
        (Number((el as HTMLElement).dataset.ganttRangeEnd) -
          Number((el as HTMLElement).dataset.ganttRangeStart)),
    );
  const x = box.x + box.width / 2,
    y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + days * dayWidth, y, { steps: 5 });
  if (release) await page.mouse.up();
}

for (const [scale, timezoneId] of [
  ["day", "America/Los_Angeles"],
  ["week", "Europe/Paris"],
  ["month", "Pacific/Kiritimati"],
]) {
  test(`Gantt move resize and Undo persist daily dates in ${scale} view`, async ({
    browser,
    baseURL,
  }) => {
    const context = await browser.newContext({
      baseURL,
      viewport: { width: 1600, height: 1000 },
      timezoneId,
    });
    const page = await context.newPage();
    const state = await timelineFixture(page);
    state.tasks[0].planned_start_date = "2026-10-05";
    state.tasks[0].planned_finish_date = "2026-10-08";
    await page.goto(timelineURL + `&scale=${scale}`);
    await dragSchedule(page, "move", 2);
    await expect(
      page.getByRole("button", { name: "Undo schedule change", exact: true }),
    ).toBeEnabled();
    expect(state.tasks[0]).toMatchObject({
      planned_start_date: "2026-10-07",
      planned_finish_date: "2026-10-10",
    });
    expect(state.posts[0].body).toEqual({
      expected_revision: 1,
      expected_board_revision: 2,
      planned_start_date: "2026-10-07",
      planned_finish_date: "2026-10-10",
    });
    if (scale === "day")
      await page.screenshot({
        path: "/tmp/circular-gantt-interactive-desktop.png",
        fullPage: true,
        animations: "disabled",
      });
    await page
      .getByRole("button", { name: "Undo schedule change", exact: true })
      .click();
    await expect(
      page.getByText("Schedule change undone.", { exact: true }),
    ).toBeVisible();
    expect(state.posts[1].body).toEqual({
      expected_revision: 2,
      expected_board_revision: 3,
      planned_start_date: "2026-10-05",
      planned_finish_date: "2026-10-08",
    });
    await expect(
      page.getByRole("tab", { name: "Gantt", exact: true }),
    ).toBeFocused();
    for (const [edge, days, start, finish] of [
      ["end", 2, "2026-10-05", "2026-10-10"],
      ["start", -2, "2026-10-03", "2026-10-08"],
    ] as const) {
      await dragSchedule(page, edge, days);
      await expect(
        page.getByRole("button", { name: "Undo schedule change", exact: true }),
      ).toBeEnabled();
      expect(state.tasks[0]).toMatchObject({
        planned_start_date: start,
        planned_finish_date: finish,
      });
      await page
        .getByRole("button", { name: "Undo schedule change", exact: true })
        .click();
      await expect(
        page.getByText("Schedule change undone.", { exact: true }),
      ).toBeVisible();
    }
    expect(state.posts).toHaveLength(6);
    expect(state.posts.every((p) => p.path === `/tasks/${T}`)).toBe(true);
    await page.reload();
    await expect(page.locator('[data-slot="gantt-bar"]')).toBeVisible();
    expect(state.tasks[0]).toMatchObject({
      planned_start_date: "2026-10-05",
      planned_finish_date: "2026-10-08",
    });
    await context.close();
  });
}

test("Gantt resize has a one-day minimum and a stationary handle does not save", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-10-12";
  state.tasks[0].planned_finish_date = "2026-10-15";
  await page.goto(timelineURL + "&scale=day");
  await page.locator('[data-slot="gantt-bar"]').hover();
  await page.locator('[data-edge="end"]').click();
  expect(state.posts).toHaveLength(0);
  await dragSchedule(page, "end", -5);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  expect(state.tasks[0]).toMatchObject({
    planned_start_date: "2026-10-12",
    planned_finish_date: "2026-10-12",
  });
  expect(state.posts).toHaveLength(1);
});

test("Gantt cancellation releases locks without writing", async ({ page }) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-10-05";
  state.tasks[0].planned_finish_date = "2026-10-08";
  await page.goto(timelineURL + "&scale=day");
  for (const reason of [
    "escape",
    "outside",
    "blur",
    "pointercancel",
    "geometry",
    "wheel",
  ] as const) {
    await dragSchedule(page, "move", 2, false);
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toBeDisabled();
    if (reason === "escape") await page.keyboard.press("Escape");
    if (reason === "outside") await page.mouse.move(100, 100);
    if (reason === "blur")
      await page.evaluate(() => window.dispatchEvent(new Event("blur")));
    if (reason === "pointercancel")
      await page.evaluate(() =>
        window.dispatchEvent(
          new PointerEvent("pointercancel", { pointerId: 1 }),
        ),
      );
    if (reason === "geometry")
      await page.setViewportSize({ width: 1500, height: 950 });
    if (reason === "wheel") await page.mouse.wheel(150, 0);
    await page.mouse.up();
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toBeEnabled();
    expect(state.posts, reason).toHaveLength(0);
    await expect(page.locator('[data-slot="gantt-drag-overlay"]')).toHaveCount(
      0,
    );
  }
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  expect(state.posts).toHaveLength(1);
});

test("Gantt save failures and pending writes keep canonical dates", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-10-05";
  state.tasks[0].planned_finish_date = "2026-10-08";
  state.writeError = 503;
  await page.goto(timelineURL + "&scale=day");
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByText(/Schedule change could not be saved/),
  ).toBeVisible();
  expect(state.tasks[0].planned_start_date).toBe("2026-10-05");
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveAttribute(
    "aria-label",
    /Oct 5 - Oct 8/,
  );
  state.writeError = 0;
  let release!: () => void;
  state.writeGate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByText("Saving schedule…", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("tab", { name: "Kanban", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeDisabled();
  await expect(page.locator('[data-slot="gantt-resize-handle"]')).toHaveCount(
    0,
  );
  expect(state.tasks[0].planned_start_date).toBe("2026-10-05");
  release();
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  expect(state.tasks[0].planned_start_date).toBe("2026-10-07");
  expect(state.posts).toHaveLength(2);
});

test("stale gestures and Undo never overwrite a concurrent schedule", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-10-05";
  state.tasks[0].planned_finish_date = "2026-10-08";
  await page.goto(timelineURL + "&scale=day");
  await dragSchedule(page, "move", 2, false);
  state.board++;
  state.tasks[0].revision++;
  state.tasks[0].planned_start_date = "2026-10-06";
  await page.mouse.up();
  await expect(
    page.getByText(/Schedule change could not be saved/),
  ).toBeVisible();
  expect(state.tasks[0].planned_start_date).toBe("2026-10-06");
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  const undoRevision = state.tasks[0].revision,
    undoBoard = state.board;
  state.board++;
  state.tasks[0].revision++;
  state.tasks[0].planned_start_date = "2026-10-09";
  await page.getByRole("button", { name: "Undo schedule change" }).click();
  await expect(page.getByText(/Undo could not be saved/)).toBeVisible();
  expect(state.posts[2].body).toMatchObject({
    expected_revision: undoRevision,
    expected_board_revision: undoBoard,
  });
  expect(state.tasks[0].planned_start_date).toBe("2026-10-09");
  expect(state.posts).toHaveLength(3);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toHaveCount(0);
});

test("clipped moves preserve full dates and Undo survives window changes", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-09-25";
  state.tasks[0].planned_finish_date = "2026-10-04";
  await page.goto(timelineURL + "&scale=day");
  await expect(page.locator('[data-edge="start"]')).toHaveCount(0);
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  expect(state.tasks[0]).toMatchObject({
    planned_start_date: "2026-09-27",
    planned_finish_date: "2026-10-06",
  });
  await expect(page.locator('[data-slot="gantt-bar"]')).toBeFocused();
  await page.getByRole("button", { name: "Next period", exact: true }).click();
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(0);
  await page.getByRole("button", { name: "Undo schedule change" }).click();
  await expect(
    page.getByText("Schedule change undone.", { exact: true }),
  ).toBeVisible();
  expect(state.tasks[0]).toMatchObject({
    planned_start_date: "2026-09-25",
    planned_finish_date: "2026-10-04",
  });
  expect(state.posts).toHaveLength(2);
});

test("keyboard scheduling edits a draft and blocks gestures until saved", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_start_date = "2026-10-05";
  state.tasks[0].planned_finish_date = "2026-10-08";
  state.active = true;
  state.tasks[0].external_refs = { linear: { id: "imported" } };
  await page.goto(timelineURL + "&scale=day");
  await page.locator('[data-slot="gantt-bar"]').focus();
  await page.keyboard.press("Enter");
  const detail = page.getByRole("dialog");
  await detail
    .getByRole("button", { name: "Later by one day", exact: true })
    .click();
  await expect(detail.getByLabel("Planned start", { exact: true })).toHaveValue(
    "2026-10-06",
  );
  await expect(
    detail.getByLabel("Planned finish", { exact: true }),
  ).toHaveValue("2026-10-09");
  expect(state.posts).toHaveLength(0);
  await expect(page.locator('[data-slot="gantt-resize-handle"]')).toHaveCount(
    0,
  );
  await detail
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(
    detail.getByRole("button", { name: "Save changes", exact: true }),
  ).toBeDisabled();
  await detail.getByRole("button", { name: "Close", exact: true }).click();
  await dragSchedule(page, "move", 2);
  await expect(
    page.getByRole("button", { name: "Undo schedule change" }),
  ).toBeEnabled();
  expect(state.posts).toHaveLength(2);
  expect(state.tasks[0]).toMatchObject({
    planned_start_date: "2026-10-08",
    planned_finish_date: "2026-10-11",
  });
});

test("empty Gantt keeps its calendar visible with shared view controls", async ({
  page,
}, info) => {
  const state = await fixture(page);
  await page.setViewportSize({ width: 2560, height: 1440 });
  await page.goto(timelineURL);
  await expect(
    page.getByRole("tab", { name: "Gantt", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page.locator('[data-slot="gantt"]')).toBeVisible();
  await expect(
    page.locator('[data-slot="gantt-timeline-header"]'),
  ).toBeVisible();
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(0);
  const main = (await page.getByRole("main").boundingBox())!;
  const chart = (await page.locator('[data-slot="gantt"]').boundingBox())!;
  expect(chart.width).toBeGreaterThan(main.width * 0.95);
  expect(chart.height).toBeGreaterThan(1440 * 0.6);
  await page.screenshot({ path: info.outputPath("wide-empty-gantt.png") });
  await page.setViewportSize({ width: 1440, height: 900 });
  const resized = (await page.locator('[data-slot="gantt"]').boundingBox())!;
  expect(resized.height).toBeGreaterThan(900 * 0.6);
  expect(resized.height).toBeLessThan(chart.height);
  await expect(
    page.getByText("No scheduled Tasks match this window and your filters."),
  ).toBeVisible();
  await expect(page.getByTestId("unscheduled-task")).toHaveCount(1);
  await page.getByTestId("unscheduled-task").scrollIntoViewIfNeeded();
  await expect(
    page.getByRole("button", {
      name: "Schedule Plan the console",
      exact: true,
    }),
  ).toBeInViewport();
  await page.getByRole("tab", { name: "Kanban", exact: true }).click();
  await expect(
    page.getByText("Plan the console", { exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "List", exact: true }).click();
  await expect(
    page.getByText("Plan the console", { exact: true }),
  ).toBeVisible();
  expect(state.posts).toHaveLength(0);
});

test("Gantt bars retain shared detail and keyboard opening", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const state = await timelineFixture(page);
  await page.goto(timelineURL);
  await expect(
    page.getByRole("tab", { name: "Gantt", exact: true }),
  ).toHaveAttribute("data-state", "active");
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(1);
  const bar = page.locator('[data-slot="gantt-bar"]');
  await bar.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("dialog").getByLabel("Planned finish", { exact: true }),
  ).toHaveValue("2026-10-31");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await bar.focus();
  await page.keyboard.press("Space");
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  expect(state.posts).toHaveLength(0);
  expect(errors).toEqual([]);
});

test("1000 Tasks stay bounded, independently pageable and refreshed at one revision", async ({
  page,
}) => {
  await clock(page, "2026-10-07T12:00:00Z");
  const state = await timelineFixture(page, 1000);
  await page.goto(timelineURL);
  const scheduled = page.getByRole("region", {
    name: "Scheduled Tasks",
    exact: true,
  });
  const unscheduled = page.getByRole("region", {
    name: "Unscheduled Tasks",
    exact: true,
  });
  await expect(
    scheduled.getByText("1–50 of 500 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    unscheduled.getByText("1–50 of 500 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(50);
  await expect(page.getByTestId("unscheduled-task")).toHaveCount(50);
  await page
    .getByRole("button", { name: "Next scheduled Tasks", exact: true })
    .click();
  await expect(
    scheduled.getByText("51–100 of 500 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    unscheduled.getByText("1–50 of 500 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Next unscheduled Tasks", exact: true })
    .click();
  await expect(
    unscheduled.getByText("51–100 of 500 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    scheduled.getByText("51–100 of 500 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  state.active = true;
  await page.clock.fastForward(15050);
  await expect(
    scheduled.getByText("1 active Run", { exact: true }),
  ).toHaveCount(50);
  await expect(
    unscheduled.getByText("1 active Run", { exact: true }),
  ).toHaveCount(50);
  state.board++;
  await page.clock.fastForward(15050);
  await expect(
    scheduled.getByText("1–50 of 500 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    unscheduled.getByText("1–50 of 500 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Tasks changed. Both sections returned to their first page.",
      { exact: true },
    ),
  ).toBeVisible();
  expect(
    state.urls.filter(
      (u) => u.includes("/task-planning") || /^\/tasks(?:\?|\/|$)/.test(u),
    ),
  ).toEqual([]);
  for (const url of state.urls.filter((u) => u.includes("/task-timeline")))
    expect(new URL(url, "http://fixture").searchParams.get("limit")).toBe("50");
});

test("schedule draft and selection survive List Board Timeline and hidden filters", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  await page.goto(`/tasks?taskId=${T}&date=2026-10-07&q=Plan`);
  const detail = page.getByRole("dialog");
  await detail.getByLabel("Planned start", { exact: true }).fill("2026-10-04");
  await detail
    .getByRole("button", { name: "Show in Kanban", exact: true })
    .click();
  await expect(detail.getByLabel("Planned start", { exact: true })).toHaveValue(
    "2026-10-04",
  );
  await detail
    .getByRole("button", { name: "Show in Gantt", exact: true })
    .click();
  await expect(page).toHaveURL(/view=timeline/);
  await expect(page).toHaveURL(/date=2026-10-01/);
  await expect(page).toHaveURL(/q=Plan/);
  await expect(detail.getByLabel("Planned start", { exact: true })).toHaveValue(
    "2026-10-04",
  );
  await expect(
    detail.getByText("Gantt bars reflect saved dates.", { exact: false }),
  ).toBeVisible();
  page.once("dialog", (dialog) => dialog.dismiss());
  await detail.getByRole("button", { name: "Close", exact: true }).click();
  await expect(detail).toBeVisible();
  await detail
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect(
    detail.getByRole("button", { name: "Save changes", exact: true }),
  ).toBeDisabled();
  expect(state.tasks[0].planned_start_date).toBe("2026-10-04");
  expect(state.posts).toHaveLength(1);
  await detail.getByRole("button", { name: "Close", exact: true }).click();
  await page.goto(`/tasks?view=timeline&date=2026-10-07&taskId=${T}&q=Hidden`);
  await expect(
    detail.getByText(
      "This Task is outside the current Gantt page, window or filters.",
      { exact: false },
    ),
  ).toBeVisible();
});

test("Unscheduled partial dates open the existing editor with date focus", async ({
  page,
}) => {
  const state = await timelineFixture(page);
  state.tasks[0].planned_finish_date = null;
  await page.goto(timelineURL);
  await expect(
    page.getByText("Unscheduled · Starts 2026-10-01", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Schedule Plan the console", exact: true })
    .click();
  await expect(
    page.getByRole("dialog").getByLabel("Planned start", { exact: true }),
  ).toBeFocused();
  expect(state.posts).toHaveLength(0);
});

test("old filter and window responses never replace the active scope", async ({
  page,
}) => {
  const state = await timelineFixture(page, 4);
  let release!: () => void;
  state.timelineGate = {
    q: "",
    wait: new Promise<void>((resolve) => {
      release = resolve;
    }),
  };
  await page.goto(timelineURL);
  await expect
    .poll(() => state.urls.filter((u) => u.includes("task-timeline")).length)
    .toBe(1);
  await page
    .getByRole("textbox", { name: "Search Tasks", exact: true })
    .fill("Task 0001");
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(1);
  release();
  await expect(
    page
      .locator('[data-slot="gantt-tree-pane"]')
      .getByRole("button", { name: "Task 0001", exact: true }),
  ).toBeVisible();
  await expect(
    page
      .locator('[data-slot="gantt-tree-pane"]')
      .getByRole("button", { name: "Plan the console", exact: true }),
  ).toHaveCount(0);
  state.timelineGate = {
    from: "2026-12-28",
    wait: new Promise<void>((resolve) => {
      release = resolve;
    }),
  };
  await page.getByRole("button", { name: "Next period", exact: true }).click();
  await expect
    .poll(() => state.urls.some((u) => u.includes("from=2026-12-28")))
    .toBe(true);
  await page.goBack();
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(1);
  release();
  await expect(page.getByTestId("timeline-window")).toHaveText(
    "2026-09-28 – 2027-01-03",
  );
});

test("errors retry and only the active collection polls", async ({ page }) => {
  await clock(page, "2026-10-07T12:00:00Z");
  const state = await timelineFixture(page);
  state.timelineError = 503;
  await page.goto(timelineURL);
  await expect(
    page.getByText("Timeline unavailable", { exact: false }),
  ).toBeVisible();
  await expect(page.getByTestId("task-timeline")).toHaveCount(0);
  await expect(
    page.getByText("No scheduled Tasks match this window and your filters."),
  ).toHaveCount(0);
  state.timelineError = 0;
  await page.getByRole("button", { name: "Retry Tasks", exact: true }).click();
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(1);
  await page.getByRole("tab", { name: "List", exact: true }).click();
  const timelineCount = state.urls.filter((u) =>
    u.includes("task-timeline"),
  ).length;
  await page.clock.fastForward(15050);
  expect(state.urls.filter((u) => u.includes("task-timeline"))).toHaveLength(
    timelineCount,
  );
  await page.getByRole("tab", { name: "Gantt", exact: true }).click();
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(1);
  const planningCount = state.urls.filter((u) =>
    u.includes("task-planning"),
  ).length;
  await page.clock.fastForward(15050);
  expect(state.urls.filter((u) => u.includes("task-planning"))).toHaveLength(
    planningCount,
  );
});

for (const [timezone, today, nextToday] of [
  ["America/Los_Angeles", "2026-10-06", "2026-10-07"],
  ["Europe/Paris", "2026-10-07", "2026-10-08"],
  ["Pacific/Kiritimati", "2026-10-07", "2026-10-08"],
]) {
  test(`local Today and highlight agree in ${timezone}; anchor survives midnight`, async ({
    browser,
    baseURL,
  }) => {
    const context = await browser.newContext({
      timezoneId: timezone,
      baseURL,
    });
    const page = await context.newPage();
    await clock(page, "2026-10-07T00:30:00Z");
    await timelineFixture(page);
    await page.goto("/tasks?view=timeline&scale=day");
    await expect(page).toHaveURL(new RegExp(`date=${today}`));
    await expect(
      page.locator('[data-slot="gantt-timeline-header"] [data-today="true"]'),
    ).toHaveText(new RegExp(`\\b${Number(today.slice(-2))}$`));
    await page.clock.fastForward(86400000);
    await expect(page).toHaveURL(new RegExp(`date=${today}`));
    await expect(
      page.locator('[data-slot="gantt-timeline-header"] [data-today="true"]'),
    ).toHaveText(new RegExp(`\\b${Number(nextToday.slice(-2))}$`));
    await page.reload();
    await expect(page).toHaveURL(new RegExp(`date=${today}`));
    await page.getByRole("button", { name: "Today", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`date=${nextToday}`));
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`date=${today}`));
    await context.close();
  });
}

test("extreme years, one-day and clipped schedules retain real dates", async ({
  page,
}) => {
  const state = await timelineFixture(page, 2);
  state.tasks[0].planned_start_date = "0001-01-01";
  state.tasks[0].planned_finish_date = "9999-12-31";
  state.tasks[1].planned_start_date = "2026-10-07";
  state.tasks[1].planned_finish_date = "2026-10-07";
  await page.goto(timelineURL + "&scale=day");
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(2);
  const oneDay = page
    .locator('[data-slot="gantt-bar"]')
    .filter({ hasText: "Task 0001" });
  const box = await oneDay.boundingBox();
  expect(box?.width).toBeGreaterThan(0);
  await oneDay.click();
  await expect(
    page.getByRole("dialog").getByLabel("Planned finish", { exact: true }),
  ).toHaveValue("2026-10-07");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await page.goto("/tasks?view=timeline&date=0001-01-01&scale=month");
  await expect(page.getByTestId("timeline-window")).toHaveText(
    "0001-01-01 – 0001-12-31",
  );
  await expect(
    page.getByRole("button", { name: "Previous period", exact: true }),
  ).toBeDisabled();
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(1);
  await page.goto("/tasks?view=timeline&date=9999-12-31");
  await expect(page.getByTestId("timeline-window")).toHaveText(
    "9999-09-27 – 9999-12-31",
  );
  await expect(
    page.getByRole("button", { name: "Next period", exact: true }),
  ).toBeDisabled();
  await expect(page.locator('[data-slot="gantt-bar"]')).toHaveCount(1);
  expect(state.posts).toHaveLength(0);
});

test("narrow touch layout scrolls without unintended edits and supports both themes", async ({
  browser,
  baseURL,
}) => {
  const context = await browser.newContext({
    baseURL,
    viewport: { width: 390, height: 844 },
    hasTouch: true,
  });
  const page = await context.newPage();
  const state = await timelineFixture(page, 4);
  state.tasks[0].title = "Long Task title ".repeat(20);
  await page.goto(timelineURL + "&scale=day");
  await expect(page.locator('[data-slot="gantt-row-group"]')).toHaveCount(2);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  const bar = page.locator('[data-slot="gantt-bar"]').first();
  await bar.dispatchEvent("pointerdown", {
    pointerType: "touch",
    pointerId: 1,
    clientX: 310,
    clientY: 450,
  });
  await bar.dispatchEvent("pointermove", {
    pointerType: "touch",
    pointerId: 1,
    clientX: 350,
    clientY: 450,
  });
  await bar.dispatchEvent("pointerup", {
    pointerType: "touch",
    pointerId: 1,
    clientX: 350,
    clientY: 450,
  });
  expect(state.posts).toHaveLength(0);
  const viewport = page.locator(
    '[data-slot="gantt-timeline-pane"] [data-gantt-native-scroll]',
  );
  await viewport.evaluate((el) => {
    el.scrollLeft = 0;
  });
  await expect.poll(() => viewport.evaluate((el) => el.scrollLeft)).toBe(0);
  await viewport.evaluate((el) => {
    el.scrollLeft = el.scrollWidth;
  });
  await expect
    .poll(() => viewport.evaluate((el) => el.scrollLeft))
    .toBeGreaterThan(0);
  await page.evaluate(() => document.documentElement.classList.remove("dark"));
  await page.screenshot({
    path: "/tmp/circular-timeline-mobile-light.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.evaluate(() => document.documentElement.classList.add("dark"));
  await page.screenshot({
    path: "/tmp/circular-timeline-mobile-dark.png",
    fullPage: true,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: "/tmp/circular-timeline-desktop.png",
    fullPage: true,
    animations: "disabled",
  });
  await context.close();
});

test("filter and window round trips reset both cursor histories and show planning labels", async ({
  page,
}) => {
  const state = await timelineFixture(page, 240);
  state.tasks[0].priority = "urgent";
  state.tasks[239].status = "completed";
  state.tasks[239].priority = "high";
  await page.goto(timelineURL);
  const scheduled = page.getByRole("region", {
    name: "Scheduled Tasks",
    exact: true,
  });
  const unscheduled = page.getByRole("region", {
    name: "Unscheduled Tasks",
    exact: true,
  });
  await expect(
    scheduled.getByText("Backlog · Urgent", { exact: true }),
  ).toBeVisible();
  await expect(
    unscheduled.getByText("Done · High", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Next scheduled Tasks", exact: true })
    .click();
  await expect(
    scheduled.getByText("51–100 of 120 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Next unscheduled Tasks", exact: true })
    .click();
  await expect(
    unscheduled.getByText("51–100 of 120 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Search Tasks", exact: true })
    .fill("Plan the console");
  await expect(
    scheduled.getByText("1–1 of 1 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Search Tasks", exact: true })
    .fill("");
  await expect(
    scheduled.getByText("1–50 of 120 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    unscheduled.getByText("1–50 of 120 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Next unscheduled Tasks", exact: true })
    .click();
  await expect(
    unscheduled.getByText("51–100 of 120 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Next period", exact: true }).click();
  await expect(
    unscheduled.getByText("1–50 of 120 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Previous period", exact: true })
    .click();
  await expect(
    unscheduled.getByText("1–50 of 120 unscheduled Tasks", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", {
      name: "Previous unscheduled Tasks",
      exact: true,
    }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Next scheduled Tasks", exact: true })
    .click();
  await expect(
    scheduled.getByText("51–100 of 120 scheduled Tasks", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Previous scheduled Tasks", exact: true })
    .click();
  await expect(
    scheduled.getByText("1–50 of 120 scheduled Tasks", { exact: true }),
  ).toBeVisible();
});

test("CDP touch swipe, hold-drag, resize and cancellation", async ({
  browser,
  baseURL,
}) => {
  const context = await browser.newContext({
    baseURL,
    hasTouch: true,
    viewport: { width: 1200, height: 900 },
  });
  const page = await context.newPage();
  const state = await fixture(page);
  state.tasks[0].planned_start_date = "2026-10-05";
  state.tasks[0].planned_finish_date = "2026-10-08";
  for (let i = 1; i < 12; i++)
    state.tasks.push({
      ...state.tasks[0],
      id: `aaaaaaaa-aaaa-4aaa-8aaa-${String(i).padStart(12, "0")}`,
      title: `Touch QA ${String(i).padStart(2, "0")}`,
    });
  await page.goto("/tasks?view=timeline&date=2026-10-07&scale=day");
  const bars = page.locator('[data-slot="gantt-bar"]');
  await expect(bars.first()).toBeVisible();
  const pane = page.locator(
    '[data-slot="gantt-timeline-pane"] [data-slot="scroll-area-viewport"]',
  );
  const cdp = await page.context().newCDPSession(page);
  const touch = async (type: string, x = 0, y = 0) =>
    cdp.send("Input.dispatchTouchEvent", {
      type,
      touchPoints:
        type === "touchEnd" || type === "touchCancel"
          ? []
          : [{ x, y, radiusX: 1, radiusY: 1, force: 1, id: 1 }],
    });
  const point = async (edge?: "start" | "end") => {
    const target = edge
      ? bars.first().locator(`[data-edge="${edge}"]`)
      : bars.first();
    await target.scrollIntoViewIfNeeded();
    const box = await target.boundingBox();
    if (!box) throw new Error("no bar");
    return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  };
  const dayWidth = await page
    .locator("[data-gantt-axis]")
    .evaluate(
      (el) =>
        (el.getBoundingClientRect().width * 86400000) /
        (Number((el as HTMLElement).dataset.ganttRangeEnd) -
          Number((el as HTMLElement).dataset.ganttRangeStart)),
    );
  const before = await pane.evaluate((el) => el.scrollTop);
  let p = await point();
  await touch("touchStart", p.x, p.y);
  for (let i = 1; i <= 8; i++) await touch("touchMove", p.x, p.y - i * 12);
  await touch("touchEnd");
  await expect
    .poll(() => pane.evaluate((el) => el.scrollTop))
    .toBeGreaterThan(before);
  expect(state.posts).toHaveLength(0);
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeEnabled();
  await page.reload();
  await expect(bars.first()).toBeVisible();
  p = await point();
  await touch("touchStart", p.x, p.y);
  await page.waitForTimeout(350);
  for (let i = 1; i <= 5; i++)
    await touch("touchMove", p.x + (2 * dayWidth * i) / 5, p.y);
  await touch("touchEnd");
  await expect(
    page.getByRole("button", { name: "Undo schedule change", exact: true }),
  ).toBeEnabled();
  expect(state.posts).toHaveLength(1);
  expect(state.tasks[0]).toMatchObject({
    planned_start_date: "2026-10-07",
    planned_finish_date: "2026-10-10",
  });
  p = await point("end");
  await touch("touchStart", p.x, p.y);
  await page.waitForTimeout(350);
  for (let i = 1; i <= 5; i++)
    await touch("touchMove", p.x + (2 * dayWidth * i) / 5, p.y);
  await touch("touchEnd");
  await expect.poll(() => state.posts.length).toBe(2);
  // A saved move reorders the timeline by start date; first() is now Touch QA 01.
  await expect
    .poll(() => state.tasks[1].planned_finish_date)
    .toBe("2026-10-10");
  p = await point();
  await touch("touchStart", p.x, p.y);
  await page.waitForTimeout(350);
  await touch("touchMove", p.x + 2 * dayWidth, p.y);
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeDisabled();
  await touch("touchCancel");
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toBeEnabled();
  expect(state.posts).toHaveLength(2);
  await page.screenshot({
    path: "/tmp/circular-gantt-interactive-touch.png",
    fullPage: true,
  });
  await context.close();
});
