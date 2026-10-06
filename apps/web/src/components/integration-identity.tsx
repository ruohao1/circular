import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Check, Pause } from "lucide-react";
import { api, type Connection } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { LinearRequestRouting } from "@/components/linear-request-routing";
import { IntegrationWebhooks } from "@/components/integration-webhooks";
import { GitHubAppKey } from "@/components/github-app-key";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

export function IntegrationIdentity({
  project,
  connection,
  onActive,
}: {
  project: string;
  connection: Connection;
  onActive: (active: boolean) => void;
}) {
  const { provider } = connection;
  const name = provider === "github" ? "GitHub" : "Linear";
  const client = useQueryClient();
  const [keyOpen, setKeyOpen] = useState(false);
  const queryKey = ["integration-identity", project, provider];
  const identity = useQuery({
    queryKey,
    queryFn: () => api.integrationIdentity(project, provider),
    enabled: connection.configured,
    retry: false,
  });
  const active =
    !identity.error &&
    identity.data?.mode === "app" &&
    identity.data.status === "enabled";
  useEffect(() => {
    onActive(active);
  }, [active, onActive]);
  const refresh = async () => {
    setKeyOpen(false);
    await client.invalidateQueries({ queryKey });
    await client.invalidateQueries({ queryKey: ["connections", project] });
    await client.invalidateQueries({ queryKey: ["integration", project] });
  };
  const setup = useMutation({
    mutationKey: ["setup", project],
    mutationFn: async () => {
      if (provider === "linear") {
        const auth = await api.connectLinearIdentity(project);
        window.location.assign(auth.authorization_url);
      } else await api.saveGitHubIdentity(project);
    },
    onSuccess: refresh,
  });
  const toggle = useMutation({
    mutationKey: ["setup", project],
    mutationFn: () =>
      active
        ? api.detachIntegrationIdentity(project, provider)
        : api.bindIntegrationIdentity(
            project,
            provider,
            identity.data!.identity_id,
          ),
    onSuccess: refresh,
  });
  if (!connection.configured) return null;
  // An unavailable or stale identity must never look like a switch to the user.
  const state = identity.error ? undefined : identity.data;
  const app = state?.mode === "app";
  const paused = app && state.status === "disabled";
  const verified = state && (app || connection.identity_mode !== "app");
  const author = verified
    ? app
      ? state.actor_name || state.actor_login || "Circular bot"
      : connection.status === "connected"
        ? `Your ${name} account`
        : "Connect to choose an author"
    : identity.isPending
      ? "Checking author…"
      : "Unable to check author";
  const pending = setup.isPending || toggle.isPending;
  return (
    <>
      <section
        aria-label={`${name} publishing identity`}
        className="space-y-4 border-t pt-5"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            <Bot className="size-4" aria-hidden="true" />
            {provider === "github" ? "PRs and reviews" : "Run updates"}
          </h3>
          {verified && app && (
            <Badge variant={active ? "secondary" : "outline"}>
              {active
                ? "Bot enabled"
                : paused
                  ? "Bot paused"
                  : "Bot needs attention"}
            </Badge>
          )}
        </div>
        <dl className="flex min-w-0 flex-wrap items-start justify-between gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Published as</dt>
          <dd
            className="flex min-w-0 items-center gap-2 font-medium"
            role="status"
          >
            {verified && app && state.avatar_url.startsWith("https://") && (
              <img
                className="size-6 shrink-0 rounded"
                src={state.avatar_url}
                alt=""
                referrerPolicy="no-referrer"
              />
            )}
            {identity.isPending ? (
              <Skeleton className="h-5 w-40" aria-label={author} />
            ) : (
              <span className="break-words">{author}</span>
            )}
          </dd>
        </dl>
        {verified && (
          <>
            {app ? (
              <>
                <p className="break-words text-xs text-muted-foreground">
                  {state.account_name}
                  {state.actor_login && ` · ${state.actor_login}`}
                </p>
                <p className="text-sm text-muted-foreground">
                  {active
                    ? "Choose which activity to publish using the controls below."
                    : paused
                      ? "Resume the bot to publish new activity. Your bot remains the selected author."
                      : "Reconnect the bot to restore publishing. Your bot remains the selected author."}
                </p>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                Enable the bot to publish with your app’s name and avatar. Your
                existing {name} connection stays connected.
              </p>
            )}
            {state.affected_projects.length > 1 && (
              <p className="text-xs text-muted-foreground">
                Shared with {state.affected_projects.length} projects:{" "}
                {state.affected_projects.join(", ")}. Pausing here affects only
                this project.
              </p>
            )}
            {state.reason && !paused && (
              <p className="text-sm text-muted-foreground">{state.reason}</p>
            )}
            {!keyOpen && (
              <div className="flex flex-wrap gap-2">
                {active || paused ? (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={pending}
                    onClick={() => toggle.mutate()}
                  >
                    {active ? (
                      <Pause aria-hidden="true" />
                    ) : (
                      <Check aria-hidden="true" />
                    )}
                    {active ? "Pause bot" : "Resume bot"}
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    disabled={
                      pending ||
                      (provider === "github" &&
                        state.environment_managed &&
                        state.status === "needs_setup")
                    }
                    onClick={() => {
                      setup.reset();
                      if (
                        provider === "github" &&
                        !state.environment_managed &&
                        state.status === "needs_setup"
                      )
                        setKeyOpen(true);
                      else setup.mutate();
                    }}
                  >
                    {pending
                      ? "Connecting…"
                      : app
                        ? "Reconnect bot"
                        : "Enable Circular bot"}
                  </Button>
                )}
                {provider === "github" && !state.environment_managed && app && (
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={pending}
                    onClick={() => {
                      setup.reset();
                      setKeyOpen(true);
                    }}
                  >
                    Update signing key
                  </Button>
                )}
              </div>
            )}
            {provider === "github" &&
              state.environment_managed &&
              state.status === "needs_setup" && (
                <p className="text-xs text-muted-foreground">
                  This installation manages the app’s signing key. Ask its
                  administrator to restore bot access.
                </p>
              )}
          </>
        )}
        {keyOpen && (
          <GitHubAppKey
            project={project}
            replacing={app}
            onSaved={() => void refresh()}
            onCancel={() => setKeyOpen(false)}
          />
        )}
        {(identity.error || setup.error || toggle.error) && (
          <ErrorAlert>
            {identity.error?.message ||
              setup.error?.message ||
              toggle.error?.message}
          </ErrorAlert>
        )}
        {!verified && !identity.isPending && (
          <Button
            size="sm"
            variant="outline"
            disabled={identity.isFetching}
            onClick={() => void refresh()}
          >
            Check again
          </Button>
        )}
        <a
          href="/docs/circular-identity"
          className="inline-block text-xs text-primary underline underline-offset-4"
        >
          How the Circular bot works
        </a>
        {active && provider === "github" && (
          <details className="text-sm">
            <summary className="cursor-pointer text-muted-foreground">
              Incoming events
            </summary>
            <div className="pt-3">
              <IntegrationWebhooks provider={provider} />
            </div>
          </details>
        )}
      </section>
      {provider === "linear" && verified && (
        <LinearRequestRouting
          key={project}
          project={project}
          identity={state}
        />
      )}
    </>
  );
}
