import { expect, test } from "@playwright/test";
import { createHmac, randomUUID } from "node:crypto";
test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Webhook reception uses the local stack's provider fixture.",
);
const base = "http://127.0.0.1:18000/api/v1";
test("reception stays waiting until signed delivery and the receiver has no control routes", async ({
  page,
}, info) => {
  const project = await (
    await page.request.post(`${base}/projects`, {
      data: { name: `${process.env.CIRCULAR_E2E_PREFIX}webhooks` },
    })
  ).json();
  const path = `${base}/projects/${project.id}/integrations`;
  const connections = await (await page.request.get(path)).json();
  if (
    !connections.find((c: { provider: string }) => c.provider === "linear")
      .configured
  ) {
    expect(
      (
        await page.request.post(`${path}/linear/app`, {
          data: { client_id: "fixture-linear" },
        })
      ).ok(),
    ).toBeTruthy();
  }
  // Earlier scenarios may have enabled the shared workspace's agent grant.
  // Reconnecting must retain that grant, not simulate a permission downgrade.
  expect(
    (
      await page.request.post("http://127.0.0.1:18001/fixture/linear/scopes", {
        data: { scopes: "read comments:create app:mentionable app:assignable" },
      })
    ).ok(),
  ).toBeTruthy();
  const auth = await (
    await page.request.post(`${path}/linear/identity/connect`, {
      data: { purpose: "identity" },
    })
  ).json();
  await page.goto(auth.authorization_url);
  const region = page.getByRole("region", { name: "Linear incoming events" });
  await expect(region).toBeVisible();
  await region
    .getByRole("button", {
      name: /^(Configure receiver|Edit receiver settings)$/,
    })
    .click();
  await region
    .getByLabel("Public receiver address", { exact: true })
    .fill("https://receiver.example");
  const secret = `fixture-linear-browser-signing-secret-${randomUUID()}`;
  await region.getByLabel("Signing secret", { exact: true }).fill(secret);
  await region
    .getByRole("button", { name: "Save receiver settings", exact: true })
    .click();
  await expect(
    region.getByText("Waiting for a verified delivery", { exact: true }),
  ).toBeVisible();
  await expect(
    region.getByLabel("Signing secret", { exact: true }),
  ).toHaveCount(0);
  for (const endpoint of [
    "/api/v1/runs",
    "/mcp",
    "/api/v1/integrations/linear/callback",
    "/artifacts",
  ]) {
    expect(
      (
        await page.request.post(`http://127.0.0.1:18002${endpoint}`, {
          data: {},
        })
      ).status(),
    ).toBe(404);
  }
  const body = JSON.stringify({
    webhookTimestamp: Date.now(),
    type: "Issue",
    action: "update",
  });
  const send = async (key: string) =>
    page.request.post("http://127.0.0.1:18002/webhooks/linear", {
      data: body,
      headers: {
        "Content-Type": "application/json",
        "Linear-Delivery": randomUUID(),
        "Linear-Event": "Issue",
        "Linear-Signature": createHmac("sha256", key)
          .update(body)
          .digest("hex"),
      },
    });
  expect((await send("wrong-secret")).status()).toBe(401);
  await region.getByRole("button", { name: "Check reception" }).click();
  await expect(
    region.getByText("Waiting for a verified delivery", { exact: true }),
  ).toBeVisible();
  expect((await send(secret)).status()).toBe(200);
  await region.getByRole("button", { name: "Check reception" }).click();
  await expect(
    region.getByText("Receiving events", { exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await region.scrollIntoViewIfNeeded();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: info.outputPath("webhooks-mobile.png"),
    fullPage: true,
  });
});
