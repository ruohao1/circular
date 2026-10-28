import {
  expect,
  type APIRequestContext,
  type APIResponse,
  type Page,
} from "@playwright/test";
import { createHash, randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";

const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
const host = compose ? "localhost" : "127.0.0.1";
const base = `http://${host}:${compose ? "8000" : "18000"}/api/v1`;
const origin = `http://${host}:${compose ? "5173" : "15173"}`;
const sessions = new WeakMap<
  APIRequestContext,
  { cookie: string; csrf: string }
>();

// Only the isolated Compose fixture lacks an OAuth provider. Seed its browser
// session in its disposable DB; every decision still uses the production API.
export async function approvalLogin(
  request: APIRequestContext,
  project: string,
) {
  const existing = sessions.get(request);
  if (existing) return existing;
  let cookie = "";
  if (compose) {
    cookie = randomBytes(32).toString("base64url");
    const digest = createHash("sha256").update(cookie).digest("hex");
    execFileSync(
      "docker",
      [
        "compose",
        "--env-file",
        ".env.example",
        "-f",
        "compose.yaml",
        "-f",
        ".github/compose.ci.yaml",
        "exec",
        "-T",
        "postgres",
        "psql",
        "-U",
        "circular",
        "-d",
        "circular",
        "-v",
        "ON_ERROR_STOP=1",
      ],
      {
        input: `INSERT INTO approval_sessions(token_hash,user_id,login,app_client_id,expires_at) VALUES('${digest}','101','fixture-reviewer','fixture-github',now()+interval '30 minutes');`,
      },
    );
  } else {
    const initial = await (
      await request.get(`${base}/approval-auth/session`)
    ).json();
    if (!initial.configured) {
      const response = await request.post(
        `${base}/projects/${project}/integrations/github/register`,
        { data: { organization: "" } },
      );
      expect(response.status(), await response.text()).toBe(200);
      const registration = await response.json();
      const registered = await request.post(registration.registration_url, {
        form: { manifest: registration.manifest },
      });
      expect(registered.ok(), await registered.text()).toBeTruthy();
      const callback = (await registered.text()).match(/href="([^"]+)"/)?.[1];
      expect(callback).toBeTruthy();
      await request.get(callback!.replaceAll("&amp;", "&"));
    }
    const started = await request.post(`${base}/approval-auth/github/start`, {
      headers: { Origin: origin },
      data: { return_path: "/proposals" },
    });
    expect(started.status(), await started.text()).toBe(200);
    await request.get((await started.json()).url);
    cookie =
      (await request.storageState()).cookies.find(
        (c) => c.name === "circular_approval_session",
      )?.value ?? "";
  }
  const state = await (
    await request.get(`${base}/approval-auth/session`, {
      headers: {
        Cookie: `circular_approval_session=${cookie}`,
        Origin: origin,
      },
    })
  ).json();
  expect(["101", "7"]).toContain(state.user?.user_id);
  const session = { cookie, csrf: state.csrf_token as string };
  sessions.set(request, session);
  return session;
}

export async function approveResponse(
  request: APIRequestContext,
  response: APIResponse,
) {
  expect(response.ok(), await response.text()).toBeTruthy();
  const value = await response.json();
  if (response.status() !== 202) return value;
  return approveProposal(request, value);
}

export async function approveProposal(request: APIRequestContext, value: any) {
  const draft = {
    ...value.draft,
    purpose: value.draft.purpose || "Exercise this browser scenario",
    description:
      value.draft.description || "Verify the behavior in this browser scenario",
    acceptance_criteria: value.draft.acceptance_criteria.length
      ? value.draft.acceptance_criteria
      : ["The scenario assertions hold"],
    completion_policy:
      value.draft.completion_policy.mode === "unresolved"
        ? { mode: "direct_completion", requirements: [] }
        : value.draft.completion_policy,
  };
  if (JSON.stringify(draft) !== JSON.stringify(value.draft)) {
    const revised = await request.patch(`${base}/task-proposals/${value.id}`, {
      data: {
        expected_revision: value.revision,
        expected_task_revision: value.expected_task_revision,
        draft,
      },
    });
    expect(revised.ok(), await revised.text()).toBeTruthy();
    value = await revised.json();
  }
  const session = await approvalLogin(request, value.project_id);
  const decided = await request.post(
    `${base}/task-proposals/${value.id}/decision`,
    {
      headers: {
        Origin: origin,
        Cookie: `circular_approval_session=${session.cookie}`,
        "X-Circular-CSRF": session.csrf,
      },
      data: {
        expected_revision: value.revision,
        decision: "approved",
        reason: "Browser fixture approval",
      },
    },
  );
  expect(decided.status(), await decided.text()).toBe(200);
  const result = await decided.json();
  return (await request.get(`${base}/tasks/${result.task_id}`)).json();
}

export async function createResource(
  request: APIRequestContext,
  path: string,
  data: object,
) {
  const response = await request.post(`${base}/${path}`, { data });
  if (path !== "tasks")
    expect(response.status(), await response.text()).toBe(201);
  return approveResponse(request, response);
}

export async function reviewCurrentProposal(page: Page) {
  await expect(page).toHaveURL(/\/proposals\/[0-9a-f-]+/);
  const id = new URL(page.url()).pathname.split("/").at(-1)!;
  const proposal = await (
    await page.request.get(`${base}/task-proposals/${id}`)
  ).json();
  const session = await approvalLogin(page.request, proposal.project_id);
  await page.context().addCookies([
    {
      name: "circular_approval_session",
      value: session.cookie,
      domain: host,
      path: "/",
      httpOnly: true,
      sameSite: "Lax",
    },
  ]);
  await page.reload();
  await page
    .getByRole("button", { name: "Revise proposal", exact: true })
    .click();
  if (!proposal.draft.purpose)
    await page
      .getByLabel("Purpose", { exact: true })
      .fill("Exercise this browser scenario");
  if (!proposal.draft.description)
    await page
      .getByLabel("Scope", { exact: true })
      .fill("Verify the behavior in this browser scenario");
  if (!proposal.draft.acceptance_criteria.length)
    await page
      .getByLabel("Acceptance criteria", { exact: true })
      .fill("The scenario assertions hold");
  if (proposal.draft.completion_policy.mode === "unresolved")
    await page
      .getByLabel("Completion policy", { exact: true })
      .selectOption("direct_completion");
  await page
    .getByRole("button", { name: "Save proposal", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: `Approve revision ${proposal.revision + 1}`,
      exact: true,
    })
    .click();
  await page.getByRole("link", { name: "Open Task", exact: true }).click();
  await expect(page).toHaveURL(/taskId=/);
}
