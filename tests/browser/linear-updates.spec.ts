import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const base = "http://127.0.0.1:18000/api/v1";
const provider = "http://127.0.0.1:18001";
const issueURL =
  "https://linear.app/circular-fixture/issue/TST-1/imported-task";

test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Linear publishing uses only the disposable native provider fixture.",
);

async function create(request: APIRequestContext, path: string, data: object) {
  const response = await request.post(`${base}/${path}`, { data });
  expect(response.status(), await response.text()).toBe(201);
  return response.json();
}

async function selectProject(page: Page, name: string) {
  await page.goto("/setup?section=integrations");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name, exact: true }).click();
}

async function connectLinear(
  page: Page,
  request: APIRequestContext,
  project: { id: string; name: string },
) {
  const connections = await (
    await request.get(`${base}/projects/${project.id}/integrations`)
  ).json();
  if (
    !connections.find(
      (item: { provider: string }) => item.provider === "linear",
    ).configured
  ) {
    const registered = await request.post(
      `${base}/projects/${project.id}/integrations/linear/app`,
      { data: { client_id: "fixture-linear" } },
    );
    expect(registered.ok(), await registered.text()).toBe(true);
  }
  await selectProject(page, project.name);
  const authorization = page.waitForRequest(
    (request) => new URL(request.url()).pathname === "/oauth/authorize",
  );
  await page
    .getByRole("button", { name: "Connect Linear", exact: true })
    .click();
  const scopes = new URL((await authorization).url()).searchParams
    .get("scope")
    ?.split(",");
  expect(scopes).toEqual(expect.arrayContaining(["read", "comments:create"]));
  expect(scopes).not.toContain("write");
  await expect(
    page.getByText("Linear connected.", { exact: true }),
  ).toBeVisible();
}

