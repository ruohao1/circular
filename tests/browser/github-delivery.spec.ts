import { expect, test, type Page } from "@playwright/test";
import { legacyIdentity } from "./fixtures/identity";

const projectID = "4b376741-3ba8-4816-befb-d81f8f06d017";
const runID = "c4a926cc-fdbf-41b5-b43d-0da40d5c05c7";
const date = "2026-09-19T10:00:00Z";
const runURL = `/runs/${runID}`;
const settingsPath = `/api/v1/projects/${projectID}/integrations/github/run-delivery`;
const deliveryPath = `/api/v1/runs/${runID}/github-delivery`;
const blankDelivery = {
  status: "not_requested",
  pull_request_url: "",
  number: 0,
  branch: "",
  base_branch: "main",
  base_commit: "a".repeat(40),
  draft: true,
  error: "",
  retryable: false,
};
const blankSettings = {
  enabled: false,
  authorized: true,
  permission_message: "",
  pending_count: 0,
  failed_count: 0,
  last_error: "",
};

async function fixture(page: Page, runStatus = "succeeded") {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let json: unknown = [];
    if (path === "/api/v1/projects") {
      json = [
        {
          id: projectID,
          name: "Pull request review",
          created_at: date,
          updated_at: date,
        },
      ];
    } else if (path.endsWith("/execution")) {
      json = {
        run: {
          id: runID,
          task_id: "task",
          agent_id: "agent",
          backend: "codex",
          status: runStatus,
          attempt: 1,
          created_at: date,
          updated_at: date,
          started_at: date,
          finished_at: date,
          error: null,
          external_refs: {},
        },
        task: {
          id: "task",
          title: "Add the greeting file",
          description: "Create hello.txt",
          project_id: projectID,
          repository_id: "repo",
          status: "completed",
          created_at: date,
          updated_at: date,
          external_refs: {},
        },
        agent: {
          id: "agent",
          name: "Coding agent",
          backend: "codex",
          enabled: true,
          instructions: "Build the task",
          project_id: projectID,
          preset: null,
          created_at: date,
          updated_at: date,
        },
        workspace: {
          id: "workspace",
          run_id: runID,
          status: "released",
          container_id: null,
        },
        usage: { input_tokens: 40, output_tokens: 20 },
        artifacts: [],
        last_event_sequence: 1,
      };
    } else if (path.endsWith("/events")) {
      json = [
        {
          id: "event",
          run_id: runID,
          sequence: 1,
          position: 1,
          source: "worker",
          type: "run.completed",
          occurred_at: date,
          recorded_at: date,
          data: {},
          raw: null,
        },
      ];
    } else if (path.endsWith("/codex/models")) {
      json = { default_model: "gpt-6-astra", models: [] };
    } else if (path.endsWith("/identity")) {
      json = legacyIdentity(path.includes("/github/") ? "github" : "linear");
    } else if (path.endsWith("/integrations")) {
      json = [
        {
          provider: "github",
          status: "connected",
          configured: true,
          setup_available: true,
          app_editable: true,
          account_name: "circular-fixture",
          account_url: "https://github.com/circular-fixture",
          app_slug: "fixture",
          installation_url: "https://github.com/settings/installations/42",
          app_client_id: "",
          callback_url: "",
          registration_url: "",
        },
      ];
    } else if (path.endsWith("/integrations/github/pr-reviews")) {
      json = {
        automatic: false,
        reviewer_id: null,
        reviewer: null,
        available: false,
        unavailable_reason: "Choose a reviewer.",
        pending_count: 0,
      };
    } else if (path.endsWith("/pr-reviews")) {
      json = { items: [], next_cursor: "" };
    } else if (path.endsWith("/installations")) {
      json = { items: [], next_page: 0 };
    } else if (path === settingsPath) {
      json = blankSettings;
    } else if (path === deliveryPath) {
      json = blankDelivery;
    }
    await route.fulfill({ json });
  });
  return errors;
}

test("a completed run publishes once, follows queued delivery, and retains the PR link on reload", async ({
  page,
}, testInfo) => {
  const errors = await fixture(page);
  let state = { ...blankDelivery };
  const writes: string[] = [];
  await page.route(`**${deliveryPath}`, async (route) => {
    if (route.request().method() === "POST") {
      writes.push(route.request().postData() ?? "");
      state = { ...state, status: "pending", branch: `circular/run-${runID}` };
    }
    await route.fulfill({ json: state });
  });
  await page.goto(runURL);
  const card = page.getByRole("region", {
    name: "GitHub pull request",
    exact: true,
  });
  await expect(
    card.getByText("Ready to publish", { exact: true }),
  ).toBeVisible();
  await expect(card.getByText(/Review the Changes tab/)).toBeVisible();
  expect(writes).toEqual([]);
  await card
    .getByRole("button", { name: "Create draft pull request", exact: true })
    .click();
  await expect(
    card.getByText("Waiting to publish", { exact: true }),
  ).toBeVisible();
  await expect(
    card.getByRole("button", {
      name: "Create draft pull request",
      exact: true,
    }),
  ).toHaveCount(0);
  state = {
    ...state,
    status: "delivered",
    number: 12,
    pull_request_url: "https://github.com/circular-fixture/demo/pull/12",
  };
  await expect(
    card.getByRole("link", { name: "Open pull request #12" }),
  ).toHaveAttribute("href", state.pull_request_url);
  await page.reload();
  await expect(
    card.getByRole("link", { name: "Open pull request #12" }),
  ).toBeVisible();
  expect(writes).toEqual(["{}"]);
  await page.setViewportSize({ width: 390, height: 844 });
  await card.scrollIntoViewIfNeeded();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("draft-ready-mobile.png"),
    fullPage: true,
  });
  expect(errors).toEqual([]);
});

