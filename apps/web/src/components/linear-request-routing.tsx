import { useState } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  api,
  type IntegrationIdentityStatus,
  type LinearRequestRoute,
} from "@/api";
import { ResourceSelect } from "@/components/resource-select";
import { ErrorAlert } from "@/components/error-alert";
import { IntegrationWebhooks } from "@/components/integration-webhooks";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  modelSettings,
  modelSummary,
  useCodexModels,
} from "@/components/codex-model-fields";

export function LinearRequestRouting({
  project,
  identity,
}: {
  project: string;
  identity: IntegrationIdentityStatus;
}) {
  const client = useQueryClient();
  const queryKey = ["linear-request-routes", project];
  const canInspect = identity.mode === "app";
  const routes = useQuery({
    queryKey,
    queryFn: () => api.requestRoutes(project),
    enabled: canInspect,
    retry: false,
  });
  const reception = useQuery({
    queryKey: ["integration-webhooks", "linear"],
    queryFn: () => api.webhookSettings("linear"),
    enabled: canInspect,
    retry: false,
  });
  const [editing, setEditing] = useState<LinearRequestRoute>();
  const [open, setOpen] = useState(false);
  const [scopeType, setScopeType] = useState<"team" | "project">("project");
  const [scope, setScope] = useState("");
  const [repo, setRepo] = useState("");
  const [agent, setAgent] = useState("");
  const [mode, setMode] = useState<"automatic" | "approval">("automatic");
  const capable = [
    "read",
    "comments:create",
    "app:mentionable",
    "app:assignable",
  ].every((s) => identity.capabilities.includes(s));
  const ready =
    identity.status === "enabled" &&
    capable &&
    reception.data?.status === "receiving";
  const repos = useQuery({
    queryKey: ["repositories", project],
    queryFn: () => api.repositories(project),
    enabled: open,
  });
  const agents = useQuery({
    queryKey: ["agents", project],
    queryFn: () => api.agents(project),
    enabled: open,
  });
  const models = useCodexModels();
  const scopes = useInfiniteQuery({
    queryKey: ["linear-request-scopes", project, scopeType],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      scopeType === "project"
        ? api.linearProjects(project, pageParam)
        : api.linearTeams(project, pageParam),
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: open && capable,
    retry: false,
  });
  const upgrade = useMutation({
    mutationFn: () => api.connectLinearIdentity(project, "agent"),
    onSuccess: (auth) => window.location.assign(auth.authorization_url),
  });
  const save = useMutation({
    mutationFn: (value: {
      enabled: boolean;
      existing?: LinearRequestRoute;
    }) => {
      const r = value.existing || editing;
      if (r)
        return api.updateRequestRoute(project, r.id, {
          repository_id: value.existing ? r.repository_id : repo,
          agent_id: value.existing ? r.agent_id : agent,
          mode: value.existing ? r.mode : mode,
          enabled: value.enabled,
          expected_generation: r.generation,
        });
      return api.saveRequestRoute(project, {
        identity_id: identity.identity_id,
        scope_type: scopeType,
        scope_id: scope,
        repository_id: repo,
        agent_id: agent,
        mode,
        enabled: value.enabled,
      });
    },
    onSuccess: async () => {
      setOpen(false);
      setEditing(undefined);
      await client.invalidateQueries({ queryKey });
    },
    onError: () => client.invalidateQueries({ queryKey }),
  });
  const selected = agents.data?.find((a) => a.id === agent);
  const startEdit = (r?: LinearRequestRoute) => {
    save.reset();
    setEditing(r);
    setScopeType(r?.scope_type || "project");
    setScope(r?.scope_id || "");
    setRepo(r?.repository_id || "");
    setAgent(r?.agent_id || "");
    setMode(r?.mode || "automatic");
    setOpen(true);
  };
  const error =
    routes.error ||
    reception.error ||
    save.error ||
    upgrade.error ||
    repos.error ||
    agents.error ||
    scopes.error;
  const hasEnabledRequests = routes.data?.some((r) => r.enabled);
  const requestStatus = !canInspect
    ? "Off"
    : routes.isPending || reception.isPending
      ? "Checking…"
      : routes.error || reception.error
        ? "Needs attention"
        : hasEnabledRequests
          ? ready
            ? "Enabled"
            : identity.status === "disabled"
              ? "Paused"
              : "Needs attention"
          : "Off";
  return (
    <section
      aria-label="Linear request routing"
      className="space-y-4 border-t pt-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">Work from Linear</h3>
        <Badge variant="outline">{requestStatus}</Badge>
      </div>
      <p className="text-sm text-muted-foreground">
        Mention Circular or delegate an issue to give it work. You choose the
        repository, coding agent and when runs start. The human assignee stays
        responsible.
      </p>
      {!canInspect ? (
        <p className="text-sm text-muted-foreground">
          Enable the Circular bot above, then complete these setup steps.
          Connecting Linear alone does not start work.
        </p>
      ) : (
        <div className="space-y-5">
          <div className="space-y-2">
            <h4 className="text-sm font-medium">
              1. Allow mentions and delegation
            </h4>
            {capable ? (
              <p className="text-sm text-muted-foreground">
                Allowed for this workspace.
              </p>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  Approve this permission in Linear to let people call on
                  Circular.
                </p>
                <Button
                  size="sm"
                  disabled={upgrade.isPending || identity.status !== "enabled"}
                  onClick={() => upgrade.mutate()}
                >
                  Allow mentions and delegation
                </Button>
              </>
            )}
          </div>
          {identity.status === "enabled" ? (
            <IntegrationWebhooks
              provider="linear"
              title="2. Verify incoming events"
            />
          ) : (
            <div className="space-y-2">
              <h4 className="text-sm font-medium">2. Verify incoming events</h4>
              <p className="text-sm text-muted-foreground">
                Resume or reconnect the bot above to check reception.
              </p>
            </div>
          )}
          <div className="space-y-3 border-t pt-4">
            <h4 className="text-sm font-medium">3. Choose where work goes</h4>
            {!ready && (
              <p role="status" className="text-sm text-muted-foreground">
                Complete bot authorization and verify an incoming delivery
                before enabling requests.
              </p>
            )}
            <ul className="space-y-3">
              {routes.data?.map((r) => (
                <li key={r.id} className="space-y-2 rounded-lg border p-3">
                  <p className="break-all text-sm">
                    {r.scope_name || `Linear ${r.scope_type} · ${r.scope_id}`}
                  </p>
                  <p className="break-words text-sm text-muted-foreground">
                    {r.repository_name || "Repository unavailable"} ·{" "}
                    {r.agent_name || "Agent unavailable"}
                  </p>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge>
                      {!r.enabled
                        ? "Paused"
                        : r.mode === "approval"
                          ? "Approval required"
                          : "Automatic"}
                    </Badge>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => startEdit(r)}
                    >
                      Edit destination
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={save.isPending || (!r.enabled && !ready)}
                      onClick={() =>
                        save.mutate({ enabled: !r.enabled, existing: r })
                      }
                    >
                      {r.enabled ? "Pause requests" : "Enable requests"}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
            {open ? (
              <form
                className="space-y-4 rounded-lg border p-4"
                onSubmit={(e) => {
                  e.preventDefault();
                  save.mutate({ enabled: true });
                }}
              >
                <ResourceSelect
                  id="request-scope-type"
                  label="Match issues in"
                  value={scopeType}
                  onValueChange={(v) => {
                    setScopeType(v as "project" | "team");
                    setScope("");
                  }}
                  options={[
                    { value: "project", label: "A Linear project" },
                    { value: "team", label: "A Linear team" },
                  ]}
                  placeholder="Select a scope"
                  disabled={!!editing}
                />
                <ResourceSelect
                  id="request-scope"
                  label={
                    scopeType === "project" ? "Linear project" : "Linear team"
                  }
                  value={scope}
                  onValueChange={setScope}
                  options={
                    scopes.data?.pages
                      .flatMap((p) => p.items)
                      .map((s) => ({ value: s.id, label: s.name })) || []
                  }
                  placeholder={
                    scopes.isPending ? "Loading scopes…" : "Select a scope"
                  }
                  disabled={!!editing}
                  required
                />
                {scopes.hasNextPage && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    disabled={scopes.isFetchingNextPage}
                    onClick={() => void scopes.fetchNextPage()}
                  >
                    Load more scopes
                  </Button>
                )}
                <ResourceSelect
                  id="request-repository"
                  label="Request repository"
                  value={repo}
                  onValueChange={setRepo}
                  options={
                    repos.data?.map((r) => ({ value: r.id, label: r.name })) ||
                    []
                  }
                  placeholder="Add a repository to this project first"
                  required
                />
                <ResourceSelect
                  id="request-agent"
                  label="Coding agent"
                  value={agent}
                  onValueChange={setAgent}
                  options={
                    agents.data
                      ?.filter((a) => a.enabled)
                      .map((a) => ({ value: a.id, label: a.name })) || []
                  }
                  placeholder="Choose an enabled agent"
                  required
                />
                {selected && (
                  <p className="text-sm text-muted-foreground">
                    {selected.backend === "codex" && models.data
                      ? modelSummary(
                          models.data,
                          modelSettings(selected.backend_config),
                        )
                      : selected.backend}{" "}
                    ·{" "}
                    <Link
                      to="/setup"
                      search={{ section: "agents" }}
                      className="underline"
                    >
                      Change agent settings
                    </Link>
                  </p>
                )}
                <ResourceSelect
                  id="request-mode"
                  label="Run mode"
                  value={mode}
                  onValueChange={(v) => setMode(v as "automatic" | "approval")}
                  options={[
                    { value: "approval", label: "Ask in Circular first" },
                    { value: "automatic", label: "Start automatically" },
                  ]}
                  placeholder="Choose a run mode"
                />
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {mode === "automatic"
                    ? "New matching issue sessions can start model work immediately."
                    : "Review the request and model in Circular before starting."}{" "}
                  Draft PRs and automatic reviews follow this project's separate
                  publishing settings. Settings for a Linear project take
                  precedence over its team, including when paused.
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    disabled={
                      !ready || !scope || !repo || !agent || save.isPending
                    }
                  >
                    {save.isPending
                      ? "Checking destination…"
                      : editing
                        ? "Save and enable requests"
                        : "Enable Linear requests"}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => setOpen(false)}
                    disabled={save.isPending}
                  >
                    Cancel
                  </Button>
                </div>
              </form>
            ) : (
              <Button
                size="sm"
                variant="outline"
                disabled={!ready}
                onClick={() => startEdit()}
              >
                Add destination
              </Button>
            )}
          </div>
        </div>
      )}
      {error && <ErrorAlert>{error.message}</ErrorAlert>}
      <Link to="/requests" className="block text-sm text-primary underline">
        View incoming requests
      </Link>
    </section>
  );
}
