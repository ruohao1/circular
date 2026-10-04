import { expect, test, type Page } from "@playwright/test";
import { legacyIdentity } from "./fixtures/identity";
const base = "http://127.0.0.1:18000/api/v1";
test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Uses the owned OAuth fixture",
);

async function connectGitHub(
  page: Page,
  project: { id: string; name: string },
) {
  const connections = await (
    await page.request.get(`${base}/projects/${project.id}/integrations`)
  ).json();
  const configured = connections.find(
    (c: { provider: string }) => c.provider === "github",
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
}

test("upgrade Linear to Circular identity, pause and reconnect on mobile", async ({
  page,
}, testInfo) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}identity` },
    })
  ).json();
  const path = `${base}/projects/${project.id}/integrations`;
  const connections = await (await page.request.get(path)).json();
  if (
    !connections.find((c: { provider: string }) => c.provider === "linear")
      .configured
  ) {
    expect(
      (
        await page.request.post(`${path}/linear/app`, {
          data: { client_id: "fixture-linear" },
        })
      ).ok(),
    ).toBeTruthy();
  }
  const auth = await (
    await page.request.post(`${path}/linear/connect`, { data: {} })
  ).json();
  await page.goto(auth.authorization_url);
  await expect(
    page.getByText("Linear connected.", { exact: true }),
  ).toBeVisible();
  const identity = page.getByRole("region", {
    name: "Linear publishing identity",
  });
  await expect(
    identity.getByText("Your Linear account", { exact: true }),
  ).toBeVisible();
  await identity
    .getByRole("button", { name: "Enable Circular bot", exact: true })
    .click();
  await expect(identity.getByText("Circular", { exact: true })).toBeVisible();
  await expect(
    page
      .getByRole("region", { name: "Linear request routing" })
      .getByText("Off", { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await identity.scrollIntoViewIfNeeded();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("identity-mobile.png"),
    fullPage: true,
  });
  await identity.getByRole("button", { name: "Pause bot" }).click();
  await expect(identity.getByText("Bot paused", { exact: true })).toBeVisible();
  await expect(identity.getByText("Circular", { exact: true })).toBeVisible();
  await expect(
    identity.getByText("Your Linear account", { exact: true }),
  ).toHaveCount(0);
  await identity.getByRole("button", { name: "Resume bot" }).click();
  await expect(identity.getByText("Circular", { exact: true })).toBeVisible();
});

test("enable GitHub's retained app key and preserve identity after failed replacement", async ({
  page,
}, testInfo) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}github-identity` },
    })
  ).json();
  const path = `${base}/projects/${project.id}/integrations`;
  await connectGitHub(page, project);
  const identity = page.getByRole("region", {
    name: "GitHub publishing identity",
  });
  await expect(
    identity.getByText("Your GitHub account", { exact: true }),
  ).toBeVisible();
  await identity
    .getByRole("button", { name: "Enable Circular bot", exact: true })
    .click();
  await expect(
    identity.getByText("Circular fixture bot", { exact: true }),
  ).toBeVisible();
  await identity
    .getByRole("button", { name: "Update signing key", exact: true })
    .click();
  await identity
    .getByLabel("GitHub App private key", { exact: true })
    .setInputFiles({
      name: "invalid.pem",
      mimeType: "application/x-pem-file",
      buffer: Buffer.from("invalid owned fixture key"),
    });
  await identity.getByRole("button", { name: "Verify and enable bot" }).click();
  await expect(identity.getByRole("alert")).toBeVisible();
  await expect(
    identity.getByText("Circular fixture bot", { exact: true }),
  ).toBeVisible();
  await expect(
    identity.getByLabel("GitHub App private key", { exact: true }),
  ).toHaveValue("");
  await identity.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(
    identity.getByRole("form", { name: "Set up GitHub bot" }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await identity.scrollIntoViewIfNeeded();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("github-identity-mobile.png"),
    fullPage: true,
  });
});

