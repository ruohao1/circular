import { expect, type Page } from "@playwright/test";

export async function chooseDirectCompletion(page: Page) {
  const panel = page.getByRole("region", {
    name: "Task completion",
    exact: true,
  });
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
    .getByRole("button", { name: "Save completion policy", exact: true })
    .click();
  await expect(
    panel.getByRole("button", { name: "Edit completion policy", exact: true }),
  ).toBeVisible();
}
