import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const base = `http://127.0.0.1:${compose ? "8000" : "18000"}/api/v1`;

test("save, edit, reload and launch the same Task through the real API", async ({
  page,
  request,
}, info) => {
  const source = mkdtempSync(
    join(
      compose ? process.env.CIRCULAR_EXECUTION_HOST_ROOT! : tmpdir(),
      "circular-planning-source-",
    ),
  );
  const cloneUrl = compose
    ? `/var/lib/circular/${basename(source)}/fixture.bundle`
    : source;
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=main");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.test");
  writeFileSync(join(source, "README.md"), "fixture\n");
  git("add", ".");
  git("commit", "-m", "initial");
  // A bundle is readable across host/container UIDs without relaxing Git ownership checks.
  if (compose) git("bundle", "create", "fixture.bundle", "--all");
  const post = async (path: string, data: object) => {
    const r = await request.post(`${base}/${path}`, { data });
    expect(r.status()).toBe(201);
    return r.json();
  };
  try {
    const project = await post("projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}planning`,
    });
    await post("repositories", {
      project_id: project.id,
      name: "Source",
      clone_url: cloneUrl,
    });
    await post("agents", {
      project_id: project.id,
      name: "Engineer",
      backend: "fake",
      backend_config: { delay_ms: 2000 },
    });
    await page.goto("/tasks");
    await page.getByRole("combobox", { name: "Project", exact: true }).click();
    await page.getByRole("option", { name: project.name, exact: true }).click();
    await page.getByRole("button", { name: "New Task", exact: true }).click();
    let dialog = page.getByRole("dialog");
    await dialog
      .getByLabel("Task title", { exact: true })
      .fill("Plan then execute");
    await dialog
      .getByRole("button", { name: "Save Task", exact: true })
      .click();
    await expect(page).toHaveURL(/taskId=/);
    const id = new URL(page.url()).searchParams.get("taskId")!;
    expect(
      await (await request.get(`${base}/runs?task_id=${id}`)).json(),
    ).toEqual([]);
    dialog = page.getByRole("dialog");
    await dialog
      .getByLabel("Description", { exact: true })
      .fill("Keep a durable plan.");
    await dialog
      .getByRole("combobox", { name: "Priority", exact: true })
      .click();
    await page.getByRole("option", { name: "High", exact: true }).click();
    await dialog
      .getByLabel("Planned start", { exact: true })
      .fill("2099-01-01");
    await dialog
      .getByLabel("Planned finish", { exact: true })
      .fill("2099-01-03");
    await dialog
      .getByRole("button", { name: "Save changes", exact: true })
      .click();
    await expect(
      dialog.getByRole("button", { name: "Save changes", exact: true }),
    ).toBeDisabled();
    await page.reload();
    await expect(dialog.getByLabel("Description", { exact: true })).toHaveValue(
      "Keep a durable plan.",
    );
    await expect(
      dialog.getByLabel("Planned start", { exact: true }),
    ).toHaveValue("2099-01-01");
    await expect(
      dialog.getByLabel("Planned finish", { exact: true }),
    ).toHaveValue("2099-01-03");
    await dialog.getByRole("link", { name: "Start Run", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`\\/\\?taskId=${id}`));
    await expect(
      dialog.getByRole("heading", { name: "Saved Task", exact: true }),
    ).toBeVisible();
    await dialog
      .getByRole("button", { name: "Start Run", exact: true })
      .click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
    const runId = page.url().split("/").at(-1)!;
    const run = await (await request.get(`${base}/runs/${runId}`)).json();
    expect(run.task_id).toBe(id);
    await page.goto(`/tasks?taskId=${id}`);
    await expect(
      dialog.getByLabel("Task title", { exact: true }),
    ).toHaveAttribute("readonly", "");
    const task = await (await request.get(`${base}/tasks/${id}`)).json();
    const rejected = await request.patch(`${base}/tasks/${id}`, {
      data: {
        expected_revision: task.revision,
        description: "Unsafe active edit",
      },
    });
    expect(rejected.status()).toBe(409);
    await expect
      .poll(
        async () =>
          (await (await request.get(`${base}/runs/${runId}`)).json()).status,
        { timeout: 40000 },
      )
      .toBe("succeeded");
    const saved = await (await request.get(`${base}/tasks/${id}`)).json();
    expect(saved).toMatchObject({
      id,
      status: "in_progress",
      priority: "high",
      description: "Keep a durable plan.",
      planned_start_date: "2099-01-01",
      planned_finish_date: "2099-01-03",
    });
    expect(
      await (
        await request.get(`${base}/tasks?project_id=${project.id}`)
      ).json(),
    ).toHaveLength(1);
    await page.screenshot({
      path: info.outputPath("saved-task-details.png"),
      fullPage: true,
    });
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});