test("missing GitHub bot key can be cancelled without losing the existing connection", async ({
  page,
}) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}legacy-bot-setup` },
    })
  ).json();
  const path = `${base}/projects/${project.id}/integrations`;
  await connectGitHub(page, project);
  await page.route(
    `**/projects/${project.id}/integrations/github/identity`,
    (route) => route.fulfill({ json: legacyIdentity("github") }),
  );
  await page.reload();
  const identity = page.getByRole("region", {
    name: "GitHub publishing identity",
  });
  await identity
    .getByRole("button", { name: "Enable Circular bot", exact: true })
    .click();
  const form = identity.getByRole("form", { name: "Set up GitHub bot" });
  await expect(
    form.getByText("Finish GitHub bot setup", { exact: true }),
  ).toBeVisible();
  await expect(form).toContainText(
    "Your existing GitHub connection stays connected",
  );
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(
    identity.getByText("Your GitHub account", { exact: true }),
  ).toBeVisible();
  const connections = await (await page.request.get(path)).json();
  expect(
    connections.find((c: { provider: string }) => c.provider === "github"),
  ).toMatchObject({ status: "connected", identity_mode: "user" });
});

test("disconnecting a personal GitHub connection keeps the bot active", async ({
  page,
}) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: {
        name: `${process.env.CIRCULAR_E2E_PREFIX}github-personal-disconnect`,
      },
    })
  ).json();
  await connectGitHub(page, project);
  const card = page.getByLabel("GitHub connection", { exact: true });
  const identity = card.getByRole("region", {
    name: "GitHub publishing identity",
  });
  await identity.getByRole("button", { name: "Enable Circular bot" }).click();
  await expect(
    identity.getByText("Circular fixture bot", { exact: true }),
  ).toBeVisible();
  await card.getByText("Connection settings", { exact: true }).click();
  await card.getByRole("button", { name: "Disconnect GitHub" }).click();
  await expect(
    card.getByRole("button", { name: "Connect GitHub", exact: true }),
  ).toBeVisible();
  await expect(card.getByText("Disconnected", { exact: true })).toBeVisible();
  await expect(card.getByText("Not connected", { exact: true })).toBeVisible();
  await expect(card.getByText("Bot paused", { exact: true })).toHaveCount(0);
  await expect(
    card.getByText("Resume bot to restore access", { exact: true }),
  ).toHaveCount(0);
  await expect(
    identity.getByText("Circular fixture bot", { exact: true }),
  ).toBeVisible();
  await expect(
    identity.getByRole("button", { name: "Pause bot", exact: true }),
  ).toBeVisible();
  const state = await (
    await page.request.get(
      `${base}/projects/${project.id}/integrations/github/identity`,
    )
  ).json();
  expect(state).toMatchObject({ mode: "app", status: "enabled" });
});

test("an unavailable publishing identity never appears to fall back to the personal author", async ({
  page,
}) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}identity-unavailable` },
    })
  ).json();
  const path = `${base}/projects/${project.id}/integrations`;
  await connectGitHub(page, project);
  await page.route(
    `**/projects/${project.id}/integrations/github/identity`,
    (route) =>
      route.fulfill({
        status: 503,
        json: { detail: "Identity verification is unavailable" },
      }),
  );
  await page.reload();
  const identity = page.getByRole("region", {
    name: "GitHub publishing identity",
  });
  await expect(
    identity.getByText("Unable to check author", { exact: true }),
  ).toBeVisible();
  await expect(
    identity.getByText("Your GitHub account", { exact: true }),
  ).toHaveCount(0);
  await expect(
    identity.getByRole("button", { name: "Enable Circular bot" }),
  ).toHaveCount(0);
  await page.unroute(`**/projects/${project.id}/integrations/github/identity`);
  await identity.getByRole("button", { name: "Check again" }).click();
  await expect(
    identity.getByText("Your GitHub account", { exact: true }),
  ).toBeVisible();
});