test("uncertain delivery checks the same run and permission failures lead to this project's settings", async ({
  page,
}) => {
  const errors = await fixture(page);
  let state = {
    ...blankDelivery,
    status: "uncertain",
    error: "The GitHub response was interrupted.",
  };
  let writes = 0;
  await page.route(`**${deliveryPath}`, async (route) => {
    if (route.request().method() === "POST") {
      writes++;
      state = {
        ...state,
        status: "retrying",
        retryable: true,
        error:
          "Allow Contents and Pull requests read and write, then approve installation access.",
      };
    }
    await route.fulfill({ json: state });
  });
  await page.goto(runURL);
  const card = page.getByRole("region", {
    name: "GitHub pull request",
    exact: true,
  });
  await expect(
    card.getByRole("button", {
      name: "Create draft pull request",
      exact: true,
    }),
  ).toHaveCount(0);
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(
    card.getByRole("button", { name: "Retry publishing", exact: true }),
  ).toBeVisible();
  await expect(
    card.getByText(/Allow Contents and Pull requests/),
  ).toBeVisible();
  expect(writes).toBe(1);
  await card.getByRole("link", { name: "GitHub publishing settings" }).click();
  await expect(page).toHaveURL(/\/setup\?section=integrations$/);
  await expect(
    page.getByRole("combobox", { name: "Project", exact: true }),
  ).toContainText("Pull request review");
  expect(
    await page.evaluate(() =>
      localStorage.getItem("circular.selected-project"),
    ),
  ).toBe(projectID);
  expect(errors).toEqual([]);
});

test("automatic drafts require permission and explicit opt in with mobile-friendly guidance", async ({
  page,
}, testInfo) => {
  const errors = await fixture(page);
  let settings = {
    ...blankSettings,
    authorized: false,
    permission_message:
      "The GitHub installation does not have publishing permissions.",
  };
  const writes: unknown[] = [];
  await page.route(`**${settingsPath}`, async (route) => {
    if (route.request().method() === "POST") {
      const body = route.request().postDataJSON();
      writes.push(body);
      settings = { ...settings, enabled: body.enabled };
    }
    await route.fulfill({ json: settings });
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/setup?section=integrations");
  const publishing = page.getByRole("region", {
    name: "GitHub pull request publishing",
  });
  const toggle = publishing.getByRole("switch", {
    name: "Automatically open draft pull requests",
  });
  await expect(toggle).not.toBeChecked();
  await expect(toggle).toBeDisabled();
  await expect(
    publishing.getByText("Allow pull request publishing", { exact: true }),
  ).toBeVisible();
  await expect(
    publishing.getByText(/Repository permissions → Contents/),
  ).toBeVisible();
  await expect(
    publishing.getByText(/Pull requests are never merged automatically/),
  ).toBeVisible();
  await publishing.scrollIntoViewIfNeeded();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("publishing-permissions-mobile.png"),
    fullPage: true,
  });
  settings = { ...settings, authorized: true, permission_message: "" };
  await publishing
    .getByRole("button", { name: "Refresh publishing permissions" })
    .click();
  await expect(toggle).toBeEnabled();
  await expect(toggle).not.toBeChecked();
  expect(writes).toEqual([]);
  await toggle.click();
  await expect(toggle).toBeChecked();
  await expect(
    publishing.getByText("Automatic drafts on", { exact: true }),
  ).toBeVisible();
  expect(writes).toEqual([{ enabled: true }]);
  await page.reload();
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  expect(writes).toEqual([{ enabled: true }, { enabled: false }]);
  expect(errors).toEqual([]);
});

test("a lost response recovers the existing pull request without reporting a stale failure", async ({
  page,
}) => {
  await fixture(page);
  let state = { ...blankDelivery };
  let writes = 0;
  await page.route(`**${deliveryPath}`, async (route) => {
    if (route.request().method() === "POST") {
      writes++;
      state = {
        ...state,
        status: "delivered",
        number: 18,
        pull_request_url: "https://github.com/circular-fixture/demo/pull/18",
      };
      await route.abort("connectionreset");
      return;
    }
    await route.fulfill({ json: state });
  });
  await page.goto(runURL);
  const card = page.getByRole("region", {
    name: "GitHub pull request",
    exact: true,
  });
  await card
    .getByRole("button", { name: "Create draft pull request", exact: true })
    .click();
  await expect(
    card.getByRole("link", { name: "Open pull request #18" }),
  ).toBeVisible();
  await expect(card.getByRole("alert")).toHaveCount(0);
  await expect(
    card.getByRole("button", {
      name: "Create draft pull request",
      exact: true,
    }),
  ).toHaveCount(0);
  expect(writes).toBe(1);
});

test("no-change and ineligible runs cannot request publishing", async ({
  page,
}) => {
  await fixture(page);
  let state = { ...blankDelivery, status: "no_changes" };
  let writes = 0;
  await page.route(`**${deliveryPath}`, async (route) => {
    if (route.request().method() === "POST") writes++;
    await route.fulfill({ json: state });
  });
  await page.goto(runURL);
  const card = page.getByRole("region", {
    name: "GitHub pull request",
    exact: true,
  });
  await expect(
    card.getByText("No changes to publish", { exact: true }),
  ).toBeVisible();
  await expect(card.getByRole("button")).toHaveCount(0);
  state = {
    ...state,
    status: "not_ready",
    error: "This repository was not imported through GitHub.",
  };
  await page.reload();
  await expect(card.getByText("Not available", { exact: true })).toBeVisible();
  await expect(card.getByRole("button")).toHaveCount(0);
  expect(writes).toBe(0);
});
