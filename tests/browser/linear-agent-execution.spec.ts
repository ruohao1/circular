import { expect, test } from "@playwright/test";
import { createHmac, randomUUID } from "node:crypto";
test.skip(
  process.env.CIRCULAR_E2E_COMPOSE === "1",
  "Native Linear execution uses the local stack's provider fixture.",
);
const base = "http://127.0.0.1:18000/api/v1",
  fixture = "http://127.0.0.1:18000/fixture/linear-agent",
  provider = "http://127.0.0.1:18001";
test("signed Linear requests execute frozen inputs, publish under app identities, recover and stop", async ({
  page,
}, info) => {
  test.setTimeout(180_000);
  page.setDefaultTimeout(15_000);
  const req = page.request;
  const post = async (url: string, data: unknown) => {
    const r = await req.post(url, { data });
    expect(r.ok(), `${url}: ${await r.text()}`).toBeTruthy();
    return r.json();
  };
  const project = await post(`${base}/projects`, {
    name: `${process.env.CIRCULAR_E2E_PREFIX}linear-native`,
  });
  const path = `${base}/projects/${project.id}/integrations`;
  const control = (data: Record<string, unknown>) =>
    post(fixture, { project_id: project.id, ...data });
  const secret = "fixture-native-agent-signing-secret";
  const send = async (
    body: unknown,
    id = randomUUID(),
    event = "AgentSessionEvent",
  ) => {
    const raw = JSON.stringify(body);
    const r = await req.post("http://127.0.0.1:18002/webhooks/linear", {
      data: raw,
      headers: {
        "Content-Type": "application/json",
        "Linear-Delivery": id,
        "Linear-Event": event,
        "Linear-Signature": createHmac("sha256", secret)
          .update(raw)
          .digest("hex"),
      },
    });
    expect(r.status(), await r.text()).toBe(200);
  };
  const incoming = async (session: string) => {
    const p = await (
      await req.get(`${base}/external-requests?project_id=${project.id}`)
    ).json();
    const u = await (
      await req.get(`${base}/external-requests?unrouted=true`)
    ).json();
    return [...p.items, ...u.items].find((q) => q.session_id === session);
  };
  const detail = async (id: string) =>
    (await req.get(`${base}/external-requests/${id}`)).json();
  const receipts = async (session: string) =>
    (await req.get(`${fixture}?session=${session}`)).json();
  try {
    let connections = await (await req.get(path)).json();
    if (!connections.find((c) => c.provider === "linear").configured)
      await post(`${path}/linear/app`, { client_id: "fixture-linear" });
    await post(`${provider}/fixture/linear/scopes`, {
      scopes: "read comments:create app:mentionable app:assignable",
    });
    const auth = await post(`${path}/linear/identity/connect`, {
      purpose: "agent",
    });
    await page.goto(auth.authorization_url);
    await expect(
      page
        .getByRole("region", { name: "Linear publishing identity" })
        .getByText("Circular", { exact: true }),
    ).toBeVisible();
    const region = page.getByRole("region", { name: "Linear request routing" });
    await expect(
      region.getByRole("button", { name: "Add destination" }),
    ).toBeDisabled();
    await control({ enable_git: true, paused: true });
    await page
      .getByRole("button", { name: "Connect GitHub", exact: true })
      .click();
    if (!connections.find((c) => c.provider === "github").configured) {
      await page
        .getByRole("button", { name: "Continue on GitHub", exact: true })
        .click();
      await page
        .getByRole("link", { name: "Create GitHub App", exact: true })
        .click();
      await page
        .getByRole("link", {
          name: "Install selected repositories",
          exact: true,
        })
        .click();
      await page
        .getByRole("button", { name: "Connect GitHub", exact: true })
        .click();
    }
    await expect(
      page.getByText("GitHub connected.", { exact: true }),
    ).toBeVisible();
    const imported = await post(`${path}/github/import`, {
      installation_id: "101",
      repository_id: "202",
    });
    await post(`${path}/github/identity/key`, { private_key: "" });
    await post(`${base}/integrations/linear/webhooks`, {
      public_origin: "https://receiver.example",
      signing_secret: secret,
    });
    await send(
      { type: "Issue", action: "update", webhookTimestamp: Date.now() },
      randomUUID(),
      "Issue",
    );
    const agent = await post(`${base}/agents`, {
      project_id: project.id,
      name: "Session engineer",
      backend: "codex",
      instructions: "Frozen agent instructions: preserve the approved model.",
      backend_config: { model: "gpt-6-astra", reasoning_effort: "low" },
    });
    await page.reload();
    await region.getByRole("button", { name: "Add destination" }).click();
    await region
      .getByRole("combobox", { name: "Linear project", exact: true })
      .click();
    await page
      .getByRole("option", { name: "Integration work", exact: true })
      .click();
    await region.getByRole("combobox", { name: "Request repository" }).click();
    await page
      .getByRole("option", { name: "fixture/private-source", exact: true })
      .click();
    await region.getByRole("combobox", { name: "Coding agent" }).click();
    await page
      .getByRole("option", { name: "Session engineer", exact: true })
      .click();
    await expect(region.getByText(/Astra · Low/)).toBeVisible();
    const runMode = region.getByRole("combobox", { name: "Run mode" });
    await expect(runMode).toHaveText("Start automatically");
    await runMode.focus();
    await page.keyboard.press("Enter");
    await expect(
      page.getByRole("option", { name: "Start automatically", exact: true }),
    ).toBeFocused();
    await page.keyboard.press("Home");
    await expect(
      page.getByRole("option", { name: "Ask in Circular first", exact: true }),
    ).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(runMode).toBeFocused();
    await expect(runMode).toHaveText("Ask in Circular first");
    await region
      .getByRole("button", { name: "Enable Linear requests" })
      .click();
    await expect(
      region.getByText("Approval required", { exact: true }),
    ).toBeVisible();
    let route = (
      await (await req.get(`${path}/linear/request-routes`)).json()
    )[0];
    const approvalSession = randomUUID();
    await send(
      await control({
        session_id: approvalSession,
        prompt: "Approval request",
      }),
    );
    await expect
      .poll(async () => (await incoming(approvalSession))?.status)
      .toBe("awaiting_approval");
    const approval = await incoming(approvalSession);
    expect((await detail(approval.id)).run_id).toBe("");
    await page.goto(`/requests/${approval.id}`);
    await expect(
      page.getByRole("button", { name: "Start reviewed request" }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("request-desktop.png"),
      fullPage: true,
    });
    const prior = await detail(approval.id);
    await req.patch(`${base}/agents/${agent.id}`, {
      data: {
        backend_config: { model: "gpt-5.6-terra", reasoning_effort: "high" },
      },
    });
    const stale = await req.post(
      `${base}/external-requests/${approval.id}/start`,
      { data: { expected_input_fingerprint: prior.input_fingerprint } },
    );
    expect(stale.status()).toBe(409);
    await post(`${base}/external-requests/${approval.id}/stop`, {});
    await req.patch(`${base}/agents/${agent.id}`, {
      data: {
        backend_config: { model: "gpt-6-astra", reasoning_effort: "low" },
      },
    });
    route = await post(`${path}/linear/request-routes/${route.id}`, {
      expected_generation: route.generation,
      repository_id: route.repository_id,
      agent_id: route.agent_id,
      mode: "automatic",
      enabled: true,
    });
    await post(`${path}/github/run-delivery`, { enabled: true });
    const reviews = await (await req.get(`${path}/github/pr-reviews`)).json();
    await post(`${path}/github/pr-reviews`, {
      automatic: true,
      reviewer_id: reviews.reviewer_id,
    });
    const session = randomUUID(),
      payload = await control({
        session_id: session,
        prompt: "Approved request text: keep this immutable.",
      });
    const deliveryID = randomUUID();
    await send(payload, deliveryID);
    await expect
      .poll(async () => (await incoming(session))?.run_id, { timeout: 15_000 })
      .toBeTruthy();
    const q = await incoming(session);
    await expect
      .poll(async () => (await receipts(session)).activities.length, {
        timeout: 10_000,
      })
      .toBeGreaterThan(0);
    await req.patch(`${base}/agents/${agent.id}`, {
      data: {
        backend_config: { model: "gpt-5.6-terra", reasoning_effort: "high" },
      },
    });
    await control({ restart: true });
    await send(payload, deliveryID);
    await send(payload);
    expect((await receipts(session)).runs).toBe(1);
    await control({ paused: false });
    await expect
      .poll(async () => (await detail(q.id)).status, { timeout: 45_000 })
      .toBe("succeeded");
    const execution = await (
      await req.get(`${base}/runs/${q.run_id}/execution`)
    ).json();
    expect(execution.workspace.status).toBe("released");
    const events = await (
      await req.get(`${base}/runs/${q.run_id}/events`)
    ).json();
    expect(JSON.stringify(events)).toContain(
      "Fixture completed using gpt-6-astra / low",
    );
    const diff = execution.artifacts.find((a) => a.kind === "diff");
    const content = await (
      await req.get(`${base}/runs/${q.run_id}/artifacts/${diff.id}/content`)
    ).text();
    expect(content).toContain("Approved request text: keep this immutable.");
    expect(content).toContain("Frozen agent instructions");
    expect(content).not.toContain("fixture-only-not-a-real-key");
    await info.attach("executed-input-snapshot", {
      body: Buffer.from(content),
      contentType: "text/plain",
    });
    await expect
      .poll(async () => (await detail(q.id)).pull_request_url, {
        timeout: 25_000,
      })
      .toContain("https://github.com/fixture/private-source/pull/");
    await expect
      .poll(async () => (await receipts(session)).reviews.length, {
        timeout: 40_000,
      })
      .toBe(1);
    await expect
      .poll(async () => JSON.stringify((await receipts(session)).activities), {
        timeout: 15_000,
      })
      .toContain("review");
    const proof = await receipts(session);
    expect(proof.runs).toBe(1);
    expect(proof.ordinary_updates).toBe(0);
    expect(proof.reviews[0].AuthorID).toBe("501");
    expect(
      proof.activities.every(
        (a) => a.actor_id === "20000000-0000-4000-8000-000000000005",
      ),
    ).toBeTruthy();
    expect(new Set(proof.activities.map((a) => a.id)).size).toBe(
      proof.activities.length,
    );
    expect(proof.links.some((l) => l.label === "Pull request")).toBeTruthy();
    await info.attach("native-session-receipts", {
      body: Buffer.from(JSON.stringify(proof, null, 2)),
      contentType: "application/json",
    });
    // Humanless and non-issue sessions cannot silently start model work.
    for (const [flag, want] of [
      ["humanless", "awaiting_approval"],
      ["non_issue", "unsupported"],
    ]) {
      const s = randomUUID();
      await send(
        await control({
          session_id: s,
          [flag]: true,
          prompt: "Do not automatically execute this",
        }),
      );
      await expect.poll(async () => (await incoming(s))?.status).toBe(want);
      expect((await receipts(s)).runs).toBe(0);
      await post(
        `${base}/external-requests/${(await incoming(s)).id}/stop`,
        {},
      );
    }
    // A paused exact-project route leaves new work unrouted; repair prepares approval only.
    route = await post(`${path}/linear/request-routes/${route.id}`, {
      expected_generation: route.generation,
      repository_id: route.repository_id,
      agent_id: route.agent_id,
      mode: "automatic",
      enabled: false,
    });
    const unmapped = randomUUID();
    await send(
      await control({ session_id: unmapped, prompt: "Unmapped request" }),
    );
    await expect
      .poll(async () => (await incoming(unmapped))?.status)
      .toBe("needs_routing");
    route = await post(`${path}/linear/request-routes/${route.id}`, {
      expected_generation: route.generation,
      repository_id: route.repository_id,
      agent_id: route.agent_id,
      mode: "automatic",
      enabled: true,
    });
    const unmappedRequest = await detail((await incoming(unmapped)).id);
    const mapped = await post(
      `${base}/external-requests/${unmappedRequest.id}/route`,
      {
        route_id: route.id,
        expected_input_fingerprint: unmappedRequest.input_fingerprint,
      },
    );
    expect(mapped.status).toBe("awaiting_approval");
    expect(mapped.run_id).toBe("");
    await post(`${base}/external-requests/${mapped.id}/stop`, {});
    // Stop a real running Docker workload and verify the workspace is released.
    const stoppedSession = randomUUID();
    await send(
      await control({
        session_id: stoppedSession,
        issue_id: randomUUID(),
        prompt: "hold-for-stop",
      }),
    );
    await expect
      .poll(async () => (await incoming(stoppedSession))?.status, {
        timeout: 25_000,
      })
      .toBe("running");
    const stopped = await incoming(stoppedSession);
    const competing = randomUUID();
    await send(
      await control({
        session_id: competing,
        issue_id: stopped.issue_id,
        prompt: "Wait for the current run",
      }),
    );
    await expect
      .poll(async () => (await incoming(competing))?.status)
      .toBe("waiting_for_active_run");
    const stop = await control({
      session_id: stoppedSession,
      action: "prompted",
      signal: "stop",
      prompt: "Stop",
    });
    await send(stop);
    await send(stop);
    await expect
      .poll(async () => (await detail(stopped.id)).status)
      .toBe("stopped");
    await expect
      .poll(async () => (await receipts(stoppedSession)).active_workspaces, {
        timeout: 30_000,
      })
      .toBe(0);
    const cancelled = await (
      await req.get(`${base}/runs/${stopped.run_id}`)
    ).json();
    expect(cancelled.status).toBe("cancelled");
    await page.goto(`/requests/${stopped.id}`);
    await expect(page.getByText("Stopped", { exact: true })).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: info.outputPath("native-stopped-mobile.png"),
      fullPage: true,
    });
    const waiting = await incoming(competing);
    expect((await detail(waiting.id)).run_id).toBe("");
    await post(`${base}/external-requests/${waiting.id}/stop`, {});
    // A completed run without publishing stays complete when stopped; no late PR/review starts.
    await post(`${path}/github/run-delivery`, { enabled: false });
    const unpublished = randomUUID();
    await send(
      await control({
        session_id: unpublished,
        issue_id: randomUUID(),
        prompt: "Retain this result without publishing",
      }),
    );
    await expect
      .poll(async () => (await incoming(unpublished))?.status, {
        timeout: 35_000,
      })
      .toBe("succeeded");
    const finished = await incoming(unpublished);
    await post(`${base}/external-requests/${finished.id}/stop`, {});
    await post(`${base}/runs/${finished.run_id}/github-delivery`, {});
    await expect
      .poll(
        async () =>
          (
            await (
              await req.get(`${base}/runs/${finished.run_id}/github-delivery`)
            ).json()
          ).status,
      )
      .toBe("failed");
    expect(
      (await (await req.get(`${base}/runs/${finished.run_id}`)).json()).status,
    ).toBe("succeeded");
    expect(
      (
        await (
          await req.get(`${base}/runs/${finished.run_id}/pr-reviews`)
        ).json()
      ).items,
    ).toHaveLength(0);
    // Reception remains durable while the provider is down. Revocation then stops
    // queued work and disables its route without starting the held workload.
    await control({ paused: true });
    const outageSession = randomUUID(),
      outagePayload = await control({
        session_id: outageSession,
        issue_id: randomUUID(),
        prompt: "Resume verification after outage",
      });
    await control({ outage: true });
    await send(outagePayload);
    expect((await receipts(outageSession)).runs).toBe(0);
    await control({ outage: false });
    await expect
      .poll(async () => (await incoming(outageSession))?.status, {
        timeout: 25_000,
      })
      .toBe("queued");
    await control({ revoked: true });
    await send(
      {
        type: "OAuthApp",
        action: "revoked",
        oauthClientId: "fixture-linear",
        organizationId: "20000000-0000-4000-8000-000000000004",
        webhookTimestamp: Date.now(),
      },
      randomUUID(),
      "OAuthApp",
    );
    await expect
      .poll(async () => (await incoming(outageSession))?.status)
      .toBe("stopped");
    await expect
      .poll(
        async () =>
          (await (await req.get(`${path}/linear/request-routes`)).json())[0]
            .enabled,
      )
      .toBe(false);
    await control({ revoked: false, paused: false });
    expect(
      (await (await req.get(`${path}/linear/request-routes`)).json())[0]
        .enabled,
    ).toBe(false);
  } finally {
    await control({ outage: false, revoked: false, paused: false });
  }
});
