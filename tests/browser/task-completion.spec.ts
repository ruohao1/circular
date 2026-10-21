import { test, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { completionFixture } from "./fixtures/task-completion";
import { T } from "./task-fixture";
const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const base = `http://127.0.0.1:${compose ? "8000" : "18000"}/api/v1`;

test("plan direct completion, run the native report tool, and retain the frozen evidence", async ({
  page,
  request,
}, info) => {
  const source = mkdtempSync(
    join(
      compose ? process.env.CIRCULAR_EXECUTION_HOST_ROOT! : tmpdir(),
      "circular-completion-source-",
    ),
  );
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=main");
  git("config", "user.name", "Fixture");
  git("config", "user.email", "fixture@example.test");
  writeFileSync(join(source, "README.md"), "fixture\n");
  git("add", ".");
  git("commit", "-m", "initial");
  if (compose) git("bundle", "create", "fixture.bundle", "--all");
  const post = async (path: string, data: object) => {
    const r = await request.post(`${base}/${path}`, { data });
    expect(r.status()).toBe(201);
    return r.json();
  };
  try {
    const project = await post("projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}completion-direct`,
    });
    const repo = await post("repositories", {
      project_id: project.id,
      name: "Source",
      clone_url: compose
        ? `/var/lib/circular/${basename(source)}/fixture.bundle`
        : source,
    });
    const agent = await post("agents", {
      project_id: project.id,
      name: "Reporting coder",
      backend: "fake",
      backend_config: {
        delay_ms: 500,
        completion_report: {
          result: "Decision documented",
          evidence: [
            { key: "decision", detail: "Recorded alternatives and reasons" },
          ],
        },
      },
    });
    const task = await post("tasks", {
      project_id: project.id,
      repository_id: repo.id,
      title: "Document decision",
      description: "Record the decision and its reasons",
      preferred_agent_id: agent.id,
    });
    await page.goto(`/tasks?taskId=${task.id}`);
    const panel = page.getByRole("region", {
      name: "Task completion",
      exact: true,
    });
    await expect(
      panel.getByText("Choose a completion policy before starting work.", {
        exact: true,
      }),
    ).toBeVisible();
    await panel
      .getByRole("button", { name: "Set completion policy", exact: true })
      .click();
    await panel
      .getByRole("combobox", { name: "Completion policy", exact: true })
      .click();
    await page
      .getByRole("option", { name: "Direct completion", exact: true })
      .click();
    await panel
      .getByRole("button", { name: "Add requirement", exact: true })
      .click();
    await panel
      .getByLabel("Requirement 1 key", { exact: true })
      .fill("decision");
    await panel
      .getByLabel("Requirement 1 description", { exact: true })
      .fill("Record alternatives and reasons");
    await panel
      .getByRole("button", { name: "Save completion policy", exact: true })
      .click();
    await expect(
      panel.getByRole("button", {
        name: "Edit completion policy",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      panel.getByText("Direct completion", { exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Start Run", exact: true }).click();
    await page.getByRole("button", { name: "Start Run", exact: true }).click();
    await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
    await expect
      .poll(
        async () =>
          (await (await request.get(`${base}/tasks/${task.id}`)).json()).status,
        { timeout: 40000 },
      )
      .toBe("completed");
    await page.goto(`/tasks?taskId=${task.id}`);
    await expect(
      panel.getByText("Frozen when work started", { exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByText("Decision documented", { exact: true }),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", {
        name: "Edit completion policy",
        exact: true,
      }),
    ).toHaveCount(0);
    await panel.scrollIntoViewIfNeeded();
    await page.screenshot({
      path: info.outputPath("completion-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(panel).toBeVisible();
    await panel.scrollIntoViewIfNeeded();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("completion-narrow.png"),
      fullPage: true,
    });
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});

test("review readiness remains separate from Done and stale evidence can recover", async ({
  page,
}, info) => {
  const f = await completionFixture(page);
  await page.goto(`/tasks?taskId=${T}`);
  const panel = page.getByRole("region", {
    name: "Task completion",
    exact: true,
  });
  const refresh = panel.getByRole("button", {
    name: "Refresh completion evidence",
    exact: true,
  });
  await expect(panel).toContainText("Wait for the selected CI job.");
  await expect(panel).toContainText("Frozen when work started");
  await expect(panel).toContainText("high: Zero is rejected");
  await expect(
    panel.getByRole("button", { name: "Edit completion policy" }),
  ).toHaveCount(0);
  // The console renders the server verdict; changing CI fields alone cannot grant readiness.
  f.view.observation!.checks![0].conclusion = "success";
  await refresh.click();
  await expect(panel).toContainText("Completion blocked");
  f.view.verdict = {
    next_stage: "in_review",
    merge_readiness: "ready",
    can_complete: false,
    blockers: [
      {
        code: "merge_required",
        message: "Merge the verified PR.",
        action: "Then explicitly move the Task to Done.",
      },
    ],
    advisories: ["One low-severity finding remains."],
  };
  f.view.review!.findings![0].severity = "low";
  await refresh.focus();
  await page.keyboard.press("Enter");
  await expect(
    panel.getByText("Ready to merge", { exact: true }),
  ).toBeVisible();
  await expect(panel).toContainText("low: Zero is rejected");
  expect(f.state.tasks[0].status).toBe("in_review");
  const workflow = page.getByRole("combobox", {
    name: "Workflow",
    exact: true,
  });
  await workflow.click();
  await page.getByRole("option", { name: "Done", exact: true }).click();
  await page.getByRole("button", { name: "Move to Done", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Merge the verified PR.");
  await expect(
    page.getByRole("button", { name: "Move to Done", exact: true }),
  ).toBeEnabled();
  expect(f.state.tasks[0].status).toBe("in_review");
  f.view.observation!.revision.base_sha = "e".repeat(40);
  f.view.observation!.revision.head_sha = "f".repeat(40);
  f.block(
    "review_outdated",
    "The reviewed base or head changed.",
    "Run an independent review of the current revision.",
  );
  await refresh.click();
  await expect(panel).toContainText("The reviewed base or head changed.");
  await expect(panel.getByText("Ready to merge", { exact: true })).toHaveCount(
    0,
  );
  f.view.observation!.checked_at = new Date(Date.now() - 125_000).toISOString();
  f.block(
    "evidence_stale",
    "Completion evidence expired.",
    "Refresh completion evidence.",
  );
  await refresh.click();
  await expect(panel).toContainText("Observed: 2 minutes ago");
  await expect(panel).toContainText("Completion evidence expired.");
  f.block(
    "provider_unavailable",
    "GitHub App permissions are missing.",
    "Approve Actions and Checks read permissions in GitHub App settings.",
  );
  await refresh.click();
  await expect(
    panel.getByRole("link", { name: "Integrations", exact: true }),
  ).toBeVisible();
  await expect(panel).toContainText("GitHub App permissions are missing.");
  await panel
    .getByLabel("Pull request number", { exact: true })
    .fill("https://github.com/elsewhere/repo/pull/1");
  await panel
    .getByRole("button", { name: "Bind pull request", exact: true })
    .click();
  await expect(panel.getByRole("alert")).toContainText(
    "Enter a positive PR number",
  );
  expect(f.bindings).toHaveLength(0);
  await panel.getByLabel("Pull request number", { exact: true }).fill("2");
  await panel
    .getByRole("button", { name: "Bind pull request", exact: true })
    .click();
  await expect.poll(() => f.bindings.length).toBe(1);
  expect(f.bindings[0]).toEqual({
    number: 2,
    session_id: f.view.current_session_id,
    expected_revision: f.view.revision,
  });
  await expect(panel.getByRole("alert")).toHaveCount(0);
  f.view.observation!.merged = true;
  f.view.observation!.merge_verified = true;
  f.view.verdict = {
    next_stage: "in_review",
    merge_readiness: "ready",
    can_complete: true,
    blockers: [],
    advisories: [],
  };
  await refresh.click();
  await expect(
    panel.getByText("Verified merge · ready for Done", { exact: true }),
  ).toBeVisible();
  expect(f.state.tasks[0].status).toBe("in_review");
  await panel.scrollIntoViewIfNeeded();
  await page.screenshot({
    path: info.outputPath("review-completion-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await panel
    .getByRole("button", { name: "Refresh completion evidence", exact: true })
    .scrollIntoViewIfNeeded();
  expect(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(
    true,
  );
  await page.screenshot({
    path: info.outputPath("review-completion-narrow.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Move to Done", exact: true }).click();
  await expect.poll(() => f.state.tasks[0].status).toBe("completed");
  expect(f.errors).toEqual([]);
});

test("blocked keyboard drop retains In Review placement and focus", async ({
  page,
}) => {
  const f = await completionFixture(page);
  await page.goto("/tasks?view=board");
  const handle = page.getByRole("button", {
    name: "Drag Plan the console",
    exact: true,
  });
  await handle.focus();
  await page.keyboard.press("Space");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Plan the console.",
  );
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[id^="DndLiveRegion"]')).toContainText(
    "Over Done.",
  );
  await page.keyboard.press("Space");
  await expect(
    page.getByRole("status").filter({ hasText: "Move could not be saved" }),
  ).toContainText("The required check is pending.");
  expect(f.state.tasks[0].status).toBe("in_review");
  await expect(
    page.getByRole("region", { name: "Review", exact: true }),
  ).toContainText("Plan the console");
  await expect(handle).toBeFocused();
});

test("repository CI selection requires an explicit save and handles missing permissions", async ({
  page,
}) => {
  const f = await completionFixture(page);
  await page.goto("/setup?section=repositories");
  await page.getByText("Required completion checks", { exact: true }).click();
  const check = page.getByRole("checkbox", { name: /Tests/ });
  await expect(check).not.toBeChecked();
  await expect(
    page.getByRole("button", { name: "Save required checks", exact: true }),
  ).toBeDisabled();
  await check.focus();
  await page.keyboard.press("Space");
  expect(f.checkSaves).toHaveLength(0);
  await page
    .getByRole("button", { name: "Save required checks", exact: true })
    .click();
  await expect(
    page.getByText("Required checks saved.", { exact: true }),
  ).toBeVisible();
  expect(f.checkSaves).toEqual([
    { expected_revision: 0, checks: f.setup.available },
  ]);
  f.setup.reason = "Approve Actions and Checks read permissions.";
  await page
    .getByRole("button", { name: "Refresh available checks", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(f.setup.reason);
  await expect(
    page.getByRole("link", { name: "GitHub App settings", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Save required checks", exact: true }),
  ).toHaveCount(0);
});
