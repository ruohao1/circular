import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  // A broken application backend must not prevent someone reading its help.
  await page.route("**/api/v1/**", (route) => route.abort("connectionrefused"));
});

test("read docs and reload a deep link without contacting the API", async ({
  page,
}, testInfo) => {
  const apiRequests: string[] = [];
  const errors: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/"))
      apiRequests.push(request.url());
  });
  page.on("pageerror", (error) => errors.push(error.message));

  await page.goto("/docs");
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await expect(page).toHaveTitle("Circular docs · Circular Docs");
  await expect(page.locator("#docs-content")).toContainText("Project");
  await page.screenshot({
    path: testInfo.outputPath("docs-desktop.png"),
    fullPage: true,
  });

  await page
    .locator("#docs-content")
    .getByRole("link", {
      name: /Run your first task/,
    })
    .first()
    .click();
  await expect(page).toHaveURL(/\/docs\/quickstart$/);
  await expect(
    page.getByRole("heading", { name: "Run your first task", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page).toHaveTitle("Run your first task · Circular Docs");
  await page
    .getByRole("link", { name: "Create an agent", exact: true })
    .last()
    .click();
  await expect(page).toHaveURL(/\/docs\/quickstart#create-an-agent$/);
  await expect(
    page.getByRole("heading", { name: /^Create an agent/ }),
  ).toBeInViewport();
  expect(apiRequests).toEqual([]);
  expect(errors).toEqual([]);
});

test("keyboard search finds article body text and opens its section", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/docs");
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  // This phrase occurs inside the article, rather than in its title or description.
  await dialog
    .getByPlaceholder("Search", { exact: true })
    .fill("current public app URL");
  const result = dialog.getByRole("button", {
    name: /current public app URL/,
  });
  await expect(result).toBeVisible();
  await result.click();
  await expect(page).toHaveURL(
    /\/docs\/troubleshooting#a-github-app-was-renamed$/,
  );
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole("heading", {
      name: /^A GitHub app was renamed/,
    }),
  ).toBeInViewport();
  expect(errors).toEqual([]);
});

test("a direct section URL scrolls to its heading after loading an uncached guide", async ({
  page,
}) => {
  await page.goto("/docs/quickstart#create-an-agent");
  await expect(
    page.getByRole("heading", { name: "Run your first task", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: /^Create an agent/ }),
  ).toBeInViewport();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: /^Create an agent/ }),
  ).toBeInViewport();
});

test("developer references are absent from navigation, search, and public routes", async ({
  page,
}) => {
  await page.goto("/docs");
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(
      'a[href^="/docs/development"], a[href^="/docs/architecture"], a[href^="/docs/adr"]',
    ),
  ).toHaveCount(0);
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  // This term is present in the repository's unpublished developer references.
  await dialog.getByPlaceholder("Search", { exact: true }).fill("PostgreSQL");
  await expect(
    dialog.getByText("No results found", { exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.goto("/docs/development/local-development");
  await expect(
    page.getByRole("heading", { name: "Page not found", exact: true }),
  ).toBeVisible();
});

test("recover from an unknown page and move between docs and the console", async ({
  page,
}) => {
  await page.goto("/docs/not-a-real-guide");
  await expect(
    page.getByRole("heading", { name: "Page not found", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Go to the documentation home" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Back to console", exact: true })
    .click();
  await expect(page).toHaveURL(/\/$/);
  const navigation = page.getByRole("navigation", { name: "Workspace" });
  await expect(
    navigation.getByRole("link", { name: "Runs", exact: true }),
  ).toBeVisible();
  await navigation.getByRole("link", { name: "Docs", exact: true }).click();
  await expect(page).toHaveURL(/\/docs$/);
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await page.goto("/docs/quickstart");
  await page.getByRole("link", { name: "Setup → Agents", exact: true }).click();
  await expect(page).toHaveURL(/\/setup\?section=agents$/);
  await expect(
    page.getByRole("tab", { name: "Agents", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
});

test("mobile navigation closes after opening a guide and long content stays contained", async ({
  page,
  context,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs");
  await expect(
    page.getByRole("heading", { name: "Circular docs", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Open Sidebar", exact: true }).click();
  await page
    .getByRole("link", {
      name: "Connect your coding agent",
      exact: true,
    })
    .click();
  await expect(page).toHaveURL(/\/docs\/coding-agent$/);
  await expect(
    page.getByRole("heading", {
      name: "Connect your coding agent",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Open Sidebar", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Back to console", exact: true }),
  ).not.toBeVisible();
  const example = page.locator("pre").first();
  const request =
    "Use Circular to list my projects and summarize the latest run.";
  await expect(example).toHaveText(request);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page
    .getByRole("button", { name: "Copy Text", exact: true })
    .first()
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    request,
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("docs-mobile.png"),
    fullPage: true,
  });
});

test("pull request review help is navigable and searchable", async ({ page }) => {
  await page.goto("/docs/pull-request-reviews");
  await expect(page.getByRole("heading", { name: "Pull request reviews", exact: true })).toBeVisible();
  await expect(page.locator("#docs-content")).toContainText("Retry publication");
  await expect(page.locator("#docs-content")).toContainText("additional model work");
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog"); await dialog.getByPlaceholder("Search", { exact: true }).fill("Check publication result");
  await dialog.getByRole("button", { name: /Check publication result/ }).first().click();
  await expect(page).toHaveURL(/\/docs\/(pull-request-reviews|troubleshooting)/);
});
