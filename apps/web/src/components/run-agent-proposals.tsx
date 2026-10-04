import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Bot, Check, LoaderCircle } from "lucide-react";
import { api, type AgentProposal, type AgentProposalCreate } from "@/api";
import { agentRecommendations } from "@/lib/agent-recommendations";
import { useProject } from "@/use-project";
import {
  CodexModelFields,
  modelSettings,
  modelSummary,
  resolvedModelSettings,
  useCodexModels,
} from "@/components/codex-model-fields";
import { ErrorAlert } from "@/components/error-alert";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

type Suggestion = { proposal?: AgentProposal; input: AgentProposalCreate };

function ProposalEditor({
  proposal,
  runID,
  projectName,
  onCreated,
}: {
  proposal: AgentProposal;
  runID: string;
  projectName: string;
  onCreated: () => void;
}) {
  const catalog = useCodexModels();
  const [name, setName] = useState(proposal.name);
  const [instructions, setInstructions] = useState(proposal.instructions);
  const [settings, setSettings] = useState(
    modelSettings(proposal.backend_config),
  );
  const [modelEditorVersion, setModelEditorVersion] = useState(0);
  const recommended = modelSettings(proposal.backend_config);
  const selected = catalog.data
    ? resolvedModelSettings(catalog.data, settings)
    : settings;
  const changedRecommendation =
    selected.model !== recommended.model ||
    selected.reasoning_effort !== recommended.reasoning_effort;
  const [preview, setPreview] = useState(false);
  const create = useMutation({
    mutationFn: () =>
      api.createProposedAgent(runID, proposal.id, {
        name: name.trim(),
        purpose: proposal.purpose,
        instructions: instructions.trim(),
        ...resolvedModelSettings(catalog.data!, settings),
      }),
    onSuccess: onCreated,
  });
  function submit(event: FormEvent) {
    event.preventDefault();
    if (!catalog.data || create.isPending) return;
    create.mutate();
  }
  return (
    <form onSubmit={submit} className="flex max-h-[90svh] flex-col">
      <div className="shrink-0 space-y-2 p-5 pr-12 sm:p-7 sm:pr-12">
        <DialogTitle className="text-xl font-semibold tracking-tight">
          Review suggested agent
        </DialogTitle>
        <DialogDescription className="text-sm text-muted-foreground">
          Customize this agent before adding it to {projectName}.
        </DialogDescription>
      </div>
      <div className="min-h-0 space-y-5 overflow-y-auto px-5 pb-5 sm:px-7">
        <div className="rounded-lg border bg-muted/20 p-4 text-sm leading-6 text-muted-foreground whitespace-pre-line">
          {proposal.purpose}
        </div>
        <div className="grid gap-2">
          <Label htmlFor="proposal-name">Agent name</Label>
          <Input
            id="proposal-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            maxLength={200}
            required
            disabled={create.isPending}
          />
        </div>
        {catalog.data ? (
          <CodexModelFields
            key={modelEditorVersion}
            id="proposal"
            catalog={catalog.data}
            value={settings}
            onChange={setSettings}
            disabled={create.isPending}
          />
        ) : catalog.error ? (
          <ErrorAlert>
            Model choices could not be loaded.{" "}
            <Button
              type="button"
              variant="link"
              onClick={() => void catalog.refetch()}
            >
              Try again
            </Button>
          </ErrorAlert>
        ) : (
          <p role="status" className="text-sm text-muted-foreground">
            Loading model choices…
          </p>
        )}
        {proposal.model_reason && catalog.data && (
          <div
            className="space-y-2 rounded-lg border border-primary/20 bg-primary/5 p-4"
            aria-label="Model recommendation"
          >
            <p className="text-xs font-medium text-primary">
              Recommended: {modelSummary(catalog.data, recommended)}
            </p>
            <p className="text-sm leading-6 text-muted-foreground whitespace-pre-line [overflow-wrap:anywhere]">
              {proposal.model_reason}
            </p>
            {changedRecommendation ? (
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-primary/15 pt-2">
                <p className="text-xs text-muted-foreground">
                  Your model and variant will be used.
                </p>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={create.isPending}
                  onClick={() => {
                    setSettings(recommended);
                    setModelEditorVersion((version) => version + 1);
                    requestAnimationFrame(() =>
                      document.getElementById("proposal-model")?.focus(),
                    );
                  }}
                >
                  Use recommendation
                </Button>
              </div>
            ) : (
              <p className="text-xs text-muted-foreground">
                Selected by the orchestrating agent. You can change either
                choice.
              </p>
            )}
          </div>
        )}
        <div className="space-y-2">
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor="proposal-instructions">Instructions</Label>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setPreview(!preview)}
            >
              {preview ? "Edit instructions" : "Preview instructions"}
            </Button>
          </div>
          {preview ? (
            <div className="max-h-72 overflow-y-auto rounded-lg border p-4">
              <Markdown>{instructions}</Markdown>
            </div>
          ) : (
            <Textarea
              id="proposal-instructions"
              value={instructions}
              onChange={(event) => setInstructions(event.target.value)}
              required
              maxLength={20000}
              disabled={create.isPending}
              className="min-h-56 max-h-80 resize-y font-mono text-xs leading-6"
            />
          )}
          <p className="text-xs text-muted-foreground">
            These instructions will guide every task assigned to this agent.
          </p>
        </div>
        {create.error && <ErrorAlert>{create.error.message}</ErrorAlert>}
      </div>
      <div className="flex shrink-0 flex-col gap-3 border-t p-5 sm:flex-row sm:items-center sm:justify-between sm:px-7">
        <p className="text-xs text-muted-foreground">
          The agent will be available in New task.
        </p>
        <Button
          type="submit"
          disabled={
            !catalog.data ||
            create.isPending ||
            !name.trim() ||
            !instructions.trim() ||
            settings.model === ""
          }
        >
          {create.isPending ? (
            <LoaderCircle className="animate-spin" aria-hidden="true" />
          ) : (
            <Bot aria-hidden="true" />
          )}
          {create.isPending ? "Creating…" : "Create agent"}
        </Button>
      </div>
    </form>
  );
}

