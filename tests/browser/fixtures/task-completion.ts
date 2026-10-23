import type { Page } from "@playwright/test";
import type { components } from "../../../apps/web/src/generated/api";
import { fixture, T } from "../task-fixture";

// UI fixtures exercise authoritative responses; provider and persistence guards have Go integration tests.
export async function completionFixture(page: Page) {
  const state = await fixture(page);
  state.tasks[0].status = "in_review";
  state.externalWork = true;
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const session = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
  const review = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
  const sha = (char: string) => char.repeat(40);
  const check = {
    workflow_id: "123",
    app_id: "42",
    workflow_path: ".github/workflows/ci.yml",
    name: "Tests",
  };
  const policy: components["schemas"]["CompletionPolicy"] = {
    mode: "review_required",
    requirements: [{ key: "tests", description: "Verify zero is accepted" }],
  };
  const identity = {
    id: session,
    generation: 1,
    installation_id: "42",
    repository_id: "101",
  };
  const view: components["schemas"]["CompletionView"] = {
    task_id: T,
    revision: 1,
    policy,
    contract: {
      policy,
      check_config: { revision: 1, checks: [check] },
      fingerprint: "c".repeat(64),
    },
    binding: { number: 1, revision: 1 },
    identity,
    session_id: session,
    current_session_id: session,
    outcome: "succeeded",
    released: true,
    owner_active: false,
    cleanup_pending: false,
    needs_user_resolution: false,
    stage: "in_review",
    review_revision: 1,
    report: {
      result: "Zero accepted",
      evidence: [
        {
          key: "tests",
          detail: "Ran the boundary cases",
          links: ["https://github.com/fixture/source/pull/1"],
        },
      ],
    },
    review: {
      id: review,
      run_id: review,
      state: "succeeded",
      base_sha: sha("a"),
      head_sha: sha("b"),
      reviewer_fingerprint: "d".repeat(64),
      report_sha: "e".repeat(64),
      coverage: "complete",
      retained: true,
      findings: [
        {
          severity: "high",
          title: "Zero is rejected",
          path: "src/validate.ts",
          side: "head",
          start_line: 2,
          end_line: 2,
          evidence: "Condition rejects zero",
          consequence: "Valid zero input fails",
          suggested_fix: "Accept zero",
        },
      ],
    },
    observation: {
      session_id: session,
      binding_revision: 1,
      review_revision: 1,
      contract_fingerprint: "c".repeat(64),
      identity,
      review_id: review,
      reviewer_fingerprint: "d".repeat(64),
      review_report_sha: "e".repeat(64),
      pr: {
        installation_id: "42",
        github_repository_id: "101",
        repository_name: "fixture/source",
        number: 1,
        url: "https://github.com/fixture/source/pull/1",
        base_ref: "main",
        head_ref: "external/change",
        base_sha: sha("a"),
        head_sha: sha("b"),
      },
      pr_state: "open",
      revision: {
        base_sha: sha("a"),
        head_sha: sha("b"),
        tested_sha: sha("c"),
        tree_sha: sha("d"),
      },
      checks: [
        {
          check,
          run_id: "1",
          attempt: 2,
          job_id: "2",
          check_run_id: "3",
          tested_sha: sha("c"),
          run_status: "in_progress",
          run_conclusion: "",
          status: "in_progress",
          conclusion: "",
          url: "https://github.com/fixture/source/actions/runs/1",
        },
      ],
      merged: false,
      merge_sha: "",
      merge_verified: false,
      error_code: "",
      checked_at: new Date().toISOString(),
    },
    observed_at: new Date().toISOString(),
    verdict: {
      next_stage: "in_review",
      merge_readiness: "blocked",
      can_complete: false,
      blockers: [
        {
          code: "check_pending",
          message: "The required check is pending.",
          action: "Wait for the selected CI job.",
        },
      ],
      advisories: [],
    },
  };
  const setup: components["schemas"]["CompletionCheckSetup"] = {
    repository_name: "fixture/source",
    config: null,
    available: [check],
    reason: "",
  };
  const bindings: unknown[] = [],
    checkSaves: components["schemas"]["CompletionChecksUpdate"][] = [];
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (path.endsWith("/completion") || path.endsWith("/completion/refresh"))
      return route.fulfill({ json: view });
    if (path.endsWith("/completion-checks")) {
      if (method === "PUT") {
        const input = route.request().postDataJSON();
        checkSaves.push(input);
        setup.config = {
          revision: (setup.config?.revision ?? 0) + 1,
          checks: input.checks,
        };
        return route.fulfill({ json: setup.config });
      }
      return route.fulfill({ json: setup });
    }
    if (path.endsWith("/pull-request")) {
      const input = route.request().postDataJSON();
      bindings.push(input);
      view.binding = { number: input.number, revision: 2 };
      return route.fulfill({ json: view });
    }
    if (path.endsWith("/pr-reviews"))
      return route.fulfill({ json: { items: [], next_cursor: "" } });
    if (path.endsWith("/move") && !view.verdict.can_complete)
      return route.fulfill({
        status: 409,
        json: {
          code: "completion_blocked",
          detail: view.verdict.blockers[0].message,
          blockers: view.verdict.blockers,
        },
      });
    return route.fallback();
  });
  return {
    state,
    view,
    setup,
    bindings,
    checkSaves,
    errors,
    block(code: string, message: string, action: string) {
      view.verdict = {
        next_stage: "in_review",
        merge_readiness: "blocked",
        can_complete: false,
        blockers: [{ code, message, action }],
        advisories: [],
      };
    },
  };
}
