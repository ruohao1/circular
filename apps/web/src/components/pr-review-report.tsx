import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, RefreshCw, ShieldCheck } from "lucide-react";
import { api, type PRReview } from "@/api";
import {
  assessmentLabel,
  reviewActive,
  reviewLabel,
  reviewRefreshInterval,
} from "@/lib/pr-review-state";
import { ReviewLaunchButton } from "./run-pr-reviews";
import { ErrorAlert } from "./error-alert";
import { Markdown } from "./markdown";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
const severity = { critical: 0, high: 1, medium: 2, low: 3 };
const publication = {
  pending: "Waiting to publish",
  retrying: "Retrying",
  published: "Published",
  delivered: "Published",
  uncertain: "Result uncertain",
  skipped: "Not published",
  failed: "Needs attention",
};
function location(
  review: PRReview,
  finding: NonNullable<PRReview["report"]>["findings"][number],
) {
  const sha =
    finding.side === "base"
      ? review.merge_base_sha
      : review.snapshot.pr.head_sha;
  const repo = review.snapshot.pr.repository_name;
  if (
    !/^[0-9a-f]{40,64}$/.test(sha) ||
    !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repo)
  )
    return undefined;
  return `https://github.com/${repo}/blob/${sha}/${finding.path.split("/").map(encodeURIComponent).join("/")}#L${finding.start_line}-L${finding.end_line}`;
}
export function PRReviewReport({ reviewID }: { reviewID: string }) {
  const client = useQueryClient();
  const queryKey = ["pr-review", reviewID];
  const query = useQuery({
    queryKey,
    queryFn: () => api.prReview(reviewID),
    retry: false,
    refetchInterval: (q) => reviewRefreshInterval(q.state.data),
  });
  const refresh = useMutation({
    mutationFn: () => api.refreshPRReview(reviewID),
    onSuccess: (value) => client.setQueryData(queryKey, value),
  });
  const publish = useMutation({
    mutationFn: () => api.retryPRReviewPublication(reviewID),
    onSuccess: (value) => client.setQueryData(queryKey, value),
  });
  const value = query.data;
  if (!value)
    return (
      <Card>
        <CardContent className="pt-5">
          {query.error ? (
            <ErrorAlert>
              {query.error.message}
              <Button variant="link" onClick={() => void query.refetch()}>
                Try again
              </Button>
            </ErrorAlert>
          ) : (
            <p role="status">Loading PR review…</p>
          )}
        </CardContent>
      </Card>
    );
  const { snapshot, report, freshness } = value;
  const active = reviewActive(value.run_status);
  const assessment = active
    ? "pending"
    : value.run_status === "succeeded"
      ? value.assessment
      : "incomplete";
  const counts = report?.findings.reduce(
    (acc, finding) => {
      acc[finding.severity]++;
      return acc;
    },
    { critical: 0, high: 0, medium: 0, low: 0 },
  );
  return (
    <section
      aria-label="PR review report"
      className="min-w-0 space-y-5 [overflow-wrap:anywhere]"
    >
      <Card className="min-w-0">
        <CardHeader className="border-b">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="space-y-2">
              <CardTitle>
                <h2 className="flex items-center gap-2 text-lg">
                  <ShieldCheck
                    className="size-5 text-primary"
                    aria-hidden="true"
                  />
                  PR review
                </h2>
              </CardTitle>
              <p className="text-sm text-muted-foreground">
                {snapshot.reviewer.name} · {snapshot.reviewer.model} ·{" "}
                {snapshot.reviewer.reasoning_effort} reasoning
              </p>
            </div>
            <Badge
              variant={
                value.run_status === "failed" ? "destructive" : "outline"
              }
            >
              {reviewLabel(value)}
            </Badge>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="flex flex-wrap gap-3 text-xs">
            <Link
              className="underline underline-offset-4"
              to="/runs/$runId"
              params={{ runId: snapshot.source_run_id }}
            >
              Source coding run
            </Link>
            <a
              className="inline-flex items-center gap-1 underline underline-offset-4"
              href={snapshot.pr.url}
              target="_blank"
              rel="noreferrer"
            >
              Pull request #{snapshot.pr.number}
              <ExternalLink className="size-3" aria-hidden="true" />
            </a>
          </div>
          <div className="space-y-3">
            <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
              Assessment of the reviewed commit
            </p>
            <h3 className="text-xl font-semibold">
              {assessmentLabel(assessment)}
            </h3>
            {value.run_status !== "succeeded" && !active && report && (
              <p className="text-sm text-muted-foreground">
                Partial report — execution did not finish successfully. These
                findings are retained for inspection.
              </p>
            )}
            {report ? (
              <>
                <Markdown>{report.summary}</Markdown>
                {counts && (
                  <p className="text-sm text-muted-foreground">
                    {counts.critical + counts.high + counts.medium} blocking
                    findings · {counts.critical} critical · {counts.high} high ·{" "}
                    {counts.medium} medium · {counts.low} low
                  </p>
                )}
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                {active
                  ? "Waiting for the reviewer's structured report. Follow execution below."
                  : value.report_error ||
                    "A complete structured report was not retained."}
              </p>
            )}
            {report && value.report_error && (
              <ErrorAlert>{value.report_error}</ErrorAlert>
            )}
          </div>
          <div className="grid gap-4 rounded-lg border bg-muted/20 p-4 sm:grid-cols-2">
            <div className="space-y-2">
              <p className="text-xs font-medium text-muted-foreground">
                Current PR status
              </p>
              <p className="text-sm">
                {freshness.status === "outdated"
                  ? "Outdated — the PR changed after this review."
                  : freshness.status === "current"
                    ? `Reviewed commits still match · PR ${freshness.pr_state}`
                    : "Freshness unavailable — the current PR could not be verified."}
              </p>
              {freshness.error && (
                <p className="text-xs text-muted-foreground">
                  {freshness.error}
                </p>
              )}
              <p className="text-xs text-muted-foreground">
                {freshness.checked_at
                  ? `Last verified ${new Date(freshness.checked_at).toLocaleString()}`
                  : "No successful freshness check yet."}
              </p>
            </div>
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs [&>dt]:text-muted-foreground [&>dd]:min-w-0 [&>dd]:font-mono">
              <dt>Head</dt>
              <dd>{snapshot.pr.head_sha}</dd>
              <dt>Base</dt>
              <dd>{snapshot.pr.base_sha}</dd>
              <dt>Merge base</dt>
              <dd>{value.merge_base_sha || "Preparing…"}</dd>
              <dt>Coverage</dt>
              <dd>{report?.coverage ?? "Awaiting report"}</dd>
            </dl>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={refresh.isPending}
              onClick={() => refresh.mutate()}
            >
              <RefreshCw
                aria-hidden="true"
                className={refresh.isPending ? "animate-spin" : ""}
              />
              Refresh PR status
            </Button>
            {!active && (
              <>
                <ReviewLaunchButton
                  sourceRunID={snapshot.source_run_id}
                  label="Review latest commit"
                />
                <ReviewLaunchButton
                  sourceRunID={snapshot.source_run_id}
                  mode="again"
                  previousReviewID={value.id}
                  label="Review again"
                />
              </>
            )}
          </div>
          {refresh.error && <ErrorAlert>{refresh.error.message}</ErrorAlert>}
        </CardContent>
      </Card>
      {report && (
        <>
          <div className="space-y-3">
            <h3 className="text-base font-semibold">
              Findings{" "}
              <span className="text-muted-foreground">
                ({report.findings.length})
              </span>
            </h3>
            {[...report.findings]
              .sort((a, b) => severity[a.severity] - severity[b.severity])
              .map((finding, index) => (
                <Card
                  key={`${finding.path}:${finding.start_line}:${index}`}
                  className="min-w-0"
                >
                  <CardHeader className="border-b">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge
                        variant={
                          finding.severity === "critical" ||
                          finding.severity === "high"
                            ? "destructive"
                            : "outline"
                        }
                      >
                        {finding.severity}
                      </Badge>
                      <h4 className="text-sm font-semibold">{finding.title}</h4>
                    </div>
                    <a
                      className="text-xs font-mono text-muted-foreground underline underline-offset-4"
                      href={location(value, finding)}
                      target="_blank"
                      rel="noreferrer"
                    >
                      {finding.path}:{finding.start_line}–{finding.end_line} ·{" "}
                      {finding.side}
                    </a>
                  </CardHeader>
                  <CardContent className="space-y-4">
                    {[
                      ["Evidence", finding.evidence],
                      ["Consequence", finding.consequence],
                      ["Suggested fix", finding.suggested_fix],
                    ].map(([title, content]) => (
                      <div className="min-w-0 space-y-2" key={title}>
                        <h5 className="text-xs font-semibold text-muted-foreground">
                          {title}
                        </h5>
                        <Markdown>{content}</Markdown>
                      </div>
                    ))}
                  </CardContent>
                </Card>
              ))}
            {report.findings.length === 0 && (
              <p className="text-sm text-muted-foreground">
                {assessment === "incomplete"
                  ? "No findings were submitted; coverage is incomplete."
                  : "No findings were submitted for these commits."}
              </p>
            )}
          </div>
          <div className="grid min-w-0 gap-5 lg:grid-cols-2">
            <Card className="min-w-0">
              <CardHeader>
                <CardTitle>
                  <h3>Checks</h3>
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                {report.checks.length ? (
                  report.checks.map((check, i) => (
                    <div key={i} className="space-y-2 border-t pt-3">
                      <Badge variant="outline">
                        {check.outcome.replaceAll("_", " ")}
                      </Badge>
                      <Markdown>{check.method}</Markdown>
                      <Markdown>
                        {check.outcome === "not_run"
                          ? check.not_run_reason
                          : check.evidence}
                      </Markdown>
                    </div>
                  ))
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No checks recorded.
                  </p>
                )}
              </CardContent>
            </Card>
            <Card className="min-w-0">
              <CardHeader>
                <CardTitle>
                  <h3>Limitations</h3>
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                {report.limitations.length ? (
                  report.limitations.map((text, i) => (
                    <Markdown key={i}>{text}</Markdown>
                  ))
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No limitations reported.
                  </p>
                )}
              </CardContent>
            </Card>
          </div>
        </>
      )}
      <Card>
        <CardHeader>
          <CardTitle>
            <h3>Feedback publication</h3>
          </CardTitle>
        </CardHeader>
        <CardContent className="grid gap-5 sm:grid-cols-2">
          {(["github", "linear"] as const).map((provider) => (
            <div key={provider} className="min-w-0 space-y-3">
              <p className="text-sm font-semibold">
                {provider === "github" ? "GitHub" : "Linear"}{" "}
                <Badge variant="outline">
                  {publication[value[provider].status]}
                </Badge>
              </p>
              {value[provider].url && (
                <a
                  className="text-sm underline underline-offset-4"
                  href={value[provider].url}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open feedback
                </a>
              )}
              {value[provider].error && (
                <p className="text-xs text-muted-foreground">
                  {value[provider].error}
                </p>
              )}
              {provider === "github" && value.github.retryable && (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={publish.isPending}
                  onClick={() => publish.mutate()}
                >
                  {value.github.status === "uncertain"
                    ? "Check publication result"
                    : "Retry publication"}
                </Button>
              )}
            </div>
          ))}
          <p className="text-xs text-muted-foreground sm:col-span-2">
            Publication retries reuse this report. They do not run the reviewer
            again.
          </p>
          {publish.error && <ErrorAlert>{publish.error.message}</ErrorAlert>}
        </CardContent>
      </Card>
    </section>
  );
}
