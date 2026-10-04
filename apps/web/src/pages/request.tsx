import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { api } from "@/api";
import { useProject } from "@/use-project";
import { requestStatus } from "@/components/external-request-list";
import { Markdown } from "@/components/markdown";
import { ResourceSelect } from "@/components/resource-select";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
export function RequestPage() {
  const { requestID } = useParams({ from: "/requests/$requestID" });
  const client = useQueryClient();
  const { selectedProject, selectProject } = useProject();
  const [route, setRoute] = useState("");
  const key = ["external-request", requestID];
  const query = useQuery({
    queryKey: key,
    queryFn: () => api.externalRequest(requestID),
    retry: false,
    refetchInterval: (q) => {
      const d = q.state.data;
      return d &&
        (![
          "succeeded",
          "failed",
          "stopped",
          "unsupported",
          "rejected",
        ].includes(d.status) ||
          ["pending", "uncertain"].includes(d.delivery_status))
        ? 5_000
        : false;
    },
  });
  const q = query.data;
  const project = q?.project_id || selectedProject;
  const routes = useQuery({
    queryKey: ["linear-request-routes", project],
    queryFn: () => api.requestRoutes(project),
    enabled: !!project && q?.status === "needs_routing",
    retry: false,
  });
  const act = useMutation({
    mutationFn: (action: "start" | "stop" | "route") =>
      action === "stop"
        ? api.stopExternalRequest(requestID)
        : action === "route"
          ? api.routeExternalRequest(requestID, route, q!.input_fingerprint)
          : api.startExternalRequest(requestID, q!.input_fingerprint),
    onSettled: async () => {
      await client.invalidateQueries({ queryKey: key });
      await client.invalidateQueries({ queryKey: ["external-requests"] });
    },
  });
  if (!q)
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-semibold">Linear request</h1>
        {query.error ? (
          <ErrorAlert>{query.error.message}</ErrorAlert>
        ) : (
          <p role="status">Loading request…</p>
        )}
        <Link to="/requests" className="text-primary underline">
          Back to requests
        </Link>
      </div>
    );
  const canStart =
    !q.run_id &&
    [
      "ready",
      "awaiting_approval",
      "waiting_for_active_run",
      "needs_access",
    ].includes(q.status);
  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <Link to="/requests" className="text-sm text-primary underline">
        ← Incoming requests
      </Link>
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="min-w-0 break-words text-2xl font-semibold">
            {q.title || "Linear request"}
          </h1>
          <Badge variant="outline">{requestStatus(q.status)}</Badge>
        </div>
        <p className="text-sm text-muted-foreground">
          Requested by {q.requester_name || "an unverified requester"}
        </p>
        {q.source_url && (
          <a
            href={q.source_url}
            target="_blank"
            rel="noreferrer"
            className="text-sm text-primary underline"
          >
            Open Linear issue ↗
          </a>
        )}
        {q.reason && (
          <p role="status" className="break-words text-sm">
            {q.reason}
          </p>
        )}
      </div>
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>Execution</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {q.agent_id ? (
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-muted-foreground">Repository</dt>
                <dd className="break-words">
                  {q.repository_name || q.repository_id}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Agent</dt>
                <dd>{q.agent_name || q.agent_id}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">Model and reasoning</dt>
                <dd>
                  {q.model || "Unavailable"}
                  {q.reasoning_effort && ` · ${q.reasoning_effort}`}
                </dd>
              </div>
            </dl>
          ) : (
            <p className="text-sm text-muted-foreground">
              Choose a matching route in the selected project. Routing prepares
              a preview for approval.
            </p>
          )}
          {q.status === "needs_routing" && (
            <div className="space-y-3">
              <ResourceSelect
                id="incoming-route"
                label="Destination route"
                value={route}
                onValueChange={setRoute}
                options={
                  routes.data
                    ?.filter(
                      (r) => r.enabled && r.identity_id === q.identity_id,
                    )
                    .map((r) => ({
                      value: r.id,
                      label: `${r.scope_name || r.scope_type} → ${r.repository_name || "Repository"} · ${r.agent_name || r.mode}`,
                    })) || []
                }
                placeholder="Configure a matching route in Integrations"
              />
              <Button
                size="sm"
                disabled={!route || act.isPending}
                onClick={() => act.mutate("route")}
              >
                Prepare request
              </Button>
            </div>
          )}
          {canStart && (
            <p className="text-xs text-muted-foreground">
              Starting uses these reviewed inputs and this project's publishing
              settings. Changed settings require a new review.
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {q.run_id && (
              <Button asChild>
                <Link to="/runs/$runId" params={{ runId: q.run_id }}>
                  Open run
                </Link>
              </Button>
            )}
            {q.pull_request_url && (
              <Button variant="outline" asChild>
                <a href={q.pull_request_url} target="_blank" rel="noreferrer">
                  Open pull request ↗
                </a>
              </Button>
            )}
            {canStart && (
              <Button
                disabled={act.isPending || !q.agent_name}
                onClick={() => act.mutate("start")}
              >
                Start reviewed request
              </Button>
            )}
            {q.status !== "stopped" && (
              <Button
                variant="outline"
                disabled={act.isPending}
                onClick={() => act.mutate("stop")}
              >
                Stop request
              </Button>
            )}
            <Button
              variant="ghost"
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              Refresh preview
            </Button>
          </div>
          {(act.error || query.error || routes.error) && (
            <ErrorAlert>
              {act.error?.message ||
                query.error?.message ||
                routes.error?.message}
            </ErrorAlert>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>Received instructions</h2>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Markdown>
            {q.prompt || "No issue instructions were received."}
          </Markdown>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>
            <h2>Linear delivery</h2>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm">
            {q.delivery_status === "delivered"
              ? "Latest session update delivered"
              : q.delivery_status === "uncertain"
                ? "Checking the original activity receipt"
                : q.delivery_status === "failed"
                  ? "Delivery needs attention"
                  : "Waiting to deliver a session update"}
          </p>
          {q.delivery_reason && (
            <p className="text-sm text-muted-foreground">{q.delivery_reason}</p>
          )}
          <Link
            to="/setup"
            search={{ section: "integrations" }}
            onClick={() => {
              if (project) selectProject(project);
            }}
            className="text-sm text-primary underline"
          >
            Linear connection and routing settings
          </Link>
        </CardContent>
      </Card>
      {!!q.messages.length && (
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Follow-up messages</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Saved messages do not change the current run. Start a new Linear
              session for new instructions.
            </p>
            {q.messages.map((m) => (
              <div key={m.id} className="border-t pt-4">
                <Markdown>{m.body}</Markdown>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
