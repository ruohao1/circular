import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Link2 } from "lucide-react";
import { api, type Task } from "@/api";
import { useProject } from "@/use-project";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

function linearIssueURL(value: unknown) {
  if (typeof value !== "string") return "";
  try {
    const url = new URL(value);
    return url.protocol === "https:" &&
      url.hostname === "linear.app" &&
      !url.username &&
      !url.password
      ? url.href
      : "";
  } catch {
    return "";
  }
}

const deliveryLabels = {
  not_linked: "Not linked",
  disabled: "Updates off",
  pending: "Waiting to publish",
  retrying: "Retrying",
  uncertain: "Needs confirmation",
  delivered: "Published",
  failed: "Could not publish",
  skipped: "Not published",
  reconnect_required: "Reconnect required",
};

const deliveryDescriptions = {
  not_linked: "",
  disabled: "Turn on publishing in Integrations to share future run activity.",
  pending: "Circular will post the latest run update shortly.",
  retrying:
    "The update could not be sent. Circular will try again automatically.",
  uncertain:
    "Circular could not confirm the original comment. It will not post another copy.",
  delivered: "The latest run update was posted to Linear.",
  failed: "Check your Linear connection in Integrations.",
  skipped:
    "This update was not published. Check publishing settings in Integrations.",
  reconnect_required: "Reconnect Linear in Integrations to allow comments.",
};

export function RunLinearDelivery({
  runID,
  task,
  review = false,
}: {
  runID: string;
  task: Task;
  review?: boolean;
}) {
  const { selectProject } = useProject();
  const reference = task.external_refs?.linear;
  const linked =
    !!reference &&
    typeof reference === "object" &&
    "issue_id" in reference &&
    typeof reference.issue_id === "string";
  const delivery = useQuery({
    queryKey: ["run-linear-delivery", runID],
    queryFn: () => api.runLinearDelivery(runID),
    enabled: linked,
    refetchInterval: 15_000,
    retry: false,
  });
  const state = delivery.data;
  if (!linked || state?.status === "not_linked") return null;
  const issueURL =
    linearIssueURL(state?.issue_url) ||
    linearIssueURL(
      reference && typeof reference === "object" && "url" in reference
        ? reference.url
        : "",
    );
  const needsSetup =
    state &&
    ["disabled", "failed", "skipped", "reconnect_required"].includes(
      state.status,
    );

  return (
    <Card role="region" aria-label="Linear issue">
      <CardHeader className="border-b">
        <CardTitle>
          <h2 className="flex items-center gap-2">
            <Link2
              className="size-4 text-muted-foreground"
              aria-hidden="true"
            />
            Linear issue
          </h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {issueURL && (
          <Button variant="link" size="sm" asChild className="h-auto p-0">
            <a href={issueURL} target="_blank" rel="noreferrer">
              Open Linear issue <ExternalLink aria-hidden="true" />
            </a>
          </Button>
        )}
        {delivery.isPending && (
          <Skeleton className="h-5 w-32" aria-label="Loading Linear update" />
        )}
        {state && (
          <div role="status" aria-live="polite" className="space-y-2">
            <Badge
              variant={
                state.status === "failed"
                  ? "destructive"
                  : state.status === "delivered"
                    ? "secondary"
                    : "outline"
              }
            >
              {deliveryLabels[state.status]}
            </Badge>
            <p className="text-xs leading-relaxed text-muted-foreground">
              {state.request_id
                ? "Progress and results appear in Circular’s native Linear agent session."
                : review && state.status === "delivered"
                  ? "The review summary was posted to Linear."
                  : review && state.status === "pending"
                    ? "Waiting for the reviewer to finish and publish its summary."
                    : deliveryDescriptions[state.status]}
            </p>
            {state.last_delivered_at && (
              <p className="text-xs text-muted-foreground">
                Last posted{" "}
                <time dateTime={state.last_delivered_at}>
                  {new Date(state.last_delivered_at).toLocaleString()}
                </time>
              </p>
            )}
          </div>
        )}
        {(delivery.error || state?.error) && (
          <ErrorAlert>{delivery.error?.message || state?.error}</ErrorAlert>
        )}
        {delivery.error && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => void delivery.refetch()}
            disabled={delivery.isFetching}
          >
            Try again
          </Button>
        )}
        {state?.request_id && (
          <Button variant="outline" size="sm" asChild>
            <Link
              to="/requests/$requestID"
              params={{ requestID: state.request_id }}
            >
              Open Linear request
            </Link>
          </Button>
        )}
        {needsSetup && (
          <Button variant="outline" size="sm" asChild>
            <Link
              to="/setup"
              search={{ section: "integrations" }}
              onClick={() => selectProject(task.project_id)}
            >
              Linear settings
            </Link>
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
