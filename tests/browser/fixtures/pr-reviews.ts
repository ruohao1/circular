import type { Page } from "@playwright/test";
import { legacyIdentity } from "./identity";
import type { components } from "../../../apps/web/src/generated/api";
type Review = components["schemas"]["PRReviewRead"];
type Launch = components["schemas"]["PRReviewLaunch"];
const id = (n: number) =>
  `10000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const date = "2026-09-19T12:00:00Z";
export async function installPRReviewFixture(page: Page) {
  const projectID = id(1),
    sourceRunID = id(2),
    reviewRunID = id(3),
    reviewID = id(4),
    reviewerID = id(5),
    coderID = id(6),
    taskID = id(7),
    repositoryID = id(8);
  const launches: Launch[] = [];
  const publicationRetries = { count: 0 };
  const controls = {
    loseLaunch: false,
    automatic: false,
    settingsError: false,
    unavailableReviewer: false,
  };
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const reviewer = {
    agent_id: reviewerID,
    name: "PR reviewer",
    backend: "codex",
    model: "gpt-6-astra",
    reasoning_effort: "low",
    instructions: "Review the changes",
    backend_config: { model: "gpt-6-astra", reasoning_effort: "low" },
    prompt_version: "pr-review-v1",
    fingerprint: "f".repeat(64),
  };
  const review: Review = {
    id: reviewID,
    run_id: reviewRunID,
    previous_review_id: null,
    attempt: 1,
    automatic: false,
    snapshot: {
      source_run_id: sourceRunID,
      task_id: taskID,
      project_id: projectID,
      repository_id: repositoryID,
      clone_url: "https://github.com/fixture/review.git",
      task_title: "Accept zero at the boundary",
      task_description: "Zero is a valid input.",
      task_external_refs: {},
      evidence: [],
      input_fingerprint: "d".repeat(64),
      reviewer,
      pr: {
        installation_id: "42",
        github_repository_id: "101",
        repository_name: "fixture/review",
        number: 1,
        url: "https://github.com/fixture/review/pull/1",
        base_ref: "main",
        head_ref: "circular/run/source",
        base_sha: "a".repeat(40),
        head_sha: "b".repeat(40),
      },
    },
    merge_base_sha: "a".repeat(40),
    run_status: "succeeded",
    assessment: "findings",
    report: {
      summary:
        "The boundary check still rejects **zero**, contrary to the task.",
      coverage: "complete",
      findings: [
        {
          severity: "high",
          title: "Zero is rejected",
          path: "src/validate.ts",
          side: "head",
          start_line: 2,
          end_line: 2,
          evidence: "`if (value <= 0) return false` rejects zero.",
          consequence: "A valid zero input fails validation.",
          suggested_fix:
            "Change the condition to `value < 0` and add a zero-input regression test.",
        },
      ],
      checks: [
        {
          method: "Inspected the changed condition and task requirements.",
          outcome: "passed",
          evidence: "Compared the branch with its merge base.",
          not_run_reason: "",
        },
      ],
      limitations: [],
    },
    report_error: "",
    freshness: {
      status: "current",
      pr_state: "open",
      observed_base_sha: "a".repeat(40),
      observed_head_sha: "b".repeat(40),
      checked_at: date,
      error: "",
    },
    github: {
      status: "retrying",
      url: "",
      error: "GitHub was temporarily unavailable.",
      retryable: true,
    },
    linear: { status: "delivered", url: "", error: "", retryable: false },
    created_at: date,
  };
  const agent = (isReview: boolean) => ({
    id: isReview ? reviewerID : coderID,
    project_id: projectID,
    name: isReview ? "PR reviewer" : "Coding agent",
    backend: "codex",
    backend_config: isReview ? reviewer.backend_config : {},
    instructions: "Work on the task",
    enabled: !isReview || !controls.unavailableReviewer,
    preset: isReview ? "pr-reviewer" : null,
    created_at: date,
    updated_at: date,
  });
  const run = (isReview: boolean) => ({
    id: isReview ? review.run_id : sourceRunID,
    task_id: taskID,
    agent_id: isReview ? reviewerID : coderID,
    parent_run_id: isReview ? sourceRunID : null,
    kind: isReview ? "pr_review" : "coding",
    backend: "codex",
    status: isReview ? review.run_status : "succeeded",
    attempt: isReview ? 2 : 1,
    worker_id: null,
    claimed_at: date,
    started_at: date,
    finished_at: date,
    error:
      isReview && review.run_status === "failed"
        ? "Review exceeded its execution limit"
        : null,
    external_refs: {},
    request_key: null,
    created_at: date,
    updated_at: date,
  });
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();
    let json: unknown;
    if (path === "/api/v1/projects")
      json = [
        {
          id: projectID,
          name: "Review workspace",
          created_at: date,
          updated_at: date,
        },
      ];
    else if (path === "/api/v1/agents") json = [agent(false), agent(true)];
    else if (path === `/api/v1/agents/${reviewerID}` && method === "PATCH") {
      const body = request.postDataJSON();
      reviewer.backend_config = body.backend_config;
      reviewer.model = body.backend_config.model;
      reviewer.reasoning_effort = body.backend_config.reasoning_effort;
      json = agent(true);
    } else if (path.endsWith("/codex/models"))
      json = {
        default_model: "gpt-6-astra",
        models: [
          {
            id: "gpt-6-astra",
            name: "Astra",
            description: "Review model",
            default_reasoning_effort: "low",
            reasoning_efforts: ["low", "medium", "high"],
          },
          {
            id: "gpt-5.6-sol",
            name: "Sol",
            description: "Alternate model",
            default_reasoning_effort: "low",
            reasoning_efforts: ["low", "high"],
          },
        ],
      };
    else if (path === "/api/v1/runs") json = [run(false), run(true)];
    else if (path.endsWith("/execution")) {
      const isReview = !path.includes(sourceRunID);
      json = {
        run: run(isReview),
        pr_review_id: isReview ? review.id : null,
        task: {
          id: taskID,
          project_id: projectID,
          repository_id: repositoryID,
          title: "Accept zero at the boundary",
          description: "Zero is a valid input.",
          external_refs: {},
          status: "completed",
          created_at: date,
          updated_at: date,
        },
        agent: agent(isReview),
        workspace: {
          id: id(9),
          run_id: isReview ? review.run_id : sourceRunID,
          status: ["succeeded", "failed", "cancelled"].includes(
            review.run_status,
          )
            ? "released"
            : "ready",
          container_id: null,
          worktree_path: null,
        },
        artifacts: [],
        usage: { input_tokens: 120, output_tokens: 90 },
        last_event_sequence: 1,
      };
    } else if (path.endsWith("/events"))
      json = [
        {
          id: id(10),
          run_id: path.includes(sourceRunID) ? sourceRunID : review.run_id,
          sequence: 1,
          type: "agent.message.completed",
          source: "codex",
          data: {
            content:
              "Review output is retained in the structured report above.",
          },
          raw: {},
          occurred_at: date,
          recorded_at: date,
        },
      ];
    else if (path.endsWith("/events/stream")) {
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": fixture\n\n",
      });
      return;
    } else if (path.endsWith("/cancel") && method === "POST") {
      review.run_status = "cancelled";
      review.assessment = "incomplete";
      json = run(true);
    } else if (path.endsWith("/github-delivery")) {
      if (!path.includes(sourceRunID))
        errors.push("Coding publication requested for review run");
      json = {
        status: "delivered",
        pull_request_url: review.snapshot.pr.url,
        number: 1,
        branch: "circular/run/source",
        base_branch: "main",
        base_commit: "a".repeat(40),
        draft: true,
        error: "",
        retryable: false,
      };
    } else if (path.endsWith("/pr-reviews/prepare"))
      json = {
        ready: !controls.unavailableReviewer,
        reason: controls.unavailableReviewer
          ? "The selected reviewer is disabled. Choose an enabled reviewer in Setup."
          : "",
        snapshot: review.snapshot,
      };
    else if (path === `/api/v1/runs/${sourceRunID}/pr-reviews`) {
      if (method === "POST") {
        launches.push(request.postDataJSON());
        if (controls.loseLaunch) {
          await route.abort("connectionreset");
          return;
        }
        json = review;
      } else json = { items: [review], next_cursor: "" };
    } else if (path.endsWith("/integrations/github/pr-reviews")) {
      if (method === "POST") {
        if (controls.settingsError) {
          await route.fulfill({
            status: 403,
            json: { detail: "Approve the GitHub App permissions first." },
          });
          return;
        }
        controls.automatic = request.postDataJSON().automatic;
      }
      json = {
        automatic: controls.automatic,
        available: !controls.unavailableReviewer,
        reviewer_id: reviewerID,
        reviewer: controls.unavailableReviewer ? null : reviewer,
        unavailable_reason: controls.unavailableReviewer
          ? "The selected reviewer is disabled."
          : "",
        pending_count: 0,
      };
    } else if (path.endsWith("/publication/retry")) {
      publicationRetries.count++;
      review.github = {
        status: "published",
        url: "https://github.com/fixture/review/pull/1#pullrequestreview-12",
        error: "",
        retryable: false,
      };
      json = review;
    } else if (path.startsWith("/api/v1/pr-reviews/")) json = review;
    else if (path.endsWith("/integrations"))
      json = ["github", "linear"].map((provider) => ({
        id: id(provider === "github" ? 11 : 12),
        project_id: projectID,
        provider,
        status: "connected",
        configured: true,
        configuration_source: "instance",
        can_manage_app: false,
        app_name: "Circular",
        client_id: "fixture",
        account_name: "Fixture",
        account_url:
          provider === "github"
            ? "https://github.com/fixture"
            : "https://linear.app/fixture",
        account_id: "101",
        enabled: true,
        connected_at: date,
        installation_url: "https://github.com/settings/installations/42",
        app_url: "",
        error: "",
        created_at: date,
        updated_at: date,
      }));
    else if (path.endsWith("/identity"))
      json = legacyIdentity(path.includes("/github/") ? "github" : "linear");
    else if (path.endsWith("/run-delivery"))
      json = {
        enabled: true,
        authorized: true,
        permission_message: "",
        pending_count: 0,
        failed_count: 0,
        last_error: "",
      };
    else if (path.endsWith("/run-updates"))
      json = {
        enabled: true,
        authorized: true,
        pending_count: 0,
        failed_count: 0,
        last_error: "",
      };
    else if (path.endsWith("/linear-delivery"))
      json = {
        status: "delivered",
        issue_url: "https://linear.app/fixture/issue/T-1",
        error: "",
        last_delivered_at: date,
      };
    else if (path === "/api/v1/repositories") json = [];
    else if (path.endsWith("/installations"))
      json = { items: [], next_page: 0 };
    else if (
      path.endsWith("/teams") ||
      path.endsWith("/repositories") ||
      path.endsWith("/projects")
    )
      json = { items: [], next_page: 0, next_cursor: "" };
    else if (method === "GET") json = [];
    else {
      errors.push(`Unexpected mutation: ${method} ${path}`);
      await route.fulfill({
        status: 500,
        json: { detail: "Unexpected fixture mutation" },
      });
      return;
    }
    await route.fulfill({ json });
  });
  return {
    sourceRunID,
    reviewRunID,
    reviewID,
    projectID,
    launches,
    publicationRetries,
    review,
    controls,
    errors,
  };
}
