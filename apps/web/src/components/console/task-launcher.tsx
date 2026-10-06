import { Link } from "@tanstack/react-router";
import { ArrowRight, LoaderCircle } from "lucide-react";
import type { Agent, Project, Repository, Task } from "@/api";
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
};
export type LaunchDraft = DraftFields & {
  projectId: string;
  imported?: Task;
  partial?: { taskId: string; agent: Agent; repository: Repository };
  error?: string;
  savedTaskId?: string;
};
export type TaskLauncherDialogProps = {
  open: boolean;
  pending: boolean;
  loading: boolean;
  canLaunch: boolean;
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
  onRetry?(): void;
  onStartAnother(): void;
  onSetup(section: "repositories" | "agents"): void;
  onCloseAutoFocus(event: Event): void;
};
export function TaskLauncherDialog(p: TaskLauncherDialogProps) {
  const { draft, pending } = p;
  const readonly = !!draft?.imported || !!draft?.partial;
  const repository = draft?.partial?.repository;
  const agent = draft?.partial?.agent;
  const repositories = repository ? [repository] : p.repositories;
  const agents = agent ? [agent] : p.agents.filter((a) => a.enabled);
  const selectedAgent = agent ?? agents.find((a) => a.id === draft?.agentId);
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
                ? "Imported Task"
                : "New Task"}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            {p.discovery
              ? "Get a project brief, priorities, and recommended agents from the repository."
              : "Give an Agent a Task to run in an isolated Workspace."}
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
            p.onSubmit();
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
                maxLength={500}
                placeholder="What would you like to work on?"
              />
            </div>
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <ResourceSelect
                id="repository"
                label="Repository"
                value={draft?.repositoryId ?? ""}
                onValueChange={(repositoryId) =>
                  p.onDraftChange({ repositoryId })
                }
                options={repositories.map((r) => ({
                  value: r.id,
                  label: r.name,
                }))}
                placeholder="No repositories"
                disabled={readonly || pending}
                required
              />
              <ResourceSelect
                id="agent"
                label="Agent"
                value={draft?.agentId ?? ""}
                onValueChange={(agentId) => p.onDraftChange({ agentId })}
                options={agents.map((a) => ({
                  value: a.id,
                  label: `${a.name} · ${a.backend}`,
                }))}
                placeholder="No enabled agents"
                disabled={pending || !!draft?.partial}
                required
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="description">
                Description{" "}
                <span className="font-normal text-muted-foreground">
                  (optional)
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
          </fieldset>
          {p.loading && (
            <p role="status" className="text-sm text-muted-foreground">
              Loading Task and launch options…
            </p>
          )}
          {unavailable && (
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
            <span className="text-xs text-muted-foreground">
              {draft?.partial
                ? `Task saved · ${draft.partial.taskId.slice(0, 8)}`
                : selectedAgent
                  ? `${selectedAgent.backend === "fake" ? "Network disabled" : "Network enabled"} · Dedicated Workspace`
                  : "Select a Repository and Agent"}
            </span>
            <div className="flex flex-wrap gap-2">
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
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
