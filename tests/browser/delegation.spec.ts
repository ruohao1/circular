import { expect, test } from "@playwright/test";

test("delegation stays live across parent suspension", async ({ page }) => {
  const runId = "44444444-4444-4444-8444-444444444444";
  const project = "11111111-1111-4111-8111-111111111111";
  const stamp = "2026-10-14T10:00:00Z";
  const event = (sequence: number, type: string) => ({
    id: `event-${sequence}`, run_id: runId, sequence, position: sequence,
    type, source: "worker", data: { invocation: sequence < 6 ? 1 : 2 },
    raw: null, occurred_at: stamp, recorded_at: stamp,
  });
  await page.addInitScript(() => {
    const streams: Array<EventTarget & { url: string; closed: boolean; onopen?: () => void; onerror?: () => void }> = [];
    class FixtureSource extends EventTarget {
      closed = false;
      onopen?: () => void;
      onerror?: () => void;
      constructor(public url: string) {
        super(); streams.push(this); setTimeout(() => this.onopen?.(), 0);
      }
      close() { this.closed = true; }
    }
    Object.assign(window, { EventSource: FixtureSource, delegationStreams: streams });
  });
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    let body: unknown = [];
    if (path.endsWith("/projects")) body = [{ id: project, name: "Delegation fixture", created_at: stamp, updated_at: stamp }];
    if (path.endsWith("/execution")) body = {
      run: { id: runId, task_id: "task", agent_id: "agent", backend: "fake", status: "waiting_for_input", attempt: 1, external_refs: {}, created_at: stamp, updated_at: stamp, started_at: stamp, kind: "coding" },
      task: { id: "task", title: "Parent objective", description: "Continue after specialist", project_id: project, status: "open", external_refs: {} },
      agent: { id: "agent", name: "Parent", backend: "fake", project_id: project },
      workspace: null, artifacts: [], usage: { input_tokens: 10, output_tokens: 20 },
      invocation: 1, delegation: null, last_event_sequence: 1, pr_review_id: null,
    };
    if (path.endsWith("/events") && Number(url.searchParams.get("after")) === 0) body = [event(1, "run.started")];
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto(`/runs/${runId}`);
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  const emit = async (sequence: number, type: string) => {
    await page.evaluate((record) => {
      const streams = (window as unknown as { delegationStreams: EventTarget[] }).delegationStreams;
      streams.at(-1)!.dispatchEvent(new MessageEvent(record.type, { data: JSON.stringify(record) }));
    }, event(sequence, type));
  };
  await page.getByRole("tab", { name: /Timeline/ }).click();
  for (const [index, type] of ["delegation.requested", "delegation.created", "run.suspended", "workspace.suspended", "run.continued"].entries()) {
    await emit(index + 2, type);
    await expect(page.getByText(type, { exact: true })).toBeVisible();
  }
  await expect(page.getByText("Live", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { delegationStreams: unknown[] }).delegationStreams.length)).toBe(1);
  await page.evaluate(() => (window as unknown as { delegationStreams: Array<{ onerror: () => void }> }).delegationStreams[0].onerror());
  await expect.poll(() => page.evaluate(() => (window as unknown as { delegationStreams: Array<{ url: string }> }).delegationStreams.at(-1)!.url)).toContain("after=6");
  await emit(7, "run.completed");
  await expect(page.getByText("Complete", { exact: true })).toBeVisible();
});
