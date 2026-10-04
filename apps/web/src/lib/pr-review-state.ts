import type { components } from "../generated/api";
type PRReview = components["schemas"]["PRReviewRead"];
export function reviewLabel(
  value: Pick<PRReview, "run_status" | "assessment" | "freshness">,
): string {
  if (value.run_status === "failed") return "Failed";
  if (value.run_status === "cancelled") return "Cancelled";
  if (value.run_status === "queued") return "Queued";
  if (value.run_status !== "succeeded") return "Reviewing";
  if (value.freshness.status === "outdated") return "Outdated";
  if (
    value.freshness.status === "unavailable" ||
    value.freshness.status === "unknown"
  )
    return "Freshness unavailable";
  return assessmentLabel(value.assessment);
}
export function assessmentLabel(value: PRReview["assessment"]): string {
  return {
    pending: "Awaiting report",
    findings: "Findings",
    no_blocking_findings: "No blocking findings",
    incomplete: "Incomplete",
  }[value];
}
export const reviewActive = (status: PRReview["run_status"]) =>
  !["succeeded", "failed", "cancelled"].includes(status);

export function reviewRefreshInterval(value?: PRReview): number | false {
  if (!value) return 60_000;
  if (reviewActive(value.run_status)) return 3000;
  if (
    [value.github.status, value.linear.status].some((status) =>
      ["pending", "retrying"].includes(status),
    )
  )
    return 15_000;
  return ["closed", "merged"].includes(value.freshness.pr_state)
    ? false
    : 60_000;
}
