import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ExternalLink, GitPullRequestDraft, LoaderCircle } from "lucide-react";
import { api, type Run, type Task } from "@/api";
import { useProject } from "@/use-project";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

const labels = {
  not_ready: "Not available",
  not_requested: "Ready to publish",
  pending: "Waiting to publish",
  retrying: "Retrying",
  delivered: "Draft pull request ready",
  failed: "Needs attention",
  uncertain: "Checking the result",
  no_changes: "No changes to publish",
};

const descriptions = {
  not_ready:
    "A successful run needs captured changes and a connected GitHub repository before Circular can publish it.",
  not_requested:
    "Publish these changes to a dedicated branch and open a draft pull request. Review the Changes tab before publishing.",
  pending:
    "Circular is preparing the branch and draft pull request. You can leave this page while it finishes.",
  retrying:
    "A temporary problem interrupted publishing. Circular will try again automatically.",
  delivered:
    "Review the changes on GitHub, then mark the draft ready when you are satisfied. Circular does not merge it automatically.",
  failed:
    "Read the error below and resolve the problem before retrying this run's publishing request.",
  uncertain:
    "GitHub may have received the request. Check again to reconcile this run's existing branch and pull request.",
  no_changes:
    "The run did not produce file changes, so a pull request is not needed.",
};

function pullRequestURL(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === "https:" &&
      url.hostname === "github.com" &&
      !url.username &&
      !url.password &&
      /^\/[^/]+\/[^/]+\/pull\/\d+$/.test(url.pathname)
      ? url.href
      : "";
  } catch {
    return "";
  }
}

export function RunGitHubDelivery({
  runID,
  task,
  runStatus,
}: {
  runID: string;
  task: Task;
  runStatus: Run["status"];
}) {
  const client = useQueryClient();
  const { selectProject } = useProject();
  const queryKey = ["run-github-delivery", runID];
  const delivery = useQuery({
    queryKey,
    queryFn: () => api.runGitHubDelivery(runID),
    enabled: runStatus === "succeeded",
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === "pending" || status === "retrying" ? 3_000 : false;
    },
    retry: false,
  });
  const publish = useMutation({
    mutationFn: () => api.publishRunGitHub(runID),
    onSuccess: (data) => {
      client.setQueryData(queryKey, data);
      void client.invalidateQueries({
        queryKey: ["integration", task.project_id, "github", "run-delivery"],
      });
      void client.invalidateQueries({
        queryKey: ["run-linear-delivery", runID],
      });
    },
    onError: () => void delivery.refetch(),
  });
  if (runStatus !== "succeeded") return null;
  const state = delivery.data;
  const url = pullRequestURL(state?.pull_request_url ?? "");
  const canPublish = state?.status === "not_requested";
  const canRetry =
    !!state &&
    (state.status === "failed" || state.status === "retrying") &&
    state.retryable;
  const checkAgain = state?.status === "uncertain";
  const requestError =
    state &&
    ["pending", "retrying", "delivered", "no_changes"].includes(state.status)
      ? undefined
      : publish.error;

  return (
    <Card role="region" aria-label="GitHub pull request">
      <CardHeader className="border-b">
        <CardTitle>
          <h2 className="flex items-center gap-2">
            <GitPullRequestDraft
              className="size-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
            GitHub pull request
          </h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {delivery.isPending && (
          <Skeleton
            className="h-5 w-32"
            aria-label="Loading pull request status"
          />
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
              {labels[state.status]}
            </Badge>
            <p className="text-xs leading-relaxed text-muted-foreground">
              {descriptions[state.status]}
            </p>
          </div>
        )}
        {url && (
          <Button variant="outline" size="sm" asChild className="w-full">
            <a href={url} target="_blank" rel="noreferrer">
              Open pull request{state?.number ? ` #${state.number}` : ""}
              <ExternalLink aria-hidden="true" />
            </a>
          </Button>
        )}
        {state?.branch && (
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs [&>dt]:text-muted-foreground [&>dd]:min-w-0 [&>dd]:[overflow-wrap:anywhere]">
            <dt>Branch</dt>
            <dd className="font-mono">{state.branch}</dd>
            {state.base_branch && (
              <>
                <dt>Into</dt>
                <dd className="font-mono">{state.base_branch}</dd>
              </>
            )}
          </dl>
        )}
        {(delivery.error || requestError || state?.error) && (
          <ErrorAlert>
            {requestError?.message || delivery.error?.message || state?.error}
          </ErrorAlert>
        )}
        {(canPublish || canRetry || checkAgain) && (
          <Button
            className="w-full"
            size="sm"
            variant={canPublish ? "default" : "outline"}
            disabled={publish.isPending}
            onClick={() => publish.mutate()}
          >
            {publish.isPending && (
              <LoaderCircle className="animate-spin" aria-hidden="true" />
            )}
            {publish.isPending
              ? canPublish
                ? "Queuing…"
                : "Checking…"
              : canPublish
                ? "Create draft pull request"
                : checkAgain
                  ? "Check again"
                  : "Retry publishing"}
          </Button>
        )}
        {delivery.error && (
          <Button
            variant="outline"
            size="sm"
            disabled={delivery.isFetching}
            onClick={() => void delivery.refetch()}
          >
            Try again
          </Button>
        )}
        {state &&
          [
            "not_requested",
            "not_ready",
            "failed",
            "retrying",
            "uncertain",
          ].includes(state.status) && (
            <Button variant="link" size="sm" className="h-auto p-0" asChild>
              <Link
                to="/setup"
                search={{ section: "integrations" }}
                onClick={() => selectProject(task.project_id)}
              >
                GitHub publishing settings
              </Link>
            </Button>
          )}
      </CardContent>
    </Card>
  );
}
