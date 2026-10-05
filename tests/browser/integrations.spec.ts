import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const base = "http://127.0.0.1:18000/api/v1";
test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "OAuth providers belong to the disposable native test stack.",
);

test("connect providers, import resources, launch an existing Task, and reconnect after revocation", async ({
  page,
  request,
}, testInfo) => {
  const source = mkdtempSync(join(tmpdir(), "circular-integration-source-"));
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=main");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.test");
  writeFileSync(join(source, "README.md"), "Integration fixture\n");
  git("add", ".");
  git("commit", "-m", "initial");
  const post = async (path: string, data: object) => {
    const response = await request.post(`${base}/${path}`, { data });
    expect(response.status()).toBe(201);
    return response.json();
  };
  try {
    const project = await post("projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}integrations`,
    });
    const repository = await post("repositories", {
      project_id: project.id,
      name: "Local integration source",
      clone_url: source,
    });
    await post("agents", {
      project_id: project.id,
      name: "Integration test engineer",
      backend: "fake",
    });
    const initialConnections = await (
      await request.get(`${base}/projects/${project.id}/integrations`)
    ).json();
    const githubConfigured = initialConnections.find(
      (item: { provider: string }) => item.provider === "github",
    ).configured;
    await page.goto("/setup?section=integrations");
    await page.getByRole("combobox", { name: "Project", exact: true }).click();
    await page.getByRole("option", { name: project.name, exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Connect GitHub", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("button", { name: "Connect GitHub", exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    if (!githubConfigured) {
      await expect(
        page.getByText("Create your GitHub App", { exact: true }),
      ).toBeVisible();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390);
      await page.screenshot({
        path: testInfo.outputPath("integrations-github-setup.png"),
        fullPage: true,
      });
      await page
        .getByRole("button", { name: "Continue on GitHub", exact: true })
        .click();
      await page
        .getByRole("link", { name: "Create GitHub App", exact: true })
        .click();
      await page
        .getByRole("link", {
          name: "Install selected repositories",
          exact: true,
        })
        .click();
      await expect(page).toHaveURL(/\/setup\?section=integrations/);
      await page
        .getByRole("button", { name: "Connect GitHub", exact: true })
        .click();
    }
    await expect(
      page.getByText("GitHub connected.", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("combobox", { name: "GitHub account", exact: true }),
    ).toContainText("circular-fixture");
    await expect(page.getByText("Private", { exact: true })).toBeVisible();
    await page
      .getByRole("button", { name: "Add fixture/private-source", exact: true })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Added fixture/private-source",
        exact: true,
      }),
    ).toBeDisabled();
    const repositories = await (
      await request.get(`${base}/repositories?project_id=${project.id}`)
    ).json();
    expect(repositories).toHaveLength(2);
    expect(
      repositories.find(
        (item: { name: string }) => item.name === "fixture/private-source",
      ),
    ).toMatchObject({
      clone_url: "https://github.com/fixture/private-source.git",
      external_refs: {
        github: { repository_id: "202", installation_id: "101" },
      },
    });
    await page
      .getByRole("button", { name: "Connect Linear", exact: true })
      .click();
    const linearConfigured = initialConnections.find(
      (item: { provider: string }) => item.provider === "linear",
    ).configured;
    if (!linearConfigured) {
      const registrationURL = new URL(
        (await page
          .getByRole("link", { name: "Create Linear app", exact: true })
          .getAttribute("href")) ?? "",
      );
      expect(registrationURL.pathname).toBe("/settings/api/applications/new");
      expect(registrationURL.searchParams.get("oauth.redirect_uris")).toBe(
        `${base}/integrations/linear/callback`,
      );
      expect(registrationURL.searchParams.get("distribution")).toBe("private");
      expect(registrationURL.searchParams.has("oauth.client_uri")).toBe(false);
      await page
        .getByLabel("Linear Client ID", { exact: true })
        .fill("fixture-linear");
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390);
      await page.screenshot({
        path: testInfo.outputPath("integrations-linear-setup.png"),
        fullPage: true,
      });
      await page
        .getByRole("button", { name: "Save and connect Linear", exact: true })
        .click();
    }
    await expect(
      page.getByText("Linear connected.", { exact: true }),
    ).toBeVisible();
    const manageAccess = page.getByRole("link", {
      name: "Manage GitHub access",
      exact: true,
    });
    await expect(manageAccess).toHaveAttribute(
      "href",
      "http://127.0.0.1:18001/settings/installations/101",
    );
    const renamed = await request.post(
      "http://127.0.0.1:18001/fixture/github/rename",
      {
        data: { slug: "renamed-circular-fixture" },
      },
    );
    expect(renamed.ok()).toBe(true);
    await page
      .getByLabel("GitHub connection", { exact: true })
      .getByText("Connection settings", { exact: true })
      .click();
    await page
      .getByRole("button", { name: "GitHub app settings", exact: true })
      .click();
    const appURL = page.getByLabel("GitHub app URL", { exact: true });
    await expect(appURL).toHaveValue(
      "http://127.0.0.1:18001/apps/circular-fixture",
    );
    await appURL.fill("http://127.0.0.1:18001/apps/another-fixture-app");
    await page
      .getByRole("button", { name: "Save GitHub app URL", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "the URL must belong to the GitHub App already registered in Circular",
    );
    await appURL.fill("http://127.0.0.1:18001/apps/renamed-circular-fixture");
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: testInfo.outputPath("integrations-github-app-settings.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Save GitHub app URL", exact: true })
      .click();
    await expect(
      page.getByText("GitHub app URL updated.", { exact: true }),
    ).toBeVisible();
    const connections = await (
      await request.get(`${base}/projects/${project.id}/integrations`)
    ).json();
    expect(connections).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          provider: "github",
          status: "connected",
          installation_url:
            "http://127.0.0.1:18001/apps/renamed-circular-fixture/installations/new",
        }),
        expect.objectContaining({ provider: "linear", status: "connected" }),
      ]),
    );
    await page.reload();
    await expect(manageAccess).toHaveAttribute(
      "href",
      "http://127.0.0.1:18001/settings/installations/101",
    );
    await page
      .getByLabel("GitHub connection", { exact: true })
      .getByText("Connection settings", { exact: true })
      .click();
    await page
      .getByRole("button", { name: "GitHub app settings", exact: true })
      .click();
    await expect(appURL).toHaveValue(
      "http://127.0.0.1:18001/apps/renamed-circular-fixture",
    );
    await page
      .getByRole("form", { name: "Update GitHub app URL" })
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Linear team", exact: true }),
    ).toContainText("Engineering");
    await page
      .getByRole("combobox", { name: "Linear project", exact: true })
      .click();
    await page
      .getByRole("option", { name: "Integration work", exact: true })
      .click();
    await page
      .getByRole("combobox", { name: "Circular repository", exact: true })
      .click();
    await page
      .getByRole("option", { name: "Local integration source", exact: true })
      .click();
    await page.screenshot({
      path: testInfo.outputPath("integrations-connected.png"),
      fullPage: true,
      animations: "disabled",
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: testInfo.outputPath("integrations-mobile.png"),
      fullPage: true,
      animations: "disabled",
    });
    await page
      .getByRole("button", { name: "Import TST-1", exact: true })
      .click();
    await expect(page).toHaveURL(/\/\?taskId=[0-9a-f-]+$/);
    const taskId = new URL(page.url()).searchParams.get("taskId")!;
    await expect(
      page.getByRole("heading", { name: "Imported Task", exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Task title", { exact: true })).toHaveValue(
      "Imported integration task",
    );
    await expect(
      page.getByRole("combobox", { name: "Repository", exact: true }),
    ).toContainText("Local integration source");
    await expect(
      page.getByRole("combobox", { name: "Repository", exact: true }),
    ).toBeDisabled();
    await expect(page.getByLabel("Description", { exact: true })).toHaveValue(
      "Verify a task imported from Linear.",
    );
    await page.reload();
    await page.getByRole("button", { name: "Start Run", exact: true }).click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
    await expect(page.locator(".heading-actions .status")).toHaveText(
      "succeeded",
    );
    await expect(page.locator(".details-column .status")).toHaveText(
      "released",
    );
    const runs = await (
      await request.get(`${base}/runs?project_id=${project.id}`)
    ).json();
    expect(runs).toHaveLength(1);
    expect(runs[0].task_id).toBe(taskId);
    const tasks = await (
      await request.get(`${base}/tasks?project_id=${project.id}`)
    ).json();
    expect(tasks).toHaveLength(1);
    expect(tasks[0]).toMatchObject({
      id: taskId,
      repository_id: repository.id,
      external_refs: { linear: { identifier: "TST-1" } },
    });
    await page.goto("/setup?section=integrations");
    await page
      .getByRole("button", { name: "Import TST-1", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`taskId=${taskId}$`));
    await expect(
      page.getByRole("combobox", { name: "Repository", exact: true }),
    ).toContainText("Local integration source");
    // Fixture revocation models a user removing authorization at the provider.
    await request.post("http://127.0.0.1:18001/fixture/revoke");
    await page.goto("/setup?section=integrations");
    await expect(
      page.getByRole("button", { name: "Reconnect Linear", exact: true }),
    ).toBeEnabled();
    await expect(
      page.getByText("Reconnect required", { exact: true }),
    ).toHaveCount(2);
    await page
      .getByRole("button", { name: "Reconnect Linear", exact: true })
      .click();
    await expect(
      page.getByText("Linear connected.", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Import TST-1", exact: true }),
    ).toBeEnabled();
    await page
      .getByLabel("Linear connection", { exact: true })
      .getByText("Connection settings", { exact: true })
      .click();
    await page
      .getByRole("button", { name: "Disconnect Linear", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Connect Linear", exact: true }),
    ).toBeEnabled();
    await expect(
      page.getByRole("button", { name: "Import TST-1", exact: true }),
    ).not.toBeVisible();
    const publicStatus = await (
      await request.get(`${base}/projects/${project.id}/integrations`)
    ).text();
    for (const secret of [
      "access_token",
      "refresh_token",
      "client_secret",
      "ghu_",
      "credentials",
    ])
      expect(publicStatus).not.toContain(secret);
    await page.goto(
      "http://127.0.0.1:18000/api/v1/integrations/linear/callback?state=wrong&code=wrong",
    );
    await expect(page.getByRole("alert")).toContainText(
      "connection could not be verified",
    );
    await expect(
      page.getByRole("button", { name: "Connect Linear", exact: true }),
    ).toBeEnabled();
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});

test("unconfigured providers explain setup and disable connection actions on mobile", async ({
  page,
  request,
}, testInfo) => {
  const response = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}unconfigured` },
  });
  expect(response.status()).toBe(201);
  const project = await response.json();
  await page.route(`**/api/v1/projects/${project.id}/integrations`, (route) =>
    route.fulfill({
      json: ["github", "linear"].map((provider) => ({
        provider,
        configured: false,
        setup_available: false,
        app_editable: false,
        app_client_id: "",
        app_slug: "",
        registration_url: "",
        status: "not_configured",
        account_name: "",
        account_url: "",
        installation_url: "",
        callback_url: `http://localhost:8000/api/v1/integrations/${provider}/callback`,
      })),
    }),
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/setup?section=integrations");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: project.name, exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Connect GitHub", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Connect Linear", exact: true }),
  ).toBeDisabled();
  await page
    .getByText("App registration details", { exact: true })
    .first()
    .click();
  await expect(
    page.getByText(
      "http://localhost:8000/api/v1/integrations/github/callback",
      { exact: true },
    ),
  ).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  const list = await page.getByRole("tablist").boundingBox();
  const last = await page
    .getByRole("tab", { name: "Integrations", exact: true })
    .boundingBox();
  const panel = await page.getByRole("tabpanel").boundingBox();
  expect(list).not.toBeNull();
  expect(last).not.toBeNull();
  expect(panel).not.toBeNull();
  expect(last!.y + last!.height).toBeLessThanOrEqual(list!.y + list!.height);
  expect(panel!.y).toBeGreaterThanOrEqual(list!.y + list!.height);
  await page.screenshot({
    path: testInfo.outputPath("integrations-unconfigured.png"),
    fullPage: true,
    animations: "disabled",
  });
});
