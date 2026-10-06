import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import { api } from "@/api";
import { requestStatus } from "@/components/external-request-list";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

export function RequestAttention({
  projectId,
  unrouted = false,
}: {
  projectId: string;
  unrouted?: boolean;
}) {
  const enabled = unrouted || !!projectId;
  const rows = useQuery({
    queryKey: ["external-requests", projectId, unrouted, "attention", 5],
    queryFn: () =>
      api.externalRequests(projectId, unrouted, "", {
        attention: true,
        limit: 5,
      }),
    enabled,
    retry: false,
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
  });
  const title = unrouted
    ? "Unrouted requests · all Projects"
    : "Project requests";
  return (
    <Card className="min-w-0" role="region" aria-label={title}>
      <CardHeader>
        <CardTitle>
          <h2 className="text-sm leading-relaxed">{title}</h2>
        </CardTitle>
        <p className="text-xs leading-relaxed text-muted-foreground">
          {unrouted
            ? "Incoming work that needs a destination in this Circular installation."
            : "Requests needing routing, approval, or access."}
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        {rows.error && (
          <ErrorAlert>
            {rows.data && <span>Showing the last update. </span>}
            {rows.error.message}{" "}
            <Button variant="link" onClick={() => void rows.refetch()}>
              Retry
            </Button>
          </ErrorAlert>
        )}
        {!enabled ? (
          <p className="text-sm text-muted-foreground">
            Choose a Project to review its requests.
          </p>
        ) : rows.isPending ? (
          <div
            role="status"
            aria-label="Loading requests"
            className="space-y-3"
          >
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-2/3" />
          </div>
        ) : rows.data?.items.length ? (
          <ul className="divide-y">
            {rows.data.items.map((item) => (
              <li key={item.id} className="space-y-2 py-3 first:pt-0">
                <Link
                  to="/requests/$requestID"
                  params={{ requestID: item.id }}
                  className="block rounded-sm text-sm font-medium leading-relaxed break-words hover:text-primary focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {item.title || "Linear request"}
                </Link>
                <Badge variant="outline">{requestStatus(item.status)}</Badge>
                <p className="text-xs leading-relaxed break-words text-muted-foreground">
                  {item.reason ||
                    `Requested by ${item.requester_name || "an unverified requester"}`}
                </p>
              </li>
            ))}
          </ul>
        ) : (
          !rows.error && (
            <p className="text-sm text-muted-foreground">
              No requests need attention.
            </p>
          )
        )}
        <Button asChild variant="link" className="h-auto p-0 text-xs">
          <Link to="/requests">
            All requests <ArrowUpRight aria-hidden="true" />
          </Link>
        </Button>
      </CardContent>
    </Card>
  );
}
