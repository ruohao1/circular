import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, type CodexModelCatalog } from "@/api";
import { ResourceSelect } from "@/components/resource-select";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export type ModelSettings = { model?: string; reasoning_effort?: string };

const effortLabels: Record<string, string> = {
  none: "None",
  minimal: "Minimal",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
  ultra: "Ultra",
};

export function useCodexModels() {
  return useQuery({
    queryKey: ["codex-models"],
    queryFn: api.codexModels,
    staleTime: Infinity,
  });
}

export function modelSettings(
  config: Record<string, unknown> = {},
): ModelSettings {
  return {
    ...(typeof config.model === "string" ? { model: config.model } : {}),
    ...(typeof config.reasoning_effort === "string"
      ? { reasoning_effort: config.reasoning_effort }
      : {}),
  };
}

export function resolvedModelSettings(
  catalog: CodexModelCatalog,
  value: ModelSettings,
): ModelSettings {
  const id = value.model ?? catalog.default_model;
  const model = catalog.models.find((item) => item.id === id);
  const effort = value.reasoning_effort ?? model?.default_reasoning_effort;
  return { model: id, ...(effort ? { reasoning_effort: effort } : {}) };
}

export function modelSummary(catalog: CodexModelCatalog, value: ModelSettings) {
  const { model: id, reasoning_effort: effort } = resolvedModelSettings(
    catalog,
    value,
  );
  const model = catalog.models.find((item) => item.id === id);
  return `${model?.name ?? id} · ${effort ? (effortLabels[effort] ?? effort) : "Default effort"}`;
}

export function CodexModelFields({
  id,
  catalog,
  value,
  onChange,
  disabled = false,
}: {
  id: string;
  catalog: CodexModelCatalog;
  value: ModelSettings;
  onChange: (value: ModelSettings) => void;
  disabled?: boolean;
}) {
  const [custom, setCustom] = useState(false);
  const modelID = value.model ?? catalog.default_model;
  const model = catalog.models.find((item) => item.id === modelID);
  const isCustom = custom || !model;
  const effort = value.reasoning_effort ?? model?.default_reasoning_effort;
  const efforts = model?.reasoning_efforts ?? Object.keys(effortLabels);
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <ResourceSelect
          id={`${id}-model`}
          label="Model"
          value={isCustom ? "__custom__" : modelID}
          onValueChange={(selected) => {
            if (!selected) return;
            setCustom(selected === "__custom__");
            if (selected === "__custom__") {
              onChange({ model: "" });
              return;
            }
            const next = catalog.models.find((item) => item.id === selected)!;
            onChange({
              model: selected,
              reasoning_effort:
                effort && next.reasoning_efforts.includes(effort)
                  ? effort
                  : next.default_reasoning_effort,
            });
          }}
          options={[
            ...catalog.models.map((item) => ({
              value: item.id,
              label: item.name,
            })),
            { value: "__custom__", label: "Custom model…" },
          ]}
          placeholder="Select a model"
          disabled={disabled}
          required
        />
        <ResourceSelect
          key={model?.id ?? "custom"}
          id={`${id}-effort`}
          label="Variant"
          value={effort ?? "__default__"}
          onValueChange={(selected) => {
            if (!selected) return;
            onChange({
              model: modelID,
              ...(selected === "__default__"
                ? {}
                : { reasoning_effort: selected }),
            });
          }}
          options={[
            ...(!model
              ? [{ value: "__default__", label: "Default for model" }]
              : []),
            ...efforts.map((item) => ({
              value: item,
              label: effortLabels[item] ?? item,
            })),
          ]}
          placeholder="Select reasoning effort"
          disabled={disabled}
          required
        />
      </div>
      {isCustom && (
        <div className="grid gap-2">
          <Label htmlFor={`${id}-custom-model`}>Custom model ID</Label>
          <Input
            id={`${id}-custom-model`}
            value={value.model ?? ""}
            onChange={(event) =>
              onChange({ ...value, model: event.target.value })
            }
            maxLength={200}
            spellCheck={false}
            autoCapitalize="none"
            disabled={disabled}
            required
            placeholder="Model identifier available to your account"
          />
        </div>
      )}
      <p className="text-xs leading-relaxed text-muted-foreground">
        Variant controls reasoning effort. Higher levels spend more time on
        complex tasks.
        {effort === "ultra" && " Ultra can also delegate work to other agents."}
      </p>
    </div>
  );
}
