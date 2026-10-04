import {
  expect,
  test,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

const base = "http://127.0.0.1:18000/api/v1";
const provider = "http://127.0.0.1:18001";

test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Repository creation uses only the disposable GitHub provider fixture.",
);

async function configure(request: APIRequestContext, changes: object = {}) {
  const response = await request.post(`${provider}/fixture/github/creation`, {
    data: {
      administration: "write",
      account_type: "User",
      account: "circular-fixture",
      attach: true,
      reject_status: 0,
      lose_response: false,
      ...changes,
    },
  });
  expect(response.ok(), await response.text()).toBe(true);
}

test.afterEach(async ({ request }) => {
  await configure(request, { reset_created: true });
});

async function connect(page: Page, request: APIRequestContext, suffix: string) {
  await configure(request);
  const created = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}github-${suffix}` },
  });
  expect(created.status(), await created.text()).toBe(201);
  const project: { id: string; name: string } = await created.json();
  const connections = await (
    await request.get(`${base}/projects/${project.id}/integrations`)
  ).json();
  const configured = connections.find(
    (item: { provider: string }) => item.provider === "github",
  ).configured;
  await page.goto("/setup?section=integrations");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: project.name, exact: true }).click();
  await page
    .getByRole("button", { name: "Connect GitHub", exact: true })
    .click();
  if (!configured) {
    await page
      .getByRole("button", { name: "Continue on GitHub", exact: true })
      .click();
    await page
      .getByRole("link", { name: "Create GitHub App", exact: true })
      .click();
    await page
      .getByRole("link", { name: "Install selected repositories", exact: true })
      .click();
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
  return project;
}

async function openCreate(page: Page) {
  await page
    .getByRole("button", {
      name: /^(Create repository|Finish adding repository)$/,
    })
    .click();
  return page.getByRole("dialog");
}

async function repositories(request: APIRequestContext, project: string) {
  const response = await request.get(
    `${base}/repositories?project_id=${project}`,
  );
  expect(response.ok(), await response.text()).toBe(true);
  return response.json();
}

async function remoteRepositories(request: APIRequestContext) {
  const response = await request.get(`${provider}/fixture/github/created`);
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()).items;
}

test("create private and public GitHub repositories with a README and attach them to the selected project", async ({
  page,
  request,
}, testInfo) => {
  const project = await connect(page, request, "create");
  const name = `private-${Date.now()}`;
  const dialog = await openCreate(page);
  await expect(
    dialog.getByRole("heading", {
      name: "Create a GitHub repository",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("combobox", { name: "Visibility", exact: true }),
  ).toContainText("Private");
  await expect(dialog.getByText(/README/)).toBeVisible();
  const submit = dialog.getByRole("button", {
    name: "Create and add repository",
    exact: true,
  });
  await expect(submit).toBeDisabled();
  await dialog.getByLabel("Repository name", { exact: true }).fill(name);
  await dialog
    .getByLabel("Description (optional)", { exact: true })
    .fill("Created by the disposable browser test");
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("github-create-mobile.png"),
    fullPage: true,
  });
  const response = page.waitForResponse(
    (item) =>
      item.request().method() === "POST" &&
      item.url() ===
        `${base}/projects/${project.id}/integrations/github/repositories`,
  );
  await submit.click();
  const result = await (await response).json();
  expect(result).toMatchObject({
    status: "attached",
    name: `circular-fixture/${name}`,
  });
  await expect(
    dialog.getByRole("heading", { name: "Repository ready", exact: true }),
  ).toBeVisible();
  expect(await repositories(request, project.id)).toEqual([
    expect.objectContaining({
      id: result.repository_id,
      name: `circular-fixture/${name}`,
      clone_url: `https://github.com/circular-fixture/${name}.git`,
      external_refs: {
        github: expect.objectContaining({
          installation_id: "101",
          repository_id: result.github_repository_id,
        }),
      },
    }),
  ]);
  const external = await remoteRepositories(request);
  expect(external).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ name, private: true, auto_init: true }),
    ]),
  );
  await page.screenshot({
    path: testInfo.outputPath("github-create-ready-mobile.png"),
    fullPage: true,
  });
  await dialog.getByRole("button", { name: "Close", exact: true }).click();

  // The same creation form supports an installed organization and explicit public visibility.
  await configure(request, {
    account_type: "Organization",
    account: "fixture-org",
  });
  await page
    .getByRole("button", { name: "Refresh GitHub repositories", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "GitHub account", exact: true }),
  ).toContainText("fixture-org");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await openCreate(page);
  const publicName = `public-${Date.now()}`;
  await dialog.getByLabel("Repository name", { exact: true }).fill(publicName);
  await dialog
    .getByRole("combobox", { name: "Visibility", exact: true })
    .click();
  await page.getByRole("option", { name: "Public", exact: true }).click();
  await submit.click();
  await expect(
    dialog.getByRole("heading", { name: "Repository ready", exact: true }),
  ).toBeVisible();
  expect(await repositories(request, project.id)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ name: `fixture-org/${publicName}` }),
    ]),
  );
  expect(await remoteRepositories(request)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        name: publicName,
        private: false,
        auto_init: true,
      }),
    ]),
  );
  await configure(request);
});

