import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, LoaderCircle } from "lucide-react";
import { api } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

export function LinearRunUpdates({
  project,
  reconnect,
  connecting,
}: {
  project: string;
  reconnect: () => void;
  connecting: boolean;
}) {
  const client = useQueryClient();
  const queryKey = ["integration", project, "linear", "run-updates"];
  const settings = useQuery({
    queryKey,
    queryFn: () => api.linearRunUpdates(project),
    refetchInterval: 15_000,
    retry: false,
  });
  const update = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (enabled: boolean) => api.setLinearRunUpdates(project, enabled),
    onSuccess: (data) => {
      client.setQueryData(queryKey, data);
      void client.invalidateQueries({ queryKey: ["run-linear-delivery"] });
    },
    onError: () => {
      void settings.refetch();
    },
  });
  const state = settings.data;

  return (
    <section
      aria-label="Linear run updates"
      className="space-y-4 rounded-lg border bg-muted/20 p-4"
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 space-y-2">
          <Label htmlFor="linear-run-updates" className="leading-snug">
            Publish run updates
          </Label>
          <p
            id="linear-run-updates-help"
            className="text-xs leading-relaxed text-muted-foreground"
          >
            Post comments on imported issues when a run starts and finishes,
            including a link to the run, a result summary, and any reported
            GitHub pull requests.
          </p>
        </div>
        <Switch
          id="linear-run-updates"
          checked={state?.enabled ?? false}
          onCheckedChange={(enabled) => update.mutate(enabled)}
          disabled={
            !state ||
            update.isPending ||
            connecting ||
            (!state.authorized && !state.enabled)
          }
          aria-describedby="linear-run-updates-help"
        />
      </div>
      {settings.isPending && (
        <Skeleton
          className="h-4 w-44"
          aria-label="Loading publishing settings"
        />
      )}
      {state && !state.authorized && (
        <div className="space-y-3">
          <p className="text-xs leading-relaxed text-muted-foreground">
            Reconnect Linear to allow Circular to post comments.{" "}
            {state.enabled
              ? "Pending updates will resume after you reconnect."
              : "Then turn on publishing for this project."}
          </p>
          <Button
            size="sm"
            variant="outline"
            disabled={connecting || update.isPending}
            onClick={reconnect}
          >
            {connecting ? (
              <LoaderCircle className="animate-spin" aria-hidden="true" />
            ) : (
              <ExternalLink aria-hidden="true" />
            )}
            Allow Linear comments
          </Button>
        </div>
      )}
      {state && (
        <div className="space-y-2" role="status" aria-live="polite">
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
                    ? "Publishing on"
                    : "Authorization needed"
                  : "Publishing off"}
            </Badge>
            {state.pending_count > 0 && (
              <span className="text-xs text-muted-foreground">
                {state.pending_count}{" "}
                {state.pending_count === 1
                  ? "update waiting"
                  : "updates waiting"}
              </span>
            )}
            {state.failed_count > 0 && (
              <span className="text-xs text-destructive">
                {state.failed_count}{" "}
                {state.failed_count === 1
                  ? "update needs attention"
                  : "updates need attention"}
              </span>
            )}
          </div>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Applies to future run activity. Comments already posted stay in
            Linear.
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
