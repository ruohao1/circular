import { expect, test } from "@playwright/test";
import { installPRReviewFixture } from "./fixtures/pr-reviews";

test("an open completed report observes independent delivery and later PR changes", async ({
  page,
}) => {
  await page.clock.install();
  const f = await installPRReviewFixture(page);
  f.review.github.status = "published";
  f.review.linear.status = "pending";
  await page.goto(`/runs/${f.reviewRunID}`);
  const report = page.getByRole("region", { name: "PR review report" });
  await expect(report).toContainText("Reviewed commits still match");
  f.review.linear.status = "delivered";
  await page.clock.fastForward(15_100);
  await expect(report.getByText("Published", { exact: true })).toHaveCount(2);
  f.review.freshness.status = "outdated";
  await page.clock.fastForward(60_100);
  await expect(report.getByText("Outdated", { exact: true })).toBeVisible();
  f.review.freshness.status = "unavailable";
  await page.clock.fastForward(60_100);
  await expect(
    report.getByText("Freshness unavailable", { exact: true }),
  ).toBeVisible();
  expect(f.launches).toHaveLength(0);
  expect(f.publicationRetries.count).toBe(0);
});

test("completed review history observes later PR changes without reopening", async ({
  page,
}) => {
  await page.clock.install();
  const f = await installPRReviewFixture(page);
  f.review.github.status = "published";
  await page.goto(`/runs/${f.sourceRunID}`);
  const history = page.getByRole("region", { name: "Pull request reviews" });
  await expect(history.getByText("Findings", { exact: true })).toBeVisible();
  f.review.freshness.status = "outdated";
  await page.clock.fastForward(60_100);
  await expect(history.getByText("Outdated", { exact: true })).toBeVisible();
  expect(f.launches).toHaveLength(0);
});