test("resume after a lost browser response without creating a second GitHub repository", async ({
  page,
  request,
}) => {
  const project = await connect(page, request, "retry");
  const path = `${base}/projects/${project.id}/integrations/github/repositories`;
  const payloads: Record<string, unknown>[] = [];
  let loseResponse = true;
  await page.route(path, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    payloads.push(route.request().postDataJSON());
    const response = await route.fetch();
    expect(response.ok(), await response.text()).toBe(true);
    if (loseResponse) {
      loseResponse = false;
      await route.abort("failed");
    } else {
      await route.fulfill({ response });
    }
  });
  const name = `retry-${Date.now()}`;
  const dialog = await openCreate(page);
  await dialog.getByLabel("Repository name", { exact: true }).fill(name);
  await dialog
    .getByRole("button", { name: "Create and add repository", exact: true })
    .click();
  await expect(
    dialog.getByText("Check repository creation", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await openCreate(page);
  await dialog
    .getByRole("button", { name: "Check and finish adding", exact: true })
    .click();
  await expect(
    dialog.getByRole("heading", { name: "Repository ready", exact: true }),
  ).toBeVisible();
  expect(payloads).toHaveLength(2);
  expect(payloads[0].request_key).toBeTruthy();
  expect(payloads[1]).toEqual(payloads[0]);
  expect(
    (await remoteRepositories(request)).filter(
      (item: { name: string }) => item.name === name,
    ),
  ).toHaveLength(1);
  expect(await repositories(request, project.id)).toHaveLength(1);
});

test("explain missing creation permission and recover repository access without creating again", async ({
  page,
  request,
}, testInfo) => {
  const project = await connect(page, request, "access");
  await configure(request, { administration: "read" });
  await page
    .getByRole("button", { name: "Refresh GitHub repositories", exact: true })
    .click();
  const dialog = await openCreate(page);
  await expect(
    dialog.getByRole("heading", {
      name: "Allow repository creation",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("listitem").filter({ hasText: /Administration/ }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("button", {
      name: "Create and add repository",
      exact: true,
    }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("github-create-permissions-mobile.png"),
    fullPage: true,
  });
  await dialog.getByRole("button", { name: "Close", exact: true }).click();

  await configure(request, { attach: false });
  await page
    .getByRole("button", { name: "Refresh GitHub repositories", exact: true })
    .click();
  await openCreate(page);
  const name = `access-${Date.now()}`;
  await dialog.getByLabel("Repository name", { exact: true }).fill(name);
  await dialog
    .getByRole("button", { name: "Create and add repository", exact: true })
    .click();
  await expect(
    dialog.getByRole("heading", {
      name: "Repository created — finish adding it",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    dialog.getByRole("link", { name: "Manage GitHub access", exact: true }),
  ).toHaveAttribute("href", `${provider}/settings/installations/101`);
  expect(await repositories(request, project.id)).toHaveLength(0);
  await configure(request);
  await dialog
    .getByRole("button", {
      name: "Check access and add repository",
      exact: true,
    })
    .click();
  await expect(
    dialog.getByRole("heading", { name: "Repository ready", exact: true }),
  ).toBeVisible();
  expect(
    (await remoteRepositories(request)).filter(
      (item: { name: string }) => item.name === name,
    ),
  ).toHaveLength(1);
  expect(await repositories(request, project.id)).toHaveLength(1);
});

test("allow correcting a definitively rejected repository name", async ({
  page,
  request,
}) => {
  const project = await connect(page, request, "validation");
  await configure(request, { reject_status: 422 });
  const dialog = await openCreate(page);
  const input = dialog.getByLabel("Repository name", { exact: true });
  const rejectedName = `rejected-${Date.now()}`;
  await input.fill(rejectedName);
  const submit = dialog.getByRole("button", {
    name: "Create and add repository",
    exact: true,
  });
  await submit.click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await expect(input).toBeEnabled();
  await expect(input).toHaveValue(rejectedName);
  expect(await repositories(request, project.id)).toHaveLength(0);
  await configure(request);
  const name = `corrected-${Date.now()}`;
  await input.fill(name);
  await submit.click();
  await expect(
    dialog.getByRole("heading", { name: "Repository ready", exact: true }),
  ).toBeVisible();
  const external = await remoteRepositories(request);
  expect(
    external.filter((item: { name: string }) => item.name === rejectedName),
  ).toHaveLength(0);
  expect(
    external.filter((item: { name: string }) => item.name === name),
  ).toHaveLength(1);
  expect(await repositories(request, project.id)).toHaveLength(1);
});
