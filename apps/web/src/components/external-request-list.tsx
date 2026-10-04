import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api } from "@/api";
import { useProject } from "@/use-project";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
export const requestStatus = (status: string) =>
  ({
    needs_routing: "Needs a route",
    awaiting_approval: "Approval required",
    ready: "Ready",
    waiting_for_active_run: "Waiting for another run",
    queued: "Queued",
    running: "Running",
    succeeded: "Succeeded",
    failed: "Failed",
    stopped: "Stopped",
    unsupported: "Issue required",
    rejected: "Needs a smaller request",
    needs_access: "Access needed",
  })[status] || status;
function RequestList({
  project,
  unrouted = false,
}: {
  project: string;
  unrouted?: boolean;
}) {
  const rows = useInfiniteQuery({
    queryKey: ["external-requests", project, unrouted],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api.externalRequests(project, unrouted, pageParam),
    getNextPageParam: (p) => p.next_cursor || undefined,
    enabled: unrouted || !!project,
    refetchInterval: 15_000,
    retry: false,
  });
  const items = rows.data?.pages.flatMap((p) => p.items) || [];
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2>{unrouted ? "Unrouted requests" : "Project requests"}</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {unrouted && (
          <p className="text-sm text-muted-foreground">
            Requests that need a destination, across this Circular installation.
          </p>
        )}
        {rows.error && <ErrorAlert>{rows.error.message}</ErrorAlert>}
        {!rows.isPending && !items.length && (
          <p className="text-sm text-muted-foreground">
            {unrouted
              ? "No requests need routing."
              : "No requests for this project yet."}
          </p>
        )}
        {rows.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            Loading requests…
          </p>
        )}
        <ul className="divide-y">
          {items.map((q) => (
            <li key={q.id} className="space-y-2 py-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Link
                  to="/requests/$requestID"
                  params={{ requestID: q.id }}
                  className="break-words font-medium text-primary underline"
                >
                  {q.title || "Linear request"}
                </Link>
                <Badge variant="outline">{requestStatus(q.status)}</Badge>
              </div>
              <p className="break-words text-sm text-muted-foreground">
                {q.reason ||
                  `Requested by ${q.requester_name || "an unverified requester"}`}
              </p>
            </li>
          ))}
        </ul>
        {rows.hasNextPage && (
          <Button
            variant="outline"
            disabled={rows.isFetchingNextPage}
            onClick={() => void rows.fetchNextPage()}
          >
            Load more requests
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
export function RequestsPage() {
  const { selectedProject } = useProject();
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Incoming requests</h1>
        <p className="mt-2 text-muted-foreground">
          Review work delegated to Circular in Linear.
        </p>
      </div>
      {selectedProject && <RequestList project={selectedProject} />}
      <RequestList project="" unrouted />
    </div>
  );
}
