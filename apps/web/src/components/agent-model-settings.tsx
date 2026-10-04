import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type Agent } from "@/api";
import {
  CodexModelFields,
  modelSettings,
  modelSummary,
  useCodexModels,
} from "@/components/codex-model-fields";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";

export function AgentModelSettings({ agent }: { agent: Agent }) {
  const client = useQueryClient();
  const catalog = useCodexModels();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(() => modelSettings(agent.backend_config));
  const save = useMutation({
    mutationKey: ["setup", "agent-model", agent.id],
    mutationFn: () => api.updateAgent(agent.id, { backend_config: draft }),
    onSuccess: (updated) => {
      client.setQueryData<Agent[]>(
        ["agents", agent.project_id],
        (current = []) =>
          current.map((item) => (item.id === updated.id ? updated : item)),
      );
      void client.invalidateQueries({ queryKey: ["agents", agent.project_id] });
      void client.invalidateQueries({
        queryKey: ["pr-review-settings", agent.project_id],
      });
      void client.invalidateQueries({ queryKey: ["pr-review-prepare"] });
      setEditing(false);
    },
  });
  return (
    <div className="space-y-3" aria-label={`Model settings for ${agent.name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="min-w-0 break-words text-muted-foreground">
          {catalog.data
            ? modelSummary(catalog.data, modelSettings(agent.backend_config))
            : "Model settings"}
        </span>
        {!editing && (
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Edit model for ${agent.name}`}
            onClick={() => {
              setDraft(modelSettings(agent.backend_config));
              save.reset();
              setEditing(true);
            }}
          >
            Edit model
          </Button>
        )}
      </div>
      {editing && (
        <form
          aria-label={`Edit model for ${agent.name}`}
          className="space-y-4 rounded-md border bg-muted/20 p-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (catalog.data && draft.model !== "" && !save.isPending)
              save.mutate();
          }}
        >
          {catalog.data ? (
            <CodexModelFields
              id={`agent-${agent.id}`}
              catalog={catalog.data}
              value={draft}
              onChange={setDraft}
              disabled={save.isPending}
            />
          ) : catalog.error ? (
            <ErrorAlert>
              Could not load model choices.
              <Button
                type="button"
                variant="link"
                onClick={() => void catalog.refetch()}
              >
                Retry
              </Button>
            </ErrorAlert>
          ) : (
            <p className="text-xs text-muted-foreground">
              Loading model choices…
            </p>
          )}
          {save.error && <ErrorAlert>{save.error.message}</ErrorAlert>}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={!catalog.data || draft.model === "" || save.isPending}
            >
              {save.isPending ? "Saving…" : "Save model"}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={save.isPending}
              onClick={() => setEditing(false)}
            >
              Cancel
            </Button>
          </div>
        </form>
      )}
      {save.isSuccess && !editing && (
        <p role="status" className="text-xs text-success">
          Model settings saved.
        </p>
      )}
    </div>
  );
}
