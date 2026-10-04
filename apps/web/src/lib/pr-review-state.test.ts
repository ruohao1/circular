import { describe, expect, it } from "vitest";
import { reviewLabel } from "./pr-review-state";
import type { components } from "../generated/api";
const freshness: components["schemas"]["PRReviewFreshness"] = {
  status: "current",
  pr_state: "open",
  observed_base_sha: "a".repeat(40),
  observed_head_sha: "b".repeat(40),
  checked_at: "2026-09-19T12:00:00Z",
  error: "",
};
describe("review status", () => {
  it("keeps incomplete separate from clean", () => {
    expect(
      reviewLabel({
        run_status: "succeeded",
        assessment: "incomplete",
        freshness,
      }),
    ).toBe("Incomplete");
    expect(
      reviewLabel({
        run_status: "succeeded",
        assessment: "no_blocking_findings",
        freshness,
      }),
    ).toBe("No blocking findings");
  });
  it("shows freshness before the historical assessment", () => {
    expect(
      reviewLabel({
        run_status: "succeeded",
        assessment: "no_blocking_findings",
        freshness: { ...freshness, status: "outdated" },
      }),
    ).toBe("Outdated");
    for (const status of ["unknown", "unavailable"] as const)
      expect(
        reviewLabel({
          run_status: "succeeded",
          assessment: "findings",
          freshness: { ...freshness, status },
        }),
      ).toBe("Freshness unavailable");
  });
  it("does not hide execution failure behind candidate output", () => {
    for (const [run_status, label] of [
      ["failed", "Failed"],
      ["cancelled", "Cancelled"],
      ["queued", "Queued"],
      ["running", "Reviewing"],
    ] as const)
      expect(
        reviewLabel({
          run_status,
          assessment: "no_blocking_findings",
          freshness,
        }),
      ).toBe(label);
  });
});
