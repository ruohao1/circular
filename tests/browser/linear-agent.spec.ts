import { expect, test } from "@playwright/test";
const base = `http://127.0.0.1:${process.env.CIRCULAR_E2E_COMPOSE === "1" ? "8000" : "18000"}/api/v1`;
test("request inbox exposes unmapped work and a stopped request on mobile", async ({
  page,
}, info) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}requests-ui` },
    })
  ).json();
  const id = "83000000-0000-4000-8000-000000000001";
  let state = {
    id,
    identity_id: "83000000-0000-4000-8000-000000000002",
    session_id: id,
    issue_id: id,
    source_url: "https://linear.app/fixture/issue/FIX-1",
    requester_id: id,
    requester_name: "Alex",
    project_id: "",
    repository_id: "",
    repository_name: "",
    agent_id: "",
    run_id: "",
    title: "Repair the fixture",
    prompt: "## Expected behavior\n\nSave **one** result.",
    status: "needs_routing",
    reason: "Choose a route in Circular for this Linear issue.",
    input_fingerprint: "a".repeat(64),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    agent_name: "",
    model: "",
    reasoning_effort: "",
    run_url: "",
    pull_request_url: "",
    delivery_status: "failed",
    delivery_reason:
      "Restore Circular's Linear agent access to resume delivery.",
    messages: [],
  };
  await page.route("**/api/v1/external-requests**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/stop")) {
      state = {
        ...state,
        status: "stopped",
        reason:
          "Stopped. Provider writes already reserved may finish; their receipts will be checked.",
        delivery_status: "delivered",
      };
      await route.fulfill({ json: state });
      return;
    }
    await route.fulfill({
      json: url.pathname.endsWith(id)
        ? state
        : {
            items: url.searchParams.get("unrouted") === "true" ? [state] : [],
            next_cursor: "",
          },
    });
  });
  await page.goto("/requests");
  await expect(
    page.getByRole("heading", { name: "Unrouted requests" }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Repair the fixture" }).click();
  await expect(
    page.getByRole("heading", { name: "Expected behavior" }),
  ).toBeVisible();
  await expect(page.getByText("Requested by Alex")).toBeVisible();
  await expect(
    page.getByText(
      "Restore Circular's Linear agent access to resume delivery.",
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Start reviewed request" }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Stop request", exact: true }).click();
  await expect(page.getByText("Stopped", { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: info.outputPath("request-mobile.png"),
    fullPage: true,
  });
});
