import { useState } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { api, ApiError, type PRReviewLaunch } from "@/api";
import {
  clearReviewIntent,
  pendingReviewIntent,
  reviewIntent,
  reviewStorage,
} from "@/lib/pr-review-intent";
import { reviewRefreshInterval, reviewLabel } from "@/lib/pr-review-state";
import { ErrorAlert } from "./error-alert";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "./ui/dialog";

export function ReviewLaunchButton({
  sourceRunID,
  mode = "normal",
  previousReviewID,
  label = "Review PR",
}: {
  sourceRunID: string;
  mode?: PRReviewLaunch["mode"];
  previousReviewID?: string;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  const storage = reviewStorage();
  const pending = pendingReviewIntent(storage, sourceRunID);
  const navigate = useNavigate();
  const client = useQueryClient();
  const preparation = useQuery({
    queryKey: ["pr-review-prepare", sourceRunID],
    queryFn: () => api.preparePRReview(sourceRunID),
    enabled: open && !pending,
    retry: false,
  });
  const launch = useMutation({
    mutationFn: async () => {
      const prepared = preparation.data;
      if (!pending && !prepared?.ready)
        throw new Error("Refresh the PR and reviewer before starting.");
      const intent =
        pending ??
        reviewIntent(storage, sourceRunID, {
          mode,
          ...(previousReviewID ? { previous_review_id: previousReviewID } : {}),
          reviewer_id: prepared!.snapshot.reviewer.agent_id,
          expected_input_fingerprint: prepared!.snapshot.input_fingerprint,
        });
      return api.launchPRReview(sourceRunID, intent);
    },
    onSuccess: (review) => {
      clearReviewIntent(storage, sourceRunID);
      void client.invalidateQueries({ queryKey: ["pr-reviews", sourceRunID] });
      setOpen(false);
      void navigate({ to: "/runs/$runId", params: { runId: review.run_id } });
    },
  });
  const snapshot = preparation.data?.snapshot;
  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={() => {
          launch.reset();
          setOpen(true);
        }}
      >
        {label}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg space-y-5">
          <DialogTitle className="pr-7 text-lg font-semibold">
            {pending ? "Check review request" : label}
          </DialogTitle>
          <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
            {pending
              ? "An earlier request may already have started. This checks the same request and reuses its reviewer and commits."
              : "This starts an additional agent run. Circular reviews the exact commits below and publishes eligible feedback to GitHub."}
          </DialogDescription>
          {!pending && preparation.data?.ready && snapshot && (
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-5 gap-y-3 text-sm [&>dt]:text-muted-foreground [&>dd]:min-w-0 [&>dd]:[overflow-wrap:anywhere]">
              <dt>Reviewer</dt>
              <dd>{snapshot.reviewer.name}</dd>
              <dt>Model</dt>
              <dd>{snapshot.reviewer.model}</dd>
              <dt>Reasoning</dt>
              <dd>{snapshot.reviewer.reasoning_effort}</dd>
              <dt>Pull request</dt>
              <dd>
                #{snapshot.pr.number} · {snapshot.pr.repository_name}
              </dd>
              <dt>Head</dt>
              <dd className="font-mono text-xs">{snapshot.pr.head_sha}</dd>
            </dl>
          )}
          {!pending && preparation.isFetching && (
            <p role="status" className="text-sm text-muted-foreground">
              Checking the PR and reviewer…
            </p>
          )}
          {!pending && preparation.data && !preparation.data.ready && (
            <ErrorAlert>{preparation.data.reason}</ErrorAlert>
          )}
          {(preparation.error || launch.error) && (
            <ErrorAlert>
              {(launch.error || preparation.error)?.message}
            </ErrorAlert>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={
                launch.isPending ||
                (!pending &&
                  (!preparation.data?.ready || preparation.isFetching))
              }
              onClick={() => launch.mutate()}
            >
              {launch.isPending
                ? "Checking…"
                : pending
                  ? "Check review request"
                  : "Start review"}
            </Button>
            {pending &&
              launch.error instanceof ApiError &&
              [409, 422].includes(launch.error.status) && (
                <Button
                  variant="outline"
                  onClick={() => {
                    clearReviewIntent(storage, sourceRunID);
                    launch.reset();
                    void preparation.refetch();
                  }}
                >
                  Refresh preparation
                </Button>
              )}
            {!pending && (
              <Button
                variant="outline"
                disabled={preparation.isFetching || launch.isPending}
                onClick={() => void preparation.refetch()}
              >
                Refresh preparation
              </Button>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
export function RunPRReviews({ sourceRunID }: { sourceRunID: string }) {
  const delivery = useQuery({
    queryKey: ["run-github-delivery", sourceRunID],
    queryFn: () => api.runGitHubDelivery(sourceRunID),
    retry: false,
  });
  const history = useInfiniteQuery({
    queryKey: ["pr-reviews", sourceRunID],
    queryFn: ({ pageParam }) => api.prReviews(sourceRunID, pageParam),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled: delivery.data?.status === "delivered",
    refetchInterval: (query) => {
      const reviews =
        query.state.data?.pages.flatMap((page) => page.items) ?? [];
      if (!reviews.length) return 60_000;
      const intervals = reviews
        .map(reviewRefreshInterval)
        .filter((value): value is number => value !== false);
      return intervals.length ? Math.min(...intervals) : false;
    },
  });
  if (delivery.data?.status !== "delivered") return null;
  const reviews = history.data?.pages.flatMap((page) => page.items) ?? [];
  return (
    <Card role="region" aria-label="Pull request reviews">
      <CardHeader className="border-b">
        <CardTitle>
          <h2>Pull request reviews</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-xs leading-relaxed text-muted-foreground">
          Get a dedicated agent's assessment of this pull request. Each report
          stays linked to the commits it reviewed.
        </p>
        <ReviewLaunchButton sourceRunID={sourceRunID} />
        {reviews.length > 0 && (
          <ul className="space-y-3">
            {reviews.map((review) => (
              <li key={review.id} className="space-y-1 border-t pt-3">
                <Link
                  className="text-sm font-medium underline-offset-4 hover:underline"
                  to="/runs/$runId"
                  params={{ runId: review.run_id }}
                >
                  Review {review.attempt} ·{" "}
                  {review.snapshot.pr.head_sha.slice(0, 8)}
                </Link>
                <div>
                  <Badge variant="outline">{reviewLabel(review)}</Badge>
                </div>
                <p className="break-words text-xs text-muted-foreground">
                  {review.snapshot.reviewer.name} ·{" "}
                  {review.snapshot.reviewer.model}
                </p>
              </li>
            ))}
          </ul>
        )}
        {history.error && <ErrorAlert>{history.error.message}</ErrorAlert>}
        {history.hasNextPage && (
          <Button
            variant="ghost"
            size="sm"
            disabled={history.isFetchingNextPage}
            onClick={() => void history.fetchNextPage()}
          >
            Older reviews
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