export function RunAgentProposals({
  runID,
  projectID,
  report,
  finished,
}: {
  runID: string;
  projectID: string;
  report: string;
  finished: boolean;
}) {
  const client = useQueryClient();
  const heading = useRef<HTMLHeadingElement>(null);
  const { projects, selectProject } = useProject();
  const projectName =
    projects.data?.find((item) => item.id === projectID)?.name ??
    "this project";
  const catalog = useCodexModels();
  const key = ["agent-proposals", runID];
  const proposals = useQuery({
    queryKey: key,
    queryFn: () => api.agentProposals(runID),
    refetchInterval: finished ? false : 1000,
  });
  const agents = useQuery({
    queryKey: ["agents", projectID],
    queryFn: () => api.agents(projectID),
    enabled: proposals.data?.some((item) => item.status === "created") ?? false,
  });
  useEffect(() => {
    if (finished) void proposals.refetch();
  }, [finished, proposals.refetch]);
  const legacy = useMemo(
    () => (finished ? agentRecommendations(report) : []),
    [report, finished],
  );
  const [editing, setEditing] = useState<AgentProposal>();
  const [showDismissed, setShowDismissed] = useState(false);
  const suggestions: Suggestion[] = [
    ...(proposals.data ?? []).map((proposal) => ({
      proposal,
      input: {
        name: proposal.name,
        purpose: proposal.purpose,
        instructions: proposal.instructions,
        model_reason: proposal.model_reason,
        ...modelSettings(proposal.backend_config),
      },
    })),
    ...legacy
      .filter(
        (input) =>
          !proposals.data?.some((proposal) => proposal.name === input.name),
      )
      .map((input) => ({ input })),
  ];
  const dismissed = suggestions.filter(
    (item) => item.proposal?.status === "dismissed",
  ).length;
  const visible = suggestions.filter(
    (item) => showDismissed || item.proposal?.status !== "dismissed",
  );
  const prepare = useMutation({
    mutationFn: async ({
      suggestion,
      dismiss,
    }: {
      suggestion: Suggestion;
      dismiss: boolean;
    }) => {
      const proposal =
        suggestion.proposal ??
        (await api.saveAgentProposal(runID, suggestion.input));
      if (dismiss)
        return {
          proposal: await api.dismissAgentProposal(runID, proposal.id),
          dismiss,
        };
      return { proposal, dismiss };
    },
    onSuccess: ({ proposal, dismiss }) => {
      client.setQueryData<AgentProposal[]>(key, (previous) => [
        ...(previous ?? []).filter((item) => item.id !== proposal.id),
        proposal,
      ]);
      if (!dismiss && proposal.status === "pending") setEditing(proposal);
      void client.invalidateQueries({ queryKey: key });
    },
  });
  if (!suggestions.length && !proposals.error) return null;
  return (
    <section aria-label="Suggested agents" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2
            ref={heading}
            tabIndex={-1}
            className="flex items-center gap-2 text-base font-semibold outline-none"
          >
            <Bot className="size-4 text-primary" aria-hidden="true" />
            Suggested agents
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Recommendations from this run. Review and customize before creating.
          </p>
        </div>
        {dismissed > 0 && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setShowDismissed(!showDismissed)}
          >
            {showDismissed ? "Hide dismissed" : `Show dismissed (${dismissed})`}
          </Button>
        )}
      </div>
      {proposals.error && (
        <ErrorAlert>
          Could not load saved suggestions.{" "}
          <Button variant="link" onClick={() => void proposals.refetch()}>
            Try again
          </Button>
        </ErrorAlert>
      )}
      {prepare.error && <ErrorAlert>{prepare.error.message}</ErrorAlert>}
      <div className="grid gap-3 md:grid-cols-2">
        {visible.map((suggestion) => {
          const { proposal, input } = suggestion;
          const status = proposal?.status ?? "pending";
          const createdAgent =
            status === "created"
              ? agents.data?.find((item) => item.id === proposal?.agent_id)
              : undefined;
          return (
            <Card
              key={proposal?.id ?? input.name}
              className="min-w-0 gap-3 p-4 sm:p-5"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="font-medium [overflow-wrap:anywhere]">
                  {createdAgent?.name ?? input.name}
                </h3>
                {status === "created" ? (
                  <Badge variant="secondary" className="gap-1 text-success">
                    <Check className="size-3" aria-hidden="true" />
                    Created
                  </Badge>
                ) : status === "dismissed" ? (
                  <Badge variant="outline">Dismissed</Badge>
                ) : (
                  <Badge variant="outline">Suggested</Badge>
                )}
              </div>
              <p className="line-clamp-3 text-sm leading-6 text-muted-foreground whitespace-pre-line">
                {input.purpose}
              </p>
              {input.model_reason && (
                <details className="text-xs text-muted-foreground">
                  <summary className="cursor-pointer font-medium">
                    Why this model and variant
                  </summary>
                  <p className="mt-2 leading-6 whitespace-pre-line [overflow-wrap:anywhere]">
                    {catalog.data && (
                      <span className="font-medium text-foreground">
                        {modelSummary(catalog.data, input)}.{" "}
                      </span>
                    )}
                    {input.model_reason}
                  </p>
                </details>
              )}
              <div className="mt-auto flex flex-wrap items-center justify-between gap-2 pt-2">
                <span className="text-xs text-muted-foreground">
                  {catalog.data
                    ? modelSummary(
                        catalog.data,
                        createdAgent
                          ? modelSettings(createdAgent.backend_config)
                          : input,
                      )
                    : "Codex"}
                </span>
                {status === "pending" ? (
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={
                        prepare.isPending ||
                        proposals.isPending ||
                        !!proposals.error
                      }
                      onClick={() =>
                        prepare.mutate({ suggestion, dismiss: true })
                      }
                    >
                      Dismiss
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={
                        prepare.isPending ||
                        proposals.isPending ||
                        !!proposals.error
                      }
                      onClick={() =>
                        prepare.mutate({ suggestion, dismiss: false })
                      }
                    >
                      Review & create
                      <ArrowUpRight aria-hidden="true" />
                    </Button>
                  </div>
                ) : status === "created" ? (
                  <Button variant="link" size="sm" asChild>
                    <Link
                      to="/setup"
                      search={{ section: "agents" }}
                      onClick={() => selectProject(projectID)}
                    >
                      View agents
                      <ArrowUpRight aria-hidden="true" />
                    </Link>
                  </Button>
                ) : null}
              </div>
            </Card>
          );
        })}
      </div>
      <Dialog
        open={!!editing}
        onOpenChange={(open) => {
          if (!open) setEditing(undefined);
        }}
      >
        {editing && (
          <DialogContent
            className="overflow-hidden p-0 sm:p-0"
            onCloseAutoFocus={(event) => {
              event.preventDefault();
              heading.current?.focus();
            }}
          >
            <ProposalEditor
              key={editing.id}
              proposal={editing}
              runID={runID}
              projectName={projectName}
              onCreated={() => {
                setEditing(undefined);
                void client.invalidateQueries({ queryKey: key });
                void client.invalidateQueries({
                  queryKey: ["agents", projectID],
                });
              }}
            />
          </DialogContent>
        )}
      </Dialog>
    </section>
  );
}
