import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const base = `http://127.0.0.1:${compose ? "8000" : "18000"}/api/v1`;
const report = [
  "# Repository discovery",
  "",
  "## Development workflow",
  "",
  "| Check | Command |",
  "| --- | --- |",
  "| Types | `pnpm typecheck` |",
  "",
  "- [x] Read the conventions",
  "",
  "## Recommended agents",
  "",
  "### Product engineer",
  "",
  "**Purpose:** Build the console and improve accessibility.",
  "",
  "Ready-to-copy instructions:",
  "",
  "```text",
  "Read AGENTS.md.\nBuild accessible components and run relevant checks.",
  "```",
  "",
  "### Execution engineer",
  "",
  "**Purpose:** Maintain Go execution and PostgreSQL.",
  "",
  "Ready-to-copy instructions:",
  "",
  "```text",
  "Read the architecture.\nPreserve execution ownership and add behavioral tests.",
  "```",
  "",
  "## Open questions",
  "",
  "[Repository file](/workspace/README.md:1)",
  "",
  "![Tracking image](https://tracker.invalid/pixel)",
  "",
  "<script>window.reportScriptExecuted = true</script>",
].join("\n");

test("render a discovery report and review, edit, create and dismiss its agents", async ({
  page,
  request,
  context,
}, testInfo) => {
  const source = mkdtempSync(
    join(
      compose ? process.env.CIRCULAR_EXECUTION_HOST_ROOT! : tmpdir(),
      "circular-report-source-",
    ),
  );
  const git = (...args: string[]) =>
    execFileSync("git", ["-C", source, ...args]);
  git("init", "--initial-branch=main");
  git("config", "user.name", "Test");
  git("config", "user.email", "test@example.test");
  writeFileSync(join(source, "README.md"), "fixture\n");
  git("add", ".");
  git("commit", "-m", "fixture");
  if (compose) git("bundle", "create", "fixture.bundle", "--all");
  const post = async (path: string, data: object) => {
    const response = await request.post(`${base}/${path}`, { data });
    expect(response.status()).toBe(201);
    return response.json();
  };
  try {
    const project = await post("projects", {
      name: `${process.env.CIRCULAR_E2E_PREFIX}report-review`,
    });
    const repository = await post("repositories", {
      project_id: project.id,
      name: "Report fixture",
      clone_url: compose
        ? `/var/lib/circular/${basename(source)}/fixture.bundle`
        : source,
    });
    const agent = await post("agents", {
      project_id: project.id,
      name: "Report runner",
      backend: "fake",
      backend_config: { delay_ms: 10 },
    });
    const task = await post("tasks", {
      project_id: project.id,
      repository_id: repository.id,
      title: "Understand the repository",
    });
    const run = await post("runs", { task_id: task.id, agent_id: agent.id });
    await expect
      .poll(
        async () =>
          (await (await request.get(`${base}/runs/${run.id}/execution`)).json())
            .workspace?.status,
      )
      .toBe("released");
    // Keep the real Run and API, substituting only report text in replay. No
    // model invocation is needed to exercise the complete creation flow.
    await page.route(`**/api/v1/runs/${run.id}/events?**`, async (route) => {
      const response = await route.fetch();
      const events = await response.json();
      await route.fulfill({
        response,
        json: events.map((event: { type: string; data: object }) =>
          event.type === "agent.message.completed"
            ? { ...event, data: { content: report } }
            : event,
        ),
      });
    });
    let imageRequests = 0;
    page.on("request", (request) => {
      if (request.url().includes("tracker.invalid")) imageRequests++;
    });
    await page.goto(`/runs/${run.id}`);
    const output = page.locator(".agent-output");
    await expect(
      output.getByRole("heading", {
        name: "Repository discovery",
        exact: true,
      }),
    ).toBeVisible();
    await expect(output.getByRole("table")).toContainText("pnpm typecheck");
    await expect(
      output.getByRole("button", { name: "Copy code", exact: true }),
    ).toHaveCount(2);
    await expect(output.locator("img, script")).toHaveCount(0);
    expect(imageRequests).toBe(0);
    await page.getByRole("button", { name: "Markdown", exact: true }).click();
    await expect(output).toContainText("# Repository discovery");
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    await page
      .getByRole("button", { name: "Copy report", exact: true })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      report,
    );

    const suggestions = page.getByRole("region", { name: "Suggested agents" });
    await expect(
      suggestions.getByRole("button", { name: "Review & create" }),
    ).toHaveCount(2);
    await suggestions
      .getByRole("button", { name: "Review & create" })
      .first()
      .click();
    const dialog = page.getByRole("dialog", { name: "Review suggested agent" });
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByRole("combobox", { name: "Model", exact: true }),
    ).toContainText("GPT-6-Astra");
    await expect(
      dialog.getByRole("combobox", { name: "Variant", exact: true }),
    ).toContainText("Low");
    await dialog
      .getByLabel("Agent name", { exact: true })
      .fill("Reviewed product engineer");
    await dialog
      .getByLabel("Instructions", { exact: true })
      .fill(
        "Read AGENTS.md. Build accessible UI and verify keyboard navigation.",
      );
    await dialog.getByRole("combobox", { name: "Model", exact: true }).click();
    await page
      .getByRole("option", { name: "GPT-5.6-Sol", exact: true })
      .click();
    await dialog
      .getByRole("combobox", { name: "Variant", exact: true })
      .click();
    await page.getByRole("option", { name: "High", exact: true }).click();
    await dialog.getByRole("button", { name: "Preview instructions" }).click();
    await expect(dialog.locator(".markdown-body")).toContainText(
      "verify keyboard navigation",
    );
    await dialog.getByRole("button", { name: "Edit instructions" }).click();
    await page.screenshot({
      path: testInfo.outputPath("review-agent.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await expect(
      dialog.getByRole("button", { name: "Create agent", exact: true }),
    ).toBeInViewport();
    await page.screenshot({
      path: testInfo.outputPath("review-agent-mobile.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 1000 });

    // Simulate a response lost after the server committed. Retrying must return
    // the one accepted Agent and retain all reviewed settings.
    await page.route(
      `**/api/v1/runs/${run.id}/agent-proposals/*/create`,
      async (route) => {
        const response = await route.fetch();
        expect(response.status()).toBe(200);
        await route.fulfill({
          status: 503,
          json: { detail: "Connection interrupted. Try again." },
        });
      },
      { times: 1 },
    );
    await dialog
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(dialog.getByRole("alert")).toContainText(
      "Connection interrupted",
    );
    await expect(dialog.getByLabel("Agent name", { exact: true })).toHaveValue(
      "Reviewed product engineer",
    );
    await dialog
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(suggestions.getByText("Created", { exact: true })).toHaveCount(
      1,
    );
    await expect(
      suggestions.getByRole("heading", {
        name: "Reviewed product engineer",
        exact: true,
      }),
    ).toBeVisible();
    await expect(suggestions).toContainText("GPT-5.6-Sol · High");
    const agents = await (
      await request.get(`${base}/agents?project_id=${project.id}`)
    ).json();
    const created = agents.filter(
      (item: { name: string }) => item.name === "Reviewed product engineer",
    );
    expect(created).toHaveLength(1);
    expect(created[0].backend_config).toEqual({
      model: "gpt-5.6-sol",
      reasoning_effort: "high",
    });
    expect(created[0].instructions).toContain("verify keyboard navigation");
    expect(
      await (await request.get(`${base}/runs?project_id=${project.id}`)).json(),
    ).toHaveLength(1);

    await suggestions
      .getByRole("button", { name: "Dismiss", exact: true })
      .click();
    await expect(
      suggestions.getByRole("button", { name: "Review & create" }),
    ).toHaveCount(0);
    await page.reload();
    await expect(suggestions.getByText("Created", { exact: true })).toHaveCount(
      1,
    );
    await suggestions
      .getByRole("button", { name: "Show dismissed (1)" })
      .click();
    await expect(
      suggestions.getByText("Dismissed", { exact: true }),
    ).toHaveCount(1);

    // A structured proposal works independently of Markdown formatting.
    await post(`runs/${run.id}/agent-proposals`, {
      name: "Structured recommendation",
      purpose: "Proposed through the MCP event pipeline",
      instructions: "Follow repo conventions and test the behavior.",
      model: "gpt-5.6-terra",
      reasoning_effort: "high",
      model_reason:
        "Terra with high reasoning for changes spanning worker ownership and recovery.",
    });
    await page.reload();
    await expect(
      suggestions.getByRole("heading", { name: "Structured recommendation" }),
    ).toBeVisible();
    await suggestions
      .getByText("Why this model and variant", { exact: true })
      .click();
    await expect(suggestions).toContainText(
      "Terra with high reasoning for changes spanning worker ownership and recovery.",
    );
    await suggestions.getByRole("button", { name: "Review & create" }).click();
    await expect(
      dialog.getByRole("combobox", { name: "Model", exact: true }),
    ).toContainText("GPT-5.6-Terra");
    await expect(
      dialog.getByRole("combobox", { name: "Variant", exact: true }),
    ).toContainText("High");
    await expect(
      dialog.getByText(
        "Selected by the orchestrating agent. You can change either choice.",
      ),
    ).toBeVisible();
    await dialog.getByRole("combobox", { name: "Model", exact: true }).click();
    await page
      .getByRole("option", { name: "Custom model…", exact: true })
      .click();
    await dialog
      .getByLabel("Custom model ID", { exact: true })
      .fill("custom-model");
    await expect(
      dialog.getByText("Your model and variant will be used."),
    ).toBeVisible();
    await dialog
      .getByRole("button", { name: "Use recommendation", exact: true })
      .click();
    await expect(
      dialog.getByRole("combobox", { name: "Model", exact: true }),
    ).toContainText("GPT-5.6-Terra");
    await expect(
      dialog.getByRole("combobox", { name: "Variant", exact: true }),
    ).toContainText("High");
    await expect(
      dialog.getByLabel("Custom model ID", { exact: true }),
    ).toHaveCount(0);
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(
      dialog.getByRole("button", { name: "Create agent", exact: true }),
    ).toBeInViewport();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: testInfo.outputPath("model-recommendation-mobile.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await dialog
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    const recommended = (
      await (
        await request.get(`${base}/agents?project_id=${project.id}`)
      ).json()
    ).find(
      (item: { name: string }) => item.name === "Structured recommendation",
    );
    expect(recommended.backend_config).toEqual({
      model: "gpt-5.6-terra",
      reasoning_effort: "high",
    });
    await page.reload();
    await expect(suggestions.getByText("Created", { exact: true })).toHaveCount(
      2,
    );
    await page.screenshot({
      path: testInfo.outputPath("report-and-agents.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: testInfo.outputPath("report-mobile.png"),
      fullPage: true,
    });
    await suggestions
      .getByRole("link", { name: "View agents" })
      .first()
      .click();
    await expect(page).toHaveURL(/\/setup\?section=agents$/);
    await expect(
      page.getByRole("heading", {
        name: "Reviewed product engineer",
        exact: true,
      }),
    ).toBeVisible();
  } finally {
    rmSync(source, { recursive: true, force: true });
  }
});
