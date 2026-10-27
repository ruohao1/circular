import { test, expect } from "@playwright/test";
import { fixture as taskFixture, A, T } from "./task-fixture";
const project = "10000000-0000-4000-8000-000000000001",
  id = "10000000-0000-4000-8000-000000000002";
const proposal = {
  id,
  project_id: project,
  target_task_id: null,
  task_id: null,
  revision: 2,
  expected_task_revision: 0,
  status: "pending",
  proposer: "Codex",
  source: {},
  draft: {
    title: "Export a report",
    purpose: "Share results",
    description: "Export only selected rows.",
    acceptance_criteria: ["CSV rows match the report"],
    open_questions: [],
    repository_id: null,
    parent_task_id: null,
    prerequisite_task_ids: [],
    priority: "none",
    preferred_agent_id: null,
    planned_start_date: null,
    planned_finish_date: null,
    completion_policy: { mode: "direct_completion", requirements: [] },
    sources: [],
  },
  decision: null,
  created_at: "2026-10-27T12:00:00Z",
  updated_at: "2026-10-27T12:00:00Z",
  review_url: `/proposals/${id}?revision=2`,
};
for (const signedIn of [false, true])
  test(`exact revision review ${signedIn ? "signed in" : "anonymous"}`, async ({
    page,
  }) => {
    const decisions: unknown[] = [];
    await page.route("**/api/v1/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      let data: unknown = [];
      if (path === "/api/v1/projects")
        data = [
          {
            id: project,
            name: "Reports",
            description: "",
            created_at: "2026-10-27T12:00:00Z",
          },
        ];
      if (path === `/api/v1/task-proposals/${id}`) data = proposal;
      if (path === "/api/v1/approval-auth/session")
        data = {
          configured: true,
          user: signedIn
            ? {
                user_id: "101",
                login: "reviewer",
                expires_at: "2026-10-27T13:00:00Z",
              }
            : null,
          csrf_token: signedIn ? "test-csrf" : "",
        };
      if (path.endsWith("/decision")) {
        expect(route.request().headers()["x-circular-csrf"]).toBe("test-csrf");
        decisions.push(route.request().postDataJSON());
        data = {
          ...proposal,
          status: "approved",
          task_id: "10000000-0000-4000-8000-000000000003",
          decision: {
            revision: 2,
            decision: "approved",
            approver_id: "101",
            approver_login: "reviewer",
            reason: "",
            created_at: "2026-10-27T12:01:00Z",
          },
        };
      }
      await route.fulfill({ json: data });
    });
    await page.goto(`/proposals/${id}?revision=${signedIn ? 1 : 2}`);
    await expect(
      page.getByRole("heading", { name: "Export a report", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("CSV rows match the report", { exact: true }),
    ).toBeVisible();
    if (!signedIn) {
      await expect(
        page.getByRole("button", { name: "Sign in with GitHub" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Approve revision 2" }),
      ).toHaveCount(0);
      return;
    }
    await expect(
      page.getByRole("button", { name: "Approve revision 2" }),
    ).toBeDisabled();
    await page.getByRole("link", { name: "Review latest revision" }).click();
    await page.getByRole("button", { name: "Approve revision 2" }).click();
    await expect(
      page.getByText("Approved by reviewer", { exact: true }),
    ).toBeVisible();
    expect(decisions).toEqual([
      { expected_revision: 2, decision: "approved", reason: "" },
    ]);
    await page.screenshot({
      path: "test-results/proposals/approved.png",
      fullPage: true,
    });
  });

test("legacy task creation closes its dialog and consumes its draft", async ({
  page,
}) => {
  await taskFixture(page);
  let creates = 0;
  await page.route("**/api/v1/tasks", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    creates++;
    await route.fulfill({
      status: 202,
      json: {
        ...proposal,
        revision: 1,
        project_id: A,
        draft: { ...proposal.draft, title: "Review this task" },
      },
    });
  });
  await page.route(`**/api/v1/task-proposals/${id}`, (route) =>
    route.fulfill({
      json: {
        ...proposal,
        revision: 1,
        project_id: A,
        draft: { ...proposal.draft, title: "Review this task" },
      },
    }),
  );
  await page.goto("/tasks");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await page.getByLabel("Task title", { exact: true }).fill("Review this task");
  await page.getByRole("button", { name: "Save Task", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/proposals/${id}`));
  await expect(page.getByRole("dialog")).toBeHidden();
  await page.getByRole("link", { name: "All proposals" }).click();
  await page.goto("/tasks");
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue("");
  expect(creates).toBe(1);
});

test("a saved scope proposal leaves Task details without discarding its changes", async ({
  page,
}) => {
  await taskFixture(page);
  let saves = 0;
  await page.route(`**/api/v1/tasks/${T}`, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    saves++;
    await route.fulfill({
      status: 202,
      json: {
        ...proposal,
        project_id: A,
        target_task_id: T,
        expected_task_revision: 1,
        draft: { ...proposal.draft, description: "Reviewed scope change" },
      },
    });
  });
  await page.route(`**/api/v1/task-proposals/${id}`, (route) =>
    route.fulfill({
      json: {
        ...proposal,
        project_id: A,
        target_task_id: T,
        expected_task_revision: 1,
        draft: { ...proposal.draft, description: "Reviewed scope change" },
      },
    }),
  );
  await page.goto(`/tasks?taskId=${T}`);
  await page
    .getByRole("dialog")
    .getByLabel("Description", { exact: true })
    .fill("Reviewed scope change");
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/proposals/${id}`));
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(
    page.getByText("Reviewed scope change", { exact: true }),
  ).toBeVisible();
  expect(saves).toBe(1);
});