test("publication recovery keeps findings and does not relaunch", async ({
  page,
}) => {
  const f = await installPRReviewFixture(page);
  await page.goto(`/runs/${f.reviewRunID}`);
  await expect(
    page.getByRole("heading", { name: "PR review", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Zero is rejected", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "GitHub pull request", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Retry publication", exact: true })
    .click();
  await expect.poll(() => f.publicationRetries.count).toBe(1);
  expect(f.launches).toHaveLength(0);
  await expect(
    page.getByText("Zero is rejected", { exact: true }),
  ).toBeVisible();
  expect(f.errors).toEqual([]);
});
test("manual launch shows the selected model and reuses an uncertain request after reload", async ({
  page,
}) => {
  const f = await installPRReviewFixture(page);
  f.controls.loseLaunch = true;
  await page.goto(`/runs/${f.sourceRunID}`);
  await page.getByRole("button", { name: "Review PR", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("gpt-6-astra");
  await expect(dialog).toContainText("low");
  await expect(dialog).toContainText("b".repeat(40));
  await dialog
    .getByRole("button", { name: "Start review", exact: true })
    .dblclick();
  await expect.poll(() => f.launches.length).toBeGreaterThanOrEqual(1);
  const requestsBeforeReload = f.launches.length;
  for (const payload of f.launches) expect(payload).toEqual(f.launches[0]);
  await page.reload();
  f.controls.loseLaunch = false;
  await page.getByRole("button", { name: "Review PR", exact: true }).click();
  await page
    .getByRole("button", { name: "Check review request", exact: true })
    .click();
  await expect(page).toHaveURL(`/runs/${f.reviewRunID}`);
  expect(f.launches).toHaveLength(requestsBeforeReload + 1);
  expect(f.launches.at(-1)).toEqual(f.launches[0]);
  expect(f.errors).toEqual([]);
});
test("settings are default off, use model controls and restore confirmed values on errors", async ({
  page,
}) => {
  const f = await installPRReviewFixture(page);
  await page.goto("/setup?section=integrations");
  const region = page.getByRole("region", { name: "PR reviews", exact: true });
  const toggle = page.getByRole("switch", {
    name: "Automatically review published PRs",
  });
  await expect(toggle).not.toBeChecked();
  await region
    .getByRole("combobox", { name: "PR reviewer", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("listbox")).toBeVisible();
  await page.keyboard.press("Escape");
  await region
    .getByRole("button", { name: "Edit model for PR reviewer" })
    .click();
  await expect(
    region.getByRole("combobox", { name: "Model", exact: true }),
  ).toBeVisible();
  await region.getByRole("button", { name: "Cancel", exact: true }).click();
  f.controls.settingsError = true;
  await toggle.click();
  await expect(region).toContainText(
    "Approve the GitHub App permissions first.",
  );
  await expect(toggle).not.toBeChecked();
  f.controls.settingsError = false;
  await toggle.focus();
  await page.keyboard.press("Space");
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  expect(f.launches).toHaveLength(0);
  expect(f.errors).toEqual([]);
});
for (const scenario of [
  "queued",
  "running",
  "cancelled",
  "failed",
  "incomplete",
  "clean",
  "low",
  "outdated",
  "unavailable",
  "uncertain",
] as const) {
  test(`review report preserves ${scenario} state`, async ({ page }) => {
    const f = await installPRReviewFixture(page);
    const r = f.review;
    if (["queued", "running", "cancelled", "failed"].includes(scenario)) {
      r.run_status = scenario as typeof r.run_status;
      r.assessment = "incomplete";
      if (scenario === "queued" || scenario === "running") r.report = null;
    }
    if (scenario === "incomplete") {
      r.assessment = "incomplete";
      r.report!.coverage = "incomplete";
      r.report!.limitations = ["Required dependencies were unavailable."];
    }
    if (scenario === "clean" || scenario === "low") {
      r.assessment = "no_blocking_findings";
      if (scenario === "clean") r.report!.findings = [];
      else r.report!.findings[0].severity = "low";
    }
    if (scenario === "outdated" || scenario === "unavailable")
      r.freshness.status = scenario;
    if (scenario === "uncertain") r.github.status = "uncertain";
    await page.goto(`/runs/${f.reviewRunID}`);
    const report = page.getByRole("region", { name: "PR review report" });
    const label = {
      queued: "Queued",
      running: "Reviewing",
      cancelled: "Cancelled",
      failed: "Failed",
      incomplete: "Incomplete",
      clean: "No blocking findings",
      low: "No blocking findings",
      outdated: "Outdated",
      unavailable: "Freshness unavailable",
      uncertain: "Result uncertain",
    }[scenario];
    await expect(
      report.getByText(label, { exact: true }).first(),
    ).toBeVisible();
    if (scenario === "running") {
      await page
        .getByRole("button", { name: "Cancel Run", exact: true })
        .click();
      await expect(
        report.getByText("Cancelled", { exact: true }),
      ).toBeVisible();
    }
    if (scenario === "uncertain") {
      await report
        .getByRole("button", { name: "Check publication result" })
        .click();
      expect(f.launches).toHaveLength(0);
    }
    if (scenario === "outdated") {
      await report
        .getByRole("button", { name: "Review latest commit" })
        .click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Start review" })
        .click();
      await expect.poll(() => f.launches.length).toBe(1);
      expect(f.launches[0].mode).toBe("normal");
    }
    expect(f.errors).toEqual([]);
  });
}
test("review again is an explicit new intent and history opens the retained report", async ({
  page,
}) => {
  const f = await installPRReviewFixture(page);
  await page.goto(`/runs/${f.sourceRunID}`);
  await page.getByRole("link", { name: /Review 1 ·/ }).click();
  await expect(page).toHaveURL(`/runs/${f.reviewRunID}`);
  await page.getByRole("button", { name: "Review again", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Start review", exact: true })
    .click();
  await expect.poll(() => f.launches.length).toBe(1);
  expect(f.launches[0].mode).toBe("again");
  expect(f.launches[0].previous_review_id).toBe(f.reviewID);
});
test("unavailable reviewer has actionable guidance", async ({ page }) => {
  const f = await installPRReviewFixture(page);
  f.controls.unavailableReviewer = true;
  await page.goto(`/runs/${f.sourceRunID}`);
  await page.getByRole("button", { name: "Review PR", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText(
    "selected reviewer is disabled",
  );
  await expect(
    page.getByRole("button", { name: "Start review", exact: true }),
  ).toBeDisabled();
  expect(f.launches).toHaveLength(0);
});
test("report and settings wrap on a narrow screen", async ({
  page,
}, testInfo) => {
  const f = await installPRReviewFixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  f.review.report!.findings[0].path = `src/${"long-path-".repeat(15)}.ts`;
  await page.goto(`/runs/${f.reviewRunID}`);
  await expect(
    page.getByText("Zero is rejected", { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("review-mobile.png"),
    fullPage: true,
  });
  await page.goto("/setup?section=integrations");
  await expect(
    page.getByRole("switch", { name: "Automatically review published PRs" }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("review-settings-mobile.png"),
    fullPage: true,
  });
  expect(f.errors).toEqual([]);
});
