import { Link } from "@tanstack/react-router";
import { ArrowRight, LoaderCircle } from "lucide-react";
import type { Agent, Project, Repository, Task, TaskPriority } from "@/api";
import {
  priorityLabels,
  taskPriorities,
  stageLabels,
  taskStages,
} from "@/lib/task-planning";
import { TaskScheduleFields } from "@/components/tasks/task-schedule-fields";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

export type DraftFields = {
  title: string;
  description: string;
  repositoryId: string;
  agentId: string;
  priority: TaskPriority;
  boardStage?: Task["status"];
  planned_start_date: string;
  planned_finish_date: string;
};
export type LaunchDraft = DraftFields & {
  projectId: string;
  imported?: Task;
  partial?: { taskId: string; agent: Agent; repository: Repository };
  error?: string;
  savedTaskId?: string;
  repositoryInitialized?: boolean;
  agentInitialized?: boolean;
  agentExplicitlySelected?: boolean;
};
export type TaskLauncherDialogProps = {
  open: boolean;
  pending: boolean;
  loading: boolean;
  canLaunch: boolean;
  canSave: boolean;
  saving: boolean;
  project?: Project;
  draft?: LaunchDraft;
  repositories: Repository[];
  agents: Agent[];
  error?: string;
  optionsError?: string;
  discovery: boolean;
  onDraftChange(patch: Partial<DraftFields>): void;
  onOpenChange(open: boolean): void;
  onSubmit(): void;
  onSave(): void;
  onRetry?(): void;
  onStartAnother(): void;
  onSetup(section: "repositories" | "agents"): void;
  onCloseAutoFocus(event: Event): void;
};
export function TaskLauncherDialog(p: TaskLauncherDialogProps) {
  const { draft, pending } = p;
  const planningOnly = !!draft?.boardStage;
  const readonly = !!draft?.imported || !!draft?.partial;
  const repository = draft?.partial?.repository;
  const agent = draft?.partial?.agent;
  const repositories = repository ? [repository] : p.repositories;
  const agents = agent ? [agent] : p.agents.filter((a) => a.enabled);
  const selectedAgent = agent ?? agents.find((a) => a.id === draft?.agentId);
  const sourceManaged = !!draft?.imported?.external_refs?.linear;
  const unavailable =
    !p.loading &&
    !p.optionsError &&
    !!draft &&
    (!repositories.length || !agents.length);
  return (
    <Dialog open={p.open} onOpenChange={p.onOpenChange}>
      <DialogContent
        onCloseAutoFocus={p.onCloseAutoFocus}
        onEscapeKeyDown={(e) => {
          if (pending) e.preventDefault();
        }}
        onInteractOutside={(e) => {
          if (pending) e.preventDefault();
        }}
      >
        <div className="mb-6 space-y-2 pr-8">
          <DialogTitle className="text-xl font-semibold tracking-tight">
            {p.discovery
              ? "Repository discovery"
              : draft?.imported
                ? sourceManaged
                  ? "Imported Task"
                  : "Saved Task"
                : "New Task"}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            {p.discovery
              ? "Get a project brief, priorities, and recommended agents from the repository."
              : planningOnly
                ? "Add work to the board. Dates are optional."
                : "Save work for later, or start an Agent in an isolated Workspace."}
          </DialogDescription>
          {p.project && (
            <p className="text-xs text-muted-foreground">
              Project{" "}
              <span className="ml-2 font-medium text-foreground">
                {p.project.name}
              </span>
            </p>
          )}
        </div>
        <form
          className="space-y-5"
          onSubmit={(e) => {
            e.preventDefault();
            if (planningOnly) p.onSave();
            else p.onSubmit();
          }}
        >
          <fieldset
            className="grid min-w-0 gap-5"
            disabled={pending || !!draft?.partial || p.loading || !draft}
          >
            <div className="grid min-w-0 gap-2">
              <Label htmlFor="task-title">Task title</Label>
              <Input
                id="task-title"
                value={draft?.title ?? ""}
                onChange={(e) => p.onDraftChange({ title: e.target.value })}
                readOnly={readonly}
                required
                maxLength={1000}
                placeholder="What would you like to work on?"
              />
            </div>
            {draft?.boardStage && (
              <ResourceSelect
                id="new-task-stage"
                label="Workflow stage"
                value={draft.boardStage}
                onValueChange={(value) => {
                  const stage = taskStages.find((stage) => stage === value);
                  if (stage) p.onDraftChange({ boardStage: stage });
                }}
                options={taskStages.map((value) => ({
                  value,
                  label: stageLabels[value],
                }))}
                placeholder="Workflow stage"
                disabled={pending}
              />
            )}
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <ResourceSelect
                id="repository"
                label="Repository"
                value={draft?.repositoryId || "none"}
                onValueChange={(repositoryId) => {
                  if (repositoryId)
                    p.onDraftChange({
                      repositoryId: repositoryId === "none" ? "" : repositoryId,
                    });
                }}
                options={[
                  { value: "none", label: "No Repository" },
                  ...repositories.map((r) => ({
                    value: r.id,
                    label: r.name,
                  })),
                ]}
                placeholder="No repositories"
                disabled={readonly || pending}
              />
              <ResourceSelect
                id="agent"
                label="Agent"
                value={draft?.agentId || "none"}
                onValueChange={(agentId) => {
                  if (agentId)
                    p.onDraftChange({
                      agentId: agentId === "none" ? "" : agentId,
                    });
                }}
                options={[
                  { value: "none", label: "No preferred Agent" },
                  ...(draft?.agentId &&
                  !agents.some((a) => a.id === draft.agentId)
                    ? [
                        {
                          value: draft.agentId,
                          label: "Unavailable preferred Agent · choose another",
                          disabled: true,
                        },
                      ]
                    : []),
                  ...agents.map((a) => ({
                    value: a.id,
                    label: `${a.name} · ${a.backend}`,
                  })),
                ]}
                placeholder="No enabled agents"
                disabled={pending || !!draft?.partial}
              />
            </div>
            {!readonly && (
              <ResourceSelect
                id="task-priority"
                label="Priority"
                value={draft?.priority ?? "none"}
                onValueChange={(value) =>
                  p.onDraftChange({
                    priority: taskPriorities.find((p) => p === value) ?? "none",
                  })
                }
                options={taskPriorities.map((value) => ({
                  value,
                  label: priorityLabels[value],
                }))}
                placeholder="No priority"
              />
            )}
            <div className="grid gap-2">
              <Label htmlFor="description">
                Description{" "}
                <span className="font-normal text-muted-foreground">
                  {planningOnly ? "(optional)" : "(required to start)"}
                </span>
              </Label>
              <Textarea
                id="description"
                aria-label="Description"
                value={draft?.description ?? ""}
                onChange={(e) =>
                  p.onDraftChange({ description: e.target.value })
                }
                readOnly={readonly}
                rows={4}
                className="min-h-28 resize-y"
                placeholder="Add context, constraints, or a definition of done."
              />
            </div>
            {planningOnly && draft && (
              <TaskScheduleFields draft={draft} onChange={p.onDraftChange} />
            )}
          </fieldset>
          {p.loading && (
            <p role="status" className="text-sm text-muted-foreground">
              Loading Task and launch options…
            </p>
          )}
          {unavailable && !planningOnly && (
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-muted/30 p-3">
              <p className="text-sm text-muted-foreground">
                {!repositories.length
                  ? "Add a repository to start working on this project."
                  : "Create an enabled agent to run tasks in this project."}
              </p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  p.onSetup(!repositories.length ? "repositories" : "agents")
                }
              >
                {!repositories.length ? "Add repository" : "Create agent"}
              </Button>
            </div>
          )}
          {p.optionsError && <ErrorAlert>{p.optionsError}</ErrorAlert>}
          {p.error && <ErrorAlert>{p.error}</ErrorAlert>}
          {p.onRetry && (
            <Button type="button" variant="outline" onClick={p.onRetry}>
              Retry
            </Button>
          )}
          {draft?.imported && (
            <p className="text-xs text-muted-foreground">
              This starts a Run for the saved Task. Its description and
              repository are preserved.
            </p>
          )}
          {draft?.savedTaskId && (
            <p className="text-xs text-muted-foreground">
              Your previous Task is saved.{" "}
              <Link
                to="/"
                search={{ taskId: draft.savedTaskId }}
                onClick={() => p.onOpenChange(false)}
                className="underline text-primary"
              >
                Resume its launch
              </Link>
            </p>
          )}
          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-5">
            {!planningOnly && (
              <span className="text-xs text-muted-foreground">
                {draft?.partial
                  ? `Task saved · ${draft.partial.taskId.slice(0, 8)}`
                  : selectedAgent
                    ? `${selectedAgent.backend === "fake" ? "Network disabled" : "Network enabled"} · Dedicated Workspace`
                    : "Select a Repository and Agent"}
              </span>
            )}
            <div className="ml-auto flex flex-wrap gap-2">
              {!readonly && (
                <Button
                  type={planningOnly ? "submit" : "button"}
                  variant={planningOnly ? "default" : "outline"}
                  disabled={pending || !p.canSave}
                  onClick={planningOnly ? undefined : p.onSave}
                >
                  {p.saving ? "Saving…" : "Save Task"}
                </Button>
              )}
              {draft?.partial && (
                <Button
                  type="button"
                  variant="ghost"
                  disabled={pending}
                  onClick={p.onStartAnother}
                >
                  Start another Task
                </Button>
              )}
              {!planningOnly && (
                <Button type="submit" disabled={pending || !p.canLaunch}>
                  {pending ? (
                    <LoaderCircle className="animate-spin" aria-hidden="true" />
                  ) : (
                    <ArrowRight aria-hidden="true" />
                  )}
                  {pending
                    ? "Starting…"
                    : draft?.partial
                      ? "Retry starting Run"
                      : "Start Run"}
                </Button>
              )}
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
