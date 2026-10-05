import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const base = `http://127.0.0.1:${compose ? "8000" : "18000"}/api/v1`;

test("choose and persist model variants for new and supplied agents", async ({
  page,
  request,
}, testInfo) => {
  const response = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}model-settings` },
  });
  expect(response.status()).toBe(201);
  const project = await response.json();
  const readAgents = async () =>
    (await request.get(`${base}/agents?project_id=${project.id}`)).json();
  const supplied = await readAgents();
  const discovery = supplied.find(
    (item: { preset: string }) => item.preset === "repository-discovery",
  );
  expect(
    supplied.find((item: { preset: string }) => item.preset === "pr-reviewer"),
  ).toMatchObject({ backend: "codex", backend_config: {}, enabled: true });
  await page.goto("/setup?section=agents");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: project.name, exact: true }).click();
  const create = page.getByRole("form", { name: "Create agent", exact: true });
  await expect(
    create.getByRole("combobox", { name: "Model", exact: true }),
  ).toContainText("GPT-6-Astra");
  await expect(
    create.getByRole("combobox", { name: "Variant", exact: true }),
  ).toContainText("Low");
  await create.getByLabel("Agent name", { exact: true }).fill("Astra engineer");
  await create
    .getByRole("button", { name: "Create agent", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Astra engineer", exact: true }),
  ).toBeVisible();
  expect(
    (await readAgents()).find(
      (item: { name: string }) => item.name === "Astra engineer",
    ).backend_config,
  ).toEqual({ model: "gpt-6-astra", reasoning_effort: "low" });

  await create.getByLabel("Agent name", { exact: true }).fill("Sol engineer");
  await create.getByRole("combobox", { name: "Model", exact: true }).click();
  await page.getByRole("option", { name: "GPT-5.6-Sol", exact: true }).click();
  await create.getByRole("combobox", { name: "Variant", exact: true }).click();
  await page.getByRole("option", { name: "Extra high", exact: true }).click();
  await create
    .getByRole("button", { name: "Create agent", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Sol engineer", exact: true }),
  ).toBeVisible();
  expect(
    (await readAgents()).find(
      (item: { name: string }) => item.name === "Sol engineer",
    ).backend_config,
  ).toEqual({ model: "gpt-5.6-sol", reasoning_effort: "xhigh" });

  await page
    .getByRole("button", {
      name: "Edit model for Repository discovery",
      exact: true,
    })
    .click();
  const edit = page.getByRole("form", {
    name: "Edit model for Repository discovery",
    exact: true,
  });
  await edit.getByRole("combobox", { name: "Variant", exact: true }).click();
  await page.getByRole("option", { name: "Ultra", exact: true }).click();
  await edit.getByRole("combobox", { name: "Model", exact: true }).click();
  await page.getByRole("option", { name: "GPT-5.6-Luna", exact: true }).click();
  await expect(
    edit.getByRole("combobox", { name: "Variant", exact: true }),
  ).toContainText("Medium");
  await edit.getByRole("combobox", { name: "Variant", exact: true }).click();
  await expect(
    page.getByRole("option", { name: "Ultra", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("option", { name: "Max", exact: true }).click();
  await page.route(
    `**/api/v1/agents/${discovery.id}`,
    (route) =>
      route.fulfill({
        status: 503,
        json: { detail: "Model settings could not be saved. Try again." },
      }),
    { times: 1 },
  );
  await edit.getByRole("button", { name: "Save model", exact: true }).click();
  await expect(edit.getByRole("alert")).toContainText("could not be saved");
  await expect(
    edit.getByRole("combobox", { name: "Model", exact: true }),
  ).toContainText("GPT-5.6-Luna");
  await expect(
    edit.getByRole("combobox", { name: "Variant", exact: true }),
  ).toContainText("Max");
  await edit.getByRole("button", { name: "Save model", exact: true }).click();
  await expect(edit).not.toBeVisible();
  await page.reload();
  await page
    .getByRole("button", {
      name: "Edit model for Repository discovery",
      exact: true,
    })
    .click();
  await expect(
    edit.getByRole("combobox", { name: "Model", exact: true }),
  ).toContainText("GPT-5.6-Luna");
  await expect(
    edit.getByRole("combobox", { name: "Variant", exact: true }),
  ).toContainText("Max");
  const saved = (await readAgents()).find(
    (item: { id: string }) => item.id === discovery.id,
  );
  expect(saved).toMatchObject({
    instructions: discovery.instructions,
    preset: "repository-discovery",
    backend_config: { model: "gpt-5.6-luna", reasoning_effort: "max" },
  });
  expect(
    await (await request.get(`${base}/runs?project_id=${project.id}`)).json(),
  ).toHaveLength(0);
  await page.screenshot({
    path: testInfo.outputPath("model-settings-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("model-settings-mobile.png"),
    fullPage: true,
  });
});

test("supplied discovery agent prepares the selected repository without starting a Run", async ({
  page,
  request,
}, testInfo) => {
  const create = async (kind: string, data: object) => {
    const response = await request.post(`${base}/${kind}`, { data });
    expect(response.status()).toBe(201);
    return response.json();
  };
  const project = await create("projects", {
    name: `${process.env.CIRCULAR_E2E_PREFIX}discovery`,
  });
  const first = await create("repositories", {
    project_id: project.id,
    name: "First source",
    clone_url: "/unused/discovery-first",
  });
  await create("repositories", {
    project_id: project.id,
    name: "Second source",
    clone_url: "/unused/discovery-second",
  });
  await create("agents", {
    project_id: project.id,
    name: "Custom test engineer",
    backend: "fake",
  });
  const agents = await (
    await request.get(`${base}/agents?project_id=${project.id}`)
  ).json();
  const discovery = agents.find(
    (item: { preset: string | null }) => item.preset === "repository-discovery",
  );
  expect(discovery).toMatchObject({
    name: "Repository discovery",
    backend: "codex",
    enabled: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/setup?section=agents");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: project.name, exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Repository discovery", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Repository to explore", exact: true })
    .click();
  await page.getByRole("option", { name: "First source", exact: true }).click();
  await page.route(
    `**/api/v1/projects/${project.id}/discovery`,
    (route) =>
      route.fulfill({
        status: 503,
        json: { detail: "Could not prepare discovery. Try again." },
      }),
    { times: 1 },
  );
  await page
    .getByRole("button", { name: "Explore repository", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Could not prepare discovery",
  );
  await expect(
    page.getByRole("combobox", { name: "Repository to explore", exact: true }),
  ).toContainText("First source");
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("discovery-setup-mobile.png"),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Explore repository", exact: true })
    .click();
  await expect(page).toHaveURL(/\/?\?taskId=/);
  await expect(
    page.getByRole("heading", { name: "Repository discovery", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue(
    "Understand First source",
  );
  await expect(
    page.getByRole("combobox", { name: "Agent", exact: true }),
  ).toContainText("Repository discovery · codex");
  await expect(
    page.getByRole("combobox", { name: "Repository", exact: true }),
  ).toContainText("First source");
  await expect(
    page.getByRole("button", { name: "Start Run", exact: true }),
  ).toBeEnabled();
  const taskID = new URL(page.url()).searchParams.get("taskId");
  const task = await (await request.get(`${base}/tasks/${taskID}`)).json();
  expect(task).toMatchObject({
    project_id: project.id,
    repository_id: first.id,
    external_refs: {
      circular: { kind: "repository-discovery", agent_id: discovery.id },
    },
  });
  await page.reload();
  await expect(
    page.getByRole("combobox", { name: "Agent", exact: true }),
  ).toContainText("Repository discovery · codex");
  await expect(
    page.getByRole("button", { name: "Start Run", exact: true }),
  ).toBeEnabled();
  expect(
    await (await request.get(`${base}/runs?project_id=${project.id}`)).json(),
  ).toHaveLength(0);
  expect(
    await (await request.get(`${base}/tasks?project_id=${project.id}`)).json(),
  ).toHaveLength(1);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: testInfo.outputPath("discovery-launcher.png"),
    fullPage: true,
  });
});

test("empty launcher leads to setup with accessible mobile navigation", async ({
  page,
}) => {
  await page.route("**/api/v1/projects", (route) =>
    route.fulfill({ json: [] }),
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "No Projects yet", exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Open setup" }).click();
  await expect(
    page.getByRole("tab", { name: "Projects", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(
    page.getByRole("button", { name: "Create project", exact: true }),
  ).toBeDisabled();
  await page.getByRole("tab", { name: "Repositories", exact: true }).click();
  await expect(
    page.getByText("Choose a project first", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Add repository", exact: true }),
  ).toBeDisabled();
  await page.getByRole("tab", { name: "Agents", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Create agent", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("combobox", { name: "Backend", exact: true }),
  ).toBeDisabled();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page
    .getByRole("button", { name: "Open navigation", exact: true })
    .click();
  await page
    .getByRole("navigation", { name: "Workspace" })
    .getByRole("link", { name: "Runs", exact: true })
    .click();
  await expect(page.getByRole("link", { name: "Open setup" })).toBeVisible();
});

test("configure resources in setup, recover from errors, and launch the first Run", async ({
  page,
  request,
}, testInfo) => {
  const source = mkdtempSync(
    join(
      compose ? process.env.CIRCULAR_EXECUTION_HOST_ROOT! : tmpdir(),
      "circular-setup-source-",
    ),
  );
  const cloneUrl = compose
    ? `/var/lib/circular/${basename(source)}/fixture.bundle`
    : source;
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=trunk");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.test");
  writeFileSync(join(source, "README.md"), "Setup fixture\n");
  git("add", ".");
  git("commit", "-m", "initial");
  if (compose) git("bundle", "create", "fixture.bundle", "--all");
  const projectName = `${process.env.CIRCULAR_E2E_PREFIX}setup`;
  try {
    await page.goto("/");
    await page
      .getByRole("navigation", { name: "Workspace" })
      .getByRole("link", { name: "Setup", exact: true })
      .click();
    await page.getByLabel("Project name", { exact: true }).fill(projectName);
    await page
      .getByLabel("Description", { exact: false })
      .fill("Created entirely through the console.");
    await page
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText(
      "created and selected",
    );
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toContainText(projectName);
    await page
      .getByRole("button", { name: "Add a repository", exact: true })
      .click();
    await expect(
      page.getByText("No repositories yet", { exact: true }),
    ).toBeVisible();
    await page
      .getByLabel("Repository name", { exact: true })
      .fill("Setup repository");
    await page.getByLabel("Clone URL or path", { exact: true }).fill(cloneUrl);
    await page.getByLabel("Default branch", { exact: true }).fill("trunk");

    // A failed save must keep the draft and allow a real retry.
    await page.route(
      "**/api/v1/repositories",
      (route) =>
        route.fulfill({
          status: 503,
          json: { detail: "Repository service unavailable. Try again." },
        }),
      { times: 1 },
    );
    await page
      .getByRole("button", { name: "Add repository", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "Repository service unavailable",
    );
    await expect(
      page.getByLabel("Repository name", { exact: true }),
    ).toHaveValue("Setup repository");
    await expect(
      page.getByLabel("Clone URL or path", { exact: true }),
    ).toHaveValue(cloneUrl);
    await page
      .getByRole("button", { name: "Add repository", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText(
      "added to this project",
    );
    await expect(
      page.getByRole("heading", { name: "Setup repository", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "View agents", exact: true })
      .click();

    await page
      .getByLabel("Agent name", { exact: true })
      .fill("Setup Codex engineer");
    await expect(
      page.getByRole("combobox", { name: "Backend", exact: true }),
    ).toContainText("Codex");
    await page.getByRole("combobox", { name: "Model", exact: true }).click();
    await page
      .getByRole("option", { name: "Custom model…", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Variant", exact: true }),
    ).toContainText("Default for model");
    await page
      .getByLabel("Custom model ID", { exact: true })
      .fill("invalid model with spaces");
    await page
      .getByLabel("Instructions", { exact: false })
      .fill("Run checks before reporting completion.");
    await page
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "Codex model must be a valid model identifier",
    );
    await expect(page.getByLabel("Agent name", { exact: true })).toHaveValue(
      "Setup Codex engineer",
    );
    await page
      .getByLabel("Custom model ID", { exact: true })
      .fill("codex-test-model");
    await page
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Setup Codex engineer", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("codex-test-model · Default effort", { exact: true }),
    ).toBeVisible();

    // Registration does not contact Codex. Only the fake Agent is launched.
    await page
      .getByLabel("Agent name", { exact: true })
      .fill("Setup test engineer");
    await page.getByRole("combobox", { name: "Backend", exact: true }).click();
    await page
      .getByRole("option", { name: "Test (fake)", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Model", exact: true }),
    ).not.toBeVisible();
    await page
      .getByLabel("Instructions", { exact: false })
      .fill("Verify the setup flow.");
    await page
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Setup test engineer", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Ready to launch", exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("setup-agents.png"),
      fullPage: true,
      animations: "disabled",
    });

    const projects = await (await request.get(`${base}/projects`)).json();
    const project = projects.find(
      (item: { name: string }) => item.name === projectName,
    );
    const repositories = await (
      await request.get(`${base}/repositories?project_id=${project.id}`)
    ).json();
    expect(repositories).toHaveLength(1);
    expect(repositories[0]).toMatchObject({
      name: "Setup repository",
      clone_url: cloneUrl,
      default_branch: "trunk",
    });
    const agents = await (
      await request.get(`${base}/agents?project_id=${project.id}`)
    ).json();
    expect(agents).toHaveLength(4);
    expect(
      agents.filter(
        (item: { preset: string }) => item.preset === "pr-reviewer",
      ),
    ).toHaveLength(1);
    expect(
      agents.find(
        (item: { name: string }) => item.name === "Setup Codex engineer",
      ),
    ).toMatchObject({
      backend_config: { model: "codex-test-model" },
      instructions: "Run checks before reporting completion.",
    });

    await page
      .getByRole("link", { name: "Go to Runs", exact: true })
      .first()
      .click();
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toContainText(projectName);
    await page.getByRole("button", { name: "New Task", exact: true }).click();
    await expect(
      page.getByRole("combobox", { name: "Repository", exact: true }),
    ).toContainText("Setup repository");
    await expect(
      page.getByRole("combobox", { name: "Agent", exact: true }),
    ).toContainText("Setup test engineer · fake");
    await page
      .getByLabel("Task title", { exact: true })
      .fill("First Run from setup");
    await page.getByRole("button", { name: "Start Run", exact: true }).click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
    await expect(page.locator(".heading-actions .status")).toHaveText(
      "succeeded",
    );
    await expect(page.locator(".details-column .status")).toHaveText(
      "released",
    );

    // Another project starts with its own resources and no inherited form draft.
    await page
      .getByRole("navigation", { name: "Workspace" })
      .getByRole("link", { name: "Setup", exact: true })
      .click();
    await page
      .getByLabel("Project name", { exact: true })
      .fill(`${projectName}-empty`);
    await page
      .getByRole("button", { name: "Create project", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText(
      "created and selected",
    );
    await page.getByRole("tab", { name: "Agents", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Repository discovery", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Explore repository", exact: true }),
    ).toBeDisabled();
    await expect(page.getByLabel("Agent name", { exact: true })).toHaveValue(
      "",
    );
    await page
      .getByRole("navigation", { name: "Workspace" })
      .getByRole("link", { name: "Runs", exact: true })
      .click();
    await page.getByRole("button", { name: "New Task", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Add repository", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Start Run", exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("button", { name: "Add repository", exact: true })
      .click();
    await expect(
      page.getByRole("tab", { name: "Repositories", exact: true }),
    ).toHaveAttribute("aria-selected", "true");
    await page.getByRole("combobox", { name: "Project", exact: true }).click();
    await page.getByRole("option", { name: projectName, exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "Setup repository", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Project", exact: true }),
    ).toContainText(projectName);
    await expect(
      page.getByRole("tab", { name: "Repositories", exact: true }),
    ).toHaveAttribute("aria-selected", "true");

    await page.setViewportSize({ width: 390, height: 844 });
    for (const section of ["Projects", "Repositories", "Agents"]) {
      await page.getByRole("tab", { name: section, exact: true }).click();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390);
    }
    await page.screenshot({
      path: testInfo.outputPath("setup-mobile.png"),
      fullPage: true,
      animations: "disabled",
    });
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});
