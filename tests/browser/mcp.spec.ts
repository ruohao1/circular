import { expect, test, type APIRequestContext } from "@playwright/test";

async function discoverTools(
  request: APIRequestContext,
  url: string,
  clientName: string,
) {
  const headers = {
    Accept: "application/json, text/event-stream",
    "MCP-Protocol-Version": "2025-06-18",
  };
  const initialized = await request.post(url, {
    headers,
    data: {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: clientName, version: "1.0.0" },
      },
    },
  });
  expect(initialized.ok(), await initialized.text()).toBeTruthy();
  expect((await initialized.json()).result.serverInfo.name).toBe(
    "circular-control",
  );
  const listed = await request.post(url, {
    headers,
    data: { jsonrpc: "2.0", id: 2, method: "tools/list", params: {} },
  });
  expect(listed.ok(), await listed.text()).toBeTruthy();
  const body = await listed.json();
  expect(body.error).toBeUndefined();
  return body.result.tools.map(
    (tool: { name: string }) => tool.name,
  ) as string[];
}

test("connect to bundled MCP with one command and real assistant activity", async ({
  page,
  context,
  request,
}, testInfo) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const statusResponse = page.waitForResponse("**/api/v1/mcp/connection");
  await page.goto("/setup?section=mcp");
  const initialStatus = await (await statusResponse).json();
  expect(initialStatus.available).toBe(true);
  await expect(
    page.getByRole("tab", { name: "MCP", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(
    page.getByRole("heading", { name: "Connect your coding agent" }),
  ).toBeVisible();
  await expect(page.getByText("Ready to add", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Circular folder" }),
  ).toBeHidden();
  await expect(page.getByText("Connected", { exact: true })).toHaveCount(0);
  if (initialStatus.last_activity === null) {
    await expect(page.getByText(/No assistant activity yet\./)).toBeVisible();
    await expect(page.getByText(/^Last used by /)).toHaveCount(0);
  }

  const command = page.getByLabel("Connection command", { exact: true });
  const commandText = await command.textContent();
  expect(commandText).toMatch(
    /^codex mcp add circular --url 'https?:\/\/[^']+\/mcp'$/,
  );
  const url = commandText!.match(/--url '([^']+)'/)![1];
  expect(url).toBe(initialStatus.url);
  await page
    .getByRole("button", { name: "Copy connection command", exact: true })
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    commandText,
  );

  const tools = await discoverTools(request, url, "Circular browser check");
  expect(tools).toHaveLength(39);
  expect(tools).toEqual(
    expect.arrayContaining([
      "list_projects",
      "create_project",
      "start_run",
      "launch_pr_review",
      "retry_pr_review_publication",
    ]),
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(
    page.getByText("Last used by Circular browser check", { exact: true }),
  ).toBeVisible();

  await page.getByRole("combobox", { name: "Access", exact: true }).click();
  await page.getByRole("option", { name: "Read only", exact: true }).click();
  await expect(command).toHaveText(
    `codex mcp add circular --url '${url}/read-only'`,
  );
  await expect(
    page.getByRole("heading", { name: "Read-only access", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Accept agent recommendations, start runs and cancel work."),
  ).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "Coding assistant", exact: true })
    .click();
  await page
    .getByRole("option", { name: "Other assistant", exact: true })
    .click();
  await expect(page.getByLabel("Connection URL", { exact: true })).toHaveText(
    `${url}/read-only`,
  );
  await page
    .getByRole("button", { name: "Copy connection url", exact: true })
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    `${url}/read-only`,
  );

  const readOnlyTools = await discoverTools(
    request,
    `${url}/read-only`,
    "Circular read-only check",
  );
  expect(readOnlyTools).toHaveLength(20);
  expect(readOnlyTools).toContain("list_projects");
  expect(readOnlyTools).toContain("get_pr_review");
  expect(readOnlyTools).toContain("refresh_pr_review");
  expect(readOnlyTools).not.toContain("create_project");
  expect(readOnlyTools).not.toContain("start_run");
  expect(readOnlyTools).not.toContain("launch_pr_review");
  expect(readOnlyTools).not.toContain("retry_pr_review_publication");
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(
    page.getByText("Last used by Circular read-only check", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(/No assistant activity yet\./)).toHaveCount(0);

  await page.route(
    "**/api/v1/mcp/connection",
    (route) =>
      route.fulfill({ status: 503, json: { detail: "MCP is restarting" } }),
    { times: 1 },
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(
    page.getByText("MCP unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("MCP is restarting");
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(page.getByText("Ready to add", { exact: true })).toBeVisible();

  await page.screenshot({
    path: testInfo.outputPath("mcp-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: testInfo.outputPath("mcp-mobile.png"),
    fullPage: true,
  });

  await page.getByText("Advanced: use a local server", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Circular folder" })
    .fill("/home/user/Circular workspace");
  const localCommand = page.getByLabel("Local connection command", {
    exact: true,
  });
  await expect(localCommand).toContainText("codex mcp add circular --");
  await expect(localCommand).toContainText(
    "'/home/user/Circular workspace/compose.yaml'",
  );
  await expect(localCommand).toContainText("--read-only");
  await page
    .getByRole("combobox", { name: "Local client configuration" })
    .click();
  await page
    .getByRole("option", { name: "Other clients · JSON configuration" })
    .click();
  const configSnippet = page.getByLabel("MCP configuration", { exact: true });
  let config = JSON.parse((await configSnippet.textContent())!);
  expect(config.mcpServers.circular.args).toContain("http://api:8000");
  expect(config.mcpServers.circular.args).toContain("--read-only");
  await page.getByRole("combobox", { name: "Run with" }).click();
  await page.getByRole("option", { name: "Local Go binary" }).click();
  config = JSON.parse((await configSnippet.textContent())!);
  expect(config.mcpServers.circular.command).toBe(
    "/home/user/Circular workspace/dist/circular-mcp",
  );
  expect(config.mcpServers.circular.args).not.toContain("http://api:8000");
  await page.getByText("Check the local server", { exact: true }).click();
  await expect(page.getByLabel("Check command", { exact: true })).toContainText(
    "--check",
  );
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page
    .getByRole("textbox", { name: "Circular folder" })
    .fill("relative/path");
  await expect(page.getByRole("alert")).toContainText("absolute folder path");
  await expect(configSnippet).toHaveCount(0);
});
