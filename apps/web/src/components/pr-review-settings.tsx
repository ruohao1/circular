import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api } from "@/api";
import { AgentModelSettings } from "./agent-model-settings";
import { ErrorAlert } from "./error-alert";
import { ResourceSelect } from "./resource-select";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Label } from "./ui/label";
import { Switch } from "./ui/switch";

export function PRReviewSettings({ projectID }: { projectID: string }) {
  const client = useQueryClient();
  const queryKey = ["pr-review-settings", projectID];
  const settings = useQuery({
    queryKey,
    queryFn: () => api.prReviewSettings(projectID),
    retry: false,
  });
  const agents = useQuery({
    queryKey: ["agents", projectID],
    queryFn: () => api.agents(projectID),
  });
  const save = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (value: { automatic: boolean; reviewer_id?: string }) =>
      api.setPRReviewSettings(projectID, value),
    onSuccess: (data) => {
      client.setQueryData(queryKey, data);
      void client.invalidateQueries({ queryKey: ["pr-review-prepare"] });
    },
  });
  const state = settings.data;
  const selected = agents.data?.find((a) => a.id === state?.reviewer_id);
  const options = (agents.data ?? [])
    .filter((a) => a.backend === "codex" && a.enabled)
    .map((a) => ({ value: a.id, label: a.name, disabled: false }));
  if (state?.reviewer_id && !options.some((a) => a.value === state.reviewer_id))
    options.unshift({
      value: state.reviewer_id,
      label: selected
        ? `${selected.name} (disabled)`
        : "Selected reviewer unavailable",
      disabled: true,
    });
  return (
    <section
      aria-label="PR reviews"
      className="space-y-4 rounded-lg border bg-muted/20 p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">PR reviews</h3>
        <Badge variant="outline">
          {state?.automatic ? "Automatic reviews on" : "Automatic reviews off"}
        </Badge>
      </div>
      <ResourceSelect
        id={`reviewer-${projectID}`}
        label="PR reviewer"
        value={state?.reviewer_id ?? ""}
        placeholder="Choose a reviewer"
        options={options}
        disabled={!state || save.isPending}
        onValueChange={(id) =>
          save.mutate({ automatic: state?.automatic ?? false, reviewer_id: id })
        }
      />
      {selected?.enabled && selected.backend === "codex" && (
        <AgentModelSettings key={selected.id} agent={selected} />
      )}
      {state && !state.available && (
        <div className="space-y-2 text-xs text-muted-foreground">
          <p>
            {state.unavailable_reason ||
              "Choose an enabled Codex reviewer and check GitHub access."}
          </p>
          <Button variant="link" size="sm" className="h-auto p-0" asChild>
            <Link to="/setup" search={{ section: "agents" }}>
              Manage reviewers
            </Link>
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void settings.refetch()}
            disabled={settings.isFetching}
          >
            Refresh review permissions
          </Button>
        </div>
      )}
      <div className="flex items-start justify-between gap-4 border-t pt-4">
        <div className="min-w-0 space-y-2">
          <Label htmlFor={`automatic-reviews-${projectID}`}>
            Automatically review published PRs
          </Label>
          <p
            id={`review-help-${projectID}`}
            className="text-xs leading-relaxed text-muted-foreground"
          >
            Start an additional reviewer run after Circular publishes a new pull
            request. Existing pull requests are not reviewed automatically.
            Turning this off cancels automatic reviews that are still queued; a
            review already running can finish.
          </p>
        </div>
        <Switch
          id={`automatic-reviews-${projectID}`}
          checked={state?.automatic ?? false}
          aria-describedby={`review-help-${projectID}`}
          disabled={
            !state || save.isPending || (!state.available && !state.automatic)
          }
          onCheckedChange={(automatic) => save.mutate({ automatic })}
        />
      </div>
      {(settings.error || agents.error || save.error) && (
        <ErrorAlert>
          {(save.error || settings.error || agents.error)?.message}
        </ErrorAlert>
      )}
      {save.isSuccess && (
        <p role="status" className="text-xs text-success">
          Review settings saved.
        </p>
      )}
      {settings.isPending && (
        <p className="text-xs text-muted-foreground">
          Loading review settings…
        </p>
      )}
    </section>
  );
}
