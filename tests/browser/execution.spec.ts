import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const base = `http://127.0.0.1:${compose ? "8000" : "18000"}/api/v1`;

for (const scenario of ["success", "cancel", "failure"] as const) {
  test(`launch and inspect ${scenario} through the real UI and isolated worker`, async ({
    page,
    request,
    context,
  }, testInfo) => {
    const source = mkdtempSync(
      join(
        compose ? process.env.CIRCULAR_EXECUTION_HOST_ROOT! : tmpdir(),
        "circular-ui-source-",
      ),
    );
    const cloneUrl = compose
      ? `/var/lib/circular/${basename(source)}/fixture.bundle`
      : source;
    const git = (...args: string[]) =>
      execFileSync("git", ["-C", source, ...args]);
    git("init", "--initial-branch=main");
    git("config", "user.name", "Test");
    git("config", "user.email", "test@example.test");
    writeFileSync(join(source, "README.md"), "fixture\n");
    git("add", ".");
    git("commit", "-m", "initial");
    // A bundle is readable across host/container UIDs without disabling Git's
    // ownership checks for a host-owned .git directory.
    if (compose) git("bundle", "create", "fixture.bundle", "--all");
    const post = async (path: string, data: object) => {
      const response = await request.post(`${base}/${path}`, { data });
      expect(response.status()).toBe(201);
      return response.json();
    };
    try {
      const project = await post("projects", {
        name: `${process.env.CIRCULAR_E2E_PREFIX}${scenario}`,
      });
      await post("repositories", {
        project_id: project.id,
        name: "Example repository",
        clone_url: cloneUrl,
      });
      await post("agents", {
        project_id: project.id,
        name: "Implementation engineer",
        backend: "fake",
        backend_config: {
          delay_ms: scenario === "cancel" ? 3000 : 600,
          failure: scenario === "failure" ? "after_first_event" : "none",
        },
      });
      await page.goto("/");
      await page
        .getByRole("combobox", { name: "Project", exact: true })
        .click();
      await expect(page.getByRole("listbox")).toBeVisible({ timeout: 1500 });
      await page
        .getByRole("option", { name: project.name, exact: true })
        .click();
      for (const [label, selected] of [
        ["Repository", "Example repository"],
        ["Agent", "Implementation engineer · fake"],
      ]) {
        const selector = page.getByRole("combobox", {
          name: label,
          exact: true,
        });
        await expect(selector).toContainText(selected);
        await selector.press("Enter");
        const menu = page.getByRole("listbox");
        await expect(menu).toBeVisible();
        // Inspect the actual styled popup, not a browser-native option list.
        const appearance = await menu.evaluate((el) => ({
          background: getComputedStyle(el).backgroundColor,
          radius: getComputedStyle(el).borderRadius,
        }));
        expect(appearance.background).not.toBe("rgba(0, 0, 0, 0)");
        expect(appearance.radius).not.toBe("0px");
        if (scenario === "success")
          await page.screenshot({
            path: testInfo.outputPath(`${label.toLowerCase()}-menu.png`),
            fullPage: true,
          });
        await page
          .getByRole("option", { name: selected, exact: true })
          .press("Enter");
        await expect(menu).not.toBeVisible();
        await expect(selector).toBeFocused();
      }
      await page
        .getByLabel("Task title")
        .fill(`Isolated execution · ${scenario}`);
      await expect(
        page.getByRole("button", { name: "Start Run" }),
      ).toBeEnabled();
      if (scenario === "success")
        await page.screenshot({
          path: testInfo.outputPath("launcher.png"),
          fullPage: true,
        });
      await page.getByRole("button", { name: "Start Run" }).click();
      await expect(page).toHaveURL(/\/runs\/[0-9a-f-]+$/);
      const runId = page.url().split("/").at(-1)!;
      if (scenario === "cancel") {
        await expect(page.locator(".heading-actions .status")).toHaveText(
          "running",
        );
        await page.getByRole("button", { name: "Cancel Run" }).click();
      } else if (scenario === "success") {
        await expect(page.locator(".agent-output")).toContainText(
          "Fake container workload completed:",
        );
        await context.setOffline(true);
        await page.waitForTimeout(900);
        await context.setOffline(false);
      }
      const expected =
        scenario === "success"
          ? "succeeded"
          : scenario === "cancel"
            ? "cancelled"
            : "failed";
      await expect(page.locator(".heading-actions .status")).toHaveText(
        expected,
      );
      await expect(page.locator(".details-column .status")).toHaveText(
        "released",
      );
      await expect(
        page.getByRole("link", { name: "Final diff" }),
      ).toBeVisible();
      if (scenario === "success") {
        await expect(page.locator(".agent-output")).toHaveText(
          "Fake container workload completed: Isolated execution · success\n",
        );
        await page.getByRole("tab", { name: "Changes" }).click();
        await expect(page.locator(".diff-output")).toContainText(
          "+Fake container workload completed:",
        );
        // Tabs keep keyboard navigation and their associated panels after the
        // component migration, without losing the streamed output.
        await page.getByRole("tab", { name: "Changes" }).press("ArrowRight");
        await expect(page.getByRole("tab", { name: "Timeline" })).toBeFocused();
        await expect(page.getByRole("tabpanel")).toContainText(
          "workspace.released",
        );
        await page.getByRole("tab", { name: "Timeline" }).press("Home");
        await expect(
          page.getByRole("tab", { name: "Agent output" }),
        ).toBeFocused();
        await expect(page.locator(".agent-output")).toContainText(
          "Fake container workload completed:",
        );
        await page.getByRole("tab", { name: "Changes" }).click();
      }
      if (scenario === "failure")
        await expect(page.getByRole("alert")).toContainText("injected_failure");
      await page.screenshot({
        path: testInfo.outputPath(`${scenario}.png`),
        fullPage: true,
      });
      if (scenario === "success") {
        await page.setViewportSize({ width: 390, height: 844 });
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth),
        ).toBeLessThanOrEqual(390);
        await page.screenshot({
          path: testInfo.outputPath("success-mobile.png"),
          fullPage: true,
        });
        await page.setViewportSize({ width: 1440, height: 1000 });
      }
      await page.reload();
      await expect(page.locator(".heading-actions .status")).toHaveText(
        expected,
      );
      await page.getByRole("tab", { name: "Timeline" }).click();
      await expect(page.locator(".timeline")).toContainText(
        "workspace.released",
      );
      const events = await (
        await request.get(`${base}/runs/${runId}/events`)
      ).json();
      expect(
        events.map((event: { sequence: number }) => event.sequence),
      ).toEqual(events.map((_: unknown, i: number) => i + 1));
      const types = events.map((event: { type: string }) => event.type);
      if (scenario !== "success") expect(types).not.toContain("run.completed");
      if (scenario === "cancel")
        expect(
          types.filter((type: string) => type === "run.cancelled"),
        ).toHaveLength(1);
      const containers = execFileSync(
        "docker",
        ["ps", "-aq", "--filter", `label=io.circular.run_id=${runId}`],
        { encoding: "utf8" },
      );
      expect(containers.trim()).toBe("");
    } finally {
      rmSync(source, { recursive: true, force: true });
    }
  });
}