test("publish imported run progress and results only after opting in", async ({
  page,
  request,
}, testInfo) => {
  const source = mkdtempSync(join(tmpdir(), "circular-linear-source-"));
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=main");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.test");
  writeFileSync(join(source, "README.md"), "Linear publishing fixture\n");
  git("add", ".");
  git("commit", "-m", "fixture");
  try {
    const project = await create(request, "projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}linear-publishing`,
    });
    await create(request, "repositories", {
      project_id: project.id,
      name: "Linear source",
      clone_url: source,
    });
    await create(request, "agents", {
      project_id: project.id,
      name: "Linear fixture engineer",
      backend: "fake",
      backend_config: { delay_ms: 3000 },
    });
    await connectLinear(page, request, project);
    const settings = page.getByRole("region", {
      name: "Linear run updates",
      exact: true,
    });
    const toggle = settings.getByRole("switch", {
      name: "Publish run updates",
      exact: true,
    });
    await expect(toggle).not.toBeChecked();
    await expect(toggle).toBeEnabled();
    await toggle.focus();
    await toggle.press("Space");
    await expect(toggle).toBeChecked();
    await page.reload();
    await expect(toggle).toBeChecked();
    const saved = await (
      await request.get(
        `${base}/projects/${project.id}/integrations/linear/run-updates`,
      )
    ).json();
    expect(saved).toMatchObject({ enabled: true, authorized: true });
    await page
      .getByRole("button", { name: "Import TST-1", exact: true })
      .click();
    await expect(page).toHaveURL(/taskId=[0-9a-f-]+$/);
    await page.getByRole("button", { name: "Start Run", exact: true }).click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
    const runId = page.url().split("/").at(-1)!;
    await expect
      .poll(async () => {
        const response = await request.get(
          `${provider}/fixture/linear/comments`,
        );
        const comments = await response.json();
        return comments.items.some(
          (comment: { body: string }) =>
            comment.body.includes(runId) &&
            comment.body.includes("Circular run started"),
        );
      })
      .toBe(true);
    await expect(page.locator(".heading-actions .status")).toHaveText(
      "succeeded",
    );
    const delivery = page.getByRole("region", {
      name: "Linear issue",
      exact: true,
    });
    await expect(
      delivery.getByRole("link", { name: "Open Linear issue", exact: true }),
    ).toHaveAttribute("href", issueURL);
    await expect
      .poll(
        async () =>
          (
            await (
              await request.get(`${base}/runs/${runId}/linear-delivery`)
            ).json()
          ).status,
      )
      .toBe("delivered");
    await expect(
      delivery.getByText("Published", { exact: true }),
    ).toBeVisible();
    const comments = await (
      await request.get(`${provider}/fixture/linear/comments`)
    ).json();
    const published = comments.items.filter((comment: { body: string }) =>
      comment.body.includes(runId),
    );
    expect(published).toHaveLength(2);
    expect(
      new Set(published.map((comment: { id: string }) => comment.id)).size,
    ).toBe(2);
    expect(
      published.some((comment: { body: string }) =>
        comment.body.includes("Circular run completed"),
      ),
    ).toBe(true);
    expect(
      published.some((comment: { body: string }) =>
        comment.body.includes("Fake container workload completed"),
      ),
    ).toBe(true);
    await page.screenshot({
      path: testInfo.outputPath("linear-delivered.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: testInfo.outputPath("linear-delivered-mobile.png"),
      fullPage: true,
    });

    // The execution remains successful while a separate notification needs
    // attention. Only this delivery response is substituted for UI edge cases.
    const deliveryState = {
      issue_url: issueURL,
      status: "pending",
      error: "",
      last_delivered_at: null,
    };
    const deliveryPath = `**/api/v1/runs/${runId}/linear-delivery`;
    let deliveryUnavailable = false;
    await page.route(deliveryPath, (route) =>
      deliveryUnavailable
        ? route.fulfill({
            status: 503,
            json: { detail: "Linear delivery is temporarily unavailable" },
          })
        : route.fulfill({ json: deliveryState }),
    );
    await page.reload();
    await expect(
      delivery.getByText("Waiting to publish", { exact: true }),
    ).toBeVisible();
    deliveryState.status = "retrying";
    deliveryState.error =
      "Linear is temporarily unavailable. Circular will try again.";
    await page.reload();
    await expect(delivery.getByText("Retrying", { exact: true })).toBeVisible();
    await expect(delivery.getByRole("alert")).toContainText(
      "Circular will try again",
    );
    deliveryState.status = "failed";
    await page.reload();
    await expect(
      delivery.getByText("Could not publish", { exact: true }),
    ).toBeVisible();
    await expect(
      delivery.getByRole("link", { name: "Linear settings", exact: true }),
    ).toBeVisible();
    await expect(page.locator(".heading-actions .status")).toHaveText(
      "succeeded",
    );
    await page.screenshot({
      path: testInfo.outputPath("linear-delivery-failed-mobile.png"),
      fullPage: true,
    });
    const otherProject = await create(request, "projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}different-linear-project`,
    });
    await selectProject(page, otherProject.name);
    await page.goto(`/runs/${runId}`);
    await delivery
      .getByRole("link", { name: "Linear settings", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toContainText(project.name);
    await expect(toggle).toBeChecked();
    await page.goto(`/runs/${runId}`);
    deliveryState.status = "reconnect_required";
    deliveryState.error = "Reconnect Linear to resume publishing.";
    await page.reload();
    await expect(
      delivery.getByText("Reconnect required", { exact: true }),
    ).toBeVisible();
    await expect(
      delivery.getByRole("link", { name: "Open Linear issue", exact: true }),
    ).toHaveAttribute("href", issueURL);
    deliveryUnavailable = true;
    await page.reload();
    await expect(delivery.getByRole("alert")).toContainText(
      "temporarily unavailable",
    );
    deliveryUnavailable = false;
    deliveryState.status = "disabled";
    deliveryState.error = "";
    await delivery
      .getByRole("button", { name: "Try again", exact: true })
      .click();
    await expect(
      delivery.getByText("Updates off", { exact: true }),
    ).toBeVisible();
    await page.unroute(deliveryPath);

    await page.goto("/setup?section=integrations");
    await toggle.click();
    await expect(toggle).not.toBeChecked();
    await page.reload();
    await expect(toggle).not.toBeChecked();
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});

test("explain missing comment permission and preserve the saved setting on errors", async ({
  page,
  request,
}, testInfo) => {
  const project = await create(request, "projects", {
    name: `${process.env.CIRCULAR_E2E_PREFIX}linear-update-settings`,
  });
  const readOnly = await request.post(`${provider}/fixture/linear/scopes`, {
    data: { scopes: "read" },
  });
  expect(readOnly.ok()).toBe(true);
  try {
    await connectLinear(page, request, project);
  } finally {
    const restore = await request.post(`${provider}/fixture/linear/scopes`, {
      data: { scopes: "read comments:create" },
    });
    expect(restore.ok()).toBe(true);
  }
  const settings = page.getByRole("region", {
    name: "Linear run updates",
    exact: true,
  });
  const toggle = settings.getByRole("switch", {
    name: "Publish run updates",
    exact: true,
  });
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toBeDisabled();
  await expect(
    settings.getByRole("button", {
      name: "Allow Linear comments",
      exact: true,
    }),
  ).toBeEnabled();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("linear-comment-permission-mobile.png"),
    fullPage: true,
  });

  await settings
    .getByRole("button", { name: "Allow Linear comments", exact: true })
    .click();
  await expect(toggle).toBeEnabled();
  await expect(toggle).not.toBeChecked();
  const upgraded = await (
    await request.get(
      `${base}/projects/${project.id}/integrations/linear/run-updates`,
    )
  ).json();
  expect(upgraded).toMatchObject({ enabled: false, authorized: true });
  await expect(
    settings.getByRole("button", {
      name: "Allow Linear comments",
      exact: true,
    }),
  ).toBeHidden();

  const path = `**/api/v1/projects/${project.id}/integrations/linear/run-updates`;
  const state = {
    enabled: false,
    authorized: true,
    pending_count: 0,
    failed_count: 0,
    last_error: "",
  };
  let unavailable = false;
  await page.route(path, async (route) => {
    if (unavailable || route.request().method() === "POST") {
      await route.fulfill({
        status: 503,
        json: { detail: "Linear update settings are temporarily unavailable" },
      });
      return;
    }
    await route.fulfill({ json: state });
  });
  await toggle.click();
  await expect(settings.getByRole("alert")).toContainText(
    "temporarily unavailable",
  );
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toBeEnabled();

  state.pending_count = 2;
  state.failed_count = 1;
  state.last_error = "Reconnect Linear to resume pending updates.";
  await page.reload();
  await expect(settings).toContainText("2 updates waiting");
  await expect(settings).toContainText("1 update needs attention");
  await expect(settings.getByRole("alert")).toContainText("Reconnect Linear");

  unavailable = true;
  await page.reload();
  await expect(settings.getByRole("alert")).toContainText(
    "temporarily unavailable",
  );
  await expect(toggle).toBeDisabled();
  unavailable = false;
  state.last_error = "";
  await settings
    .getByRole("button", { name: "Try again", exact: true })
    .click();
  await expect(toggle).toBeEnabled();
});