test("source-managed Task content stays readonly while planning metadata is editable", async ({
  page,
  request,
}) => {
  const p = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}source-planning` },
  });
  expect(p.status()).toBe(201);
  const project = await p.json();
  const created = await request.post(`${base}/tasks`, {
    data: {
      project_id: project.id,
      title: "Imported source",
      description: "Source instructions",
      external_refs: {
        linear: {
          issue_id: "planning-source",
          url: "https://linear.app/example",
        },
      },
    },
  });
  expect(created.status()).toBe(201);
  const task = await created.json();
  await page.goto(`/tasks?taskId=${task.id}`);
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByLabel("Task title", { exact: true }),
  ).toHaveAttribute("readonly", "");
  await dialog.getByRole("combobox", { name: "Priority", exact: true }).click();
  await page.getByRole("option", { name: "Urgent", exact: true }).click();
  await dialog.getByLabel("Planned start", { exact: true }).fill("2026-10-04");
  await dialog
    .getByRole("button", { name: "Save changes", exact: true })
    .click();
  await expect
    .poll(
      async () =>
        (await (await request.get(`${base}/tasks/${task.id}`)).json()).priority,
    )
    .toBe("urgent");
  expect(
    await (await request.get(`${base}/tasks/${task.id}`)).json(),
  ).toMatchObject({
    title: "Imported source",
    description: "Source instructions",
    planned_start_date: "2026-10-04",
    planned_finish_date: null,
  });
  expect(
    (
      await request.patch(`${base}/tasks/${task.id}`, {
        data: { expected_revision: 2, title: "Overwrite" },
      })
    ).status(),
  ).toBe(409);
});

test("filtered Kanban anchors preserve hidden neighbors and reject racing revisions", async ({
  page,
  request,
}) => {
  const created = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}board-order` },
  });
  expect(created.status()).toBe(201);
  const project = await created.json();
  const tasks = [];
  for (const title of [
    "Visible A",
    "Hidden neighbor",
    "Visible B",
    "Visible X",
  ]) {
    const response = await request.post(`${base}/tasks`, {
      data: { project_id: project.id, title },
    });
    expect(response.status()).toBe(201);
    tasks.push(await response.json());
  }
  const planning = async () =>
    await (
      await request.get(`${base}/projects/${project.id}/task-planning`)
    ).json();
  const initial = await planning();
  const setup = await request.post(`${base}/tasks/${tasks[3].id}/move`, {
    data: {
      expected_revision: 1,
      expected_board_revision: initial.board_revision,
      status: "ready",
      placement: "end",
    },
  });
  expect(setup.status()).toBe(200);
  await page.goto(`/tasks?view=board&q=Visible&taskId=${tasks[3].id}`);
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  const handle = page.getByRole("button", {
    name: "Drag Visible X",
    exact: true,
  });
  await expect(handle).toBeEnabled();
  const target = (await page
    .locator(`[data-slot="kanban-item"][data-value="${tasks[2].id}"]`)
    .boundingBox())!;
  const start = (await handle.boundingBox())!;
  await page.mouse.move(start.x + start.width / 2, start.y + start.height / 2);
  await page.mouse.down();
  await page.mouse.move(
    start.x + start.width / 2 + 12,
    start.y + start.height / 2,
    { steps: 3 },
  );
  await expect(page.locator('[data-slot="kanban"]')).toHaveAttribute(
    "data-dragging",
    "true",
  );
  await page.mouse.move(target.x + 30, target.y + 20, { steps: 12 });
  await page.mouse.up();
  const order = async () =>
    (await planning()).groups
      .find((g: { status: string }) => g.status === "open")
      .items.map((i: { task: { id: string } }) => i.task.id);
  await expect
    .poll(order)
    .toEqual([tasks[0].id, tasks[1].id, tasks[3].id, tasks[2].id]);
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Backlog", exact: true })
      .getByRole("link"),
  ).toHaveText(["Visible A", "Visible X", "Visible B"]);
  const snapshot = await planning();
  const x = snapshot.groups
    .find((g: { status: string }) => g.status === "open")
    .items.find(
      (i: { task: { id: string } }) => i.task.id === tasks[3].id,
    ).task;
  const race = await Promise.all(
    ["start", "end"].map((placement) =>
      request.post(`${base}/tasks/${x.id}/move`, {
        data: {
          expected_revision: x.revision,
          expected_board_revision: snapshot.board_revision,
          status: "open",
          placement,
        },
      }),
    ),
  );
  expect(race.map((r) => r.status()).sort()).toEqual([200, 409]);
  const final = await order();
  expect([final[0], final.at(-1)]).toContain(x.id);
  expect(final.filter((id: string) => id !== x.id)).toEqual([
    tasks[0].id,
    tasks[1].id,
    tasks[2].id,
  ]);
  for (const task of tasks)
    expect(
      await (await request.get(`${base}/runs?task_id=${task.id}`)).json(),
    ).toEqual([]);
});
