import { expect, test } from "@playwright/test";

const base = "http://127.0.0.1:18000/api/v1";
const fixture = "http://127.0.0.1:18000/fixture/pr-review";
test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Requires the disposable review fixture workload.",
);

test("a source task becomes a retained review and reaches GitHub and Linear", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  const created = await request.post(`${base}/projects`, {
    data: { name: `${process.env.CIRCULAR_E2E_PREFIX}review-execution` },
  });
  expect(created.status()).toBe(201);
  const project = await created.json();
  const connections = await (
    await request.get(`${base}/projects/${project.id}/integrations`)
  ).json();
  await page.goto("/setup?section=integrations");
  await page.getByRole("combobox", { name: "Project", exact: true }).click();
  await page.getByRole("option", { name: project.name, exact: true }).click();
  await page
    .getByRole("button", { name: "Connect GitHub", exact: true })
    .click();
  if (
    !connections.find((v: { provider: string }) => v.provider === "github")
      .configured
  ) {
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
  if (
    !connections.find((v: { provider: string }) => v.provider === "linear")
      .configured
  ) {
    const saved = await request.post(
      `${base}/projects/${project.id}/integrations/linear/app`,
      { data: { client_id: "fixture-linear" } },
    );
    expect(saved.ok(), await saved.text()).toBe(true);
    await page.reload();
  }
  await page
    .getByRole("button", { name: "Connect Linear", exact: true })
    .click();
  await expect(
    page.getByText("Linear connected.", { exact: true }),
  ).toBeVisible();
  const enabled = await request.post(
    `${base}/projects/${project.id}/integrations/linear/run-updates`,
    { data: { enabled: true } },
  );
  expect(enabled.ok(), await enabled.text()).toBe(true);
  const seeded = await request.post(fixture, {
    data: { project_id: project.id },
  });
  expect(seeded.ok(), await seeded.text()).toBe(true);
  const source = await seeded.json();
  try {
    await page.goto(`/runs/${source.source_run_id}`);
    await page.getByRole("button", { name: "Review PR", exact: true }).click();
    await expect(page.getByRole("dialog")).toContainText(source.head_sha);
    await page
      .getByRole("button", { name: "Start review", exact: true })
      .click();
    await expect(page).not.toHaveURL(`/runs/${source.source_run_id}`);
    const runID = page.url().split("/").at(-1)!;
    const report = page.getByRole("region", { name: "PR review report" });
    await expect(
      report.getByText("Zero is rejected", { exact: true }),
    ).toBeVisible({ timeout: 60_000 });
    await expect(page.locator(".heading-actions .status")).toHaveText(
      "succeeded",
    );
    const execution = await (
      await request.get(`${base}/runs/${runID}/execution`)
    ).json();
    expect(execution.run).toMatchObject({
      kind: "pr_review",
      task_id: source.task_id,
      parent_run_id: source.source_run_id,
    });
    expect(execution.workspace.status).toBe("released");
    const artifact = execution.artifacts.find(
      (v: { kind: string }) => v.kind === "pr_review_report",
    );
    expect(artifact).toBeTruthy();
    const retained = await request.get(
      `${base}/runs/${runID}/artifacts/${artifact.id}/content`,
    );
    expect(retained.ok(), await retained.text()).toBe(true);
    expect((await retained.json()).findings[0].title).toBe("Zero is rejected");
    await expect
      .poll(
        async () =>
          (
            await (
              await request.get(`${base}/pr-reviews/${execution.pr_review_id}`)
            ).json()
          ).github.status,
        { timeout: 35_000 },
      )
      .toBe("published");
    await expect
      .poll(
        async () =>
          (
            await (
              await request.get(`${base}/pr-reviews/${execution.pr_review_id}`)
            ).json()
          ).linear.status,
        { timeout: 35_000 },
      )
      .toBe("delivered");
    const receipts = await (await request.get(fixture)).json();
    const matchingReviews = receipts.reviews.filter((v: { Body: string }) =>
      v.Body.includes(`/runs/${runID}`),
    );
    expect(matchingReviews).toHaveLength(1);
    expect(matchingReviews[0]).toMatchObject({ CommitID: source.head_sha });
    expect(matchingReviews[0].Body).toContain("Zero is rejected");
    const comments = await (
      await request.get("http://127.0.0.1:18001/fixture/linear/comments")
    ).json();
    const matching = comments.items.filter((v: { body: string }) =>
      v.body.includes(runID),
    );
    expect(matching).toHaveLength(1);
    expect(matching[0].body).toContain("high");
  } finally {
    await request.delete(fixture);
  }
});
