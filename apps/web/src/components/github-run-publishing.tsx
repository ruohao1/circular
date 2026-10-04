import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, RefreshCw } from "lucide-react";
import { api } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

export function GitHubRunPublishing({
  project,
  installationURL,
}: {
  project: string;
  installationURL: string | null;
}) {
  const client = useQueryClient();
  const queryKey = ["integration", project, "github", "run-delivery"];
  const settings = useQuery({
    queryKey,
    queryFn: () => api.githubRunDeliverySettings(project),
    refetchInterval: 15_000,
    retry: false,
  });
  const update = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (enabled: boolean) =>
      api.setGitHubRunDeliverySettings(project, enabled),
    onSuccess: (data) => {
      client.setQueryData(queryKey, data);
      void client.invalidateQueries({ queryKey: ["run-github-delivery"] });
    },
    onError: () => void settings.refetch(),
  });
  const state = settings.data;

  return (
    <section
      aria-label="GitHub pull request publishing"
      className="space-y-4 rounded-lg border bg-muted/20 p-4"
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-2">
          <Label htmlFor="github-run-publishing" className="leading-snug">
            Automatically open draft pull requests
          </Label>
          <p
            id="github-run-publishing-help"
            className="text-xs leading-relaxed text-muted-foreground"
          >
            After a successful run with changes, Circular publishes a dedicated
            branch and opens a draft pull request for you to review. Pull
            requests are never merged automatically.
          </p>
        </div>
        <Switch
          id="github-run-publishing"
          checked={state?.enabled ?? false}
          onCheckedChange={(enabled) => update.mutate(enabled)}
          disabled={
            !state || update.isPending || (!state.authorized && !state.enabled)
          }
          aria-describedby="github-run-publishing-help"
        />
      </div>
      {settings.isPending && (
        <Skeleton
          className="h-4 w-44"
          aria-label="Loading pull request settings"
        />
      )}
      {state && !state.authorized && (
        <div className="space-y-3 text-xs leading-relaxed text-muted-foreground">
          <p className="font-medium text-foreground">
            Allow pull request publishing
          </p>
          <p>
            In your GitHub App settings, set{" "}
            <strong>Repository permissions → Contents</strong> and{" "}
            <strong>Pull requests</strong> to <strong>Read and write</strong>.
            Approve the updated installation permissions, then refresh here.
          </p>
          {state.permission_message && <p>{state.permission_message}</p>}
          <div className="flex flex-wrap gap-2">
            {installationURL && (
              <Button size="sm" variant="outline" asChild>
                <a href={installationURL} target="_blank" rel="noreferrer">
                  Review GitHub app access <ExternalLink aria-hidden="true" />
                </a>
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={settings.isFetching || update.isPending}
              onClick={() => void settings.refetch()}
            >
              <RefreshCw
                aria-hidden="true"
                className={settings.isFetching ? "animate-spin" : ""}
              />
              Refresh publishing permissions
            </Button>
          </div>
        </div>
      )}
      {state && (
        <div role="status" aria-live="polite" className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={
                state.enabled && state.authorized ? "secondary" : "outline"
              }
            >
              {update.isPending
                ? "Saving…"
                : state.enabled
                  ? state.authorized
                    ? "Automatic drafts on"
                    : "Authorization needed"
                  : "Automatic drafts off"}
            </Badge>
            {state.pending_count > 0 && (
              <span className="text-xs text-muted-foreground">
                {state.pending_count} waiting to publish
              </span>
            )}
            {state.failed_count > 0 && (
              <span className="text-xs text-destructive">
                {state.failed_count} needing attention
              </span>
            )}
          </div>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Applies to future successful runs in this project. You can also
            create a draft pull request from an individual run. Turning this off
            stops future automatic publishing. Work already in progress may
            finish; existing branches and pull requests stay on GitHub.
          </p>
        </div>
      )}
      {(settings.error || update.error) && (
        <ErrorAlert>{(update.error || settings.error)?.message}</ErrorAlert>
      )}
      {settings.error && (
        <Button
          variant="outline"
          size="sm"
          disabled={settings.isFetching}
          onClick={() => void settings.refetch()}
        >
          Try again
        </Button>
      )}
      {state?.last_error && <ErrorAlert>{state.last_error}</ErrorAlert>}
    </section>
  );
}
