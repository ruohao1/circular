import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type {
  Agent,
  Repository,
  Task,
  TaskMove,
  TaskPlanningDetail,
} from "@/api";
import { scheduleError, type TaskPlanningDraft } from "@/lib/task-schedules";
import { TaskScheduleFields } from "./task-schedule-fields";
import {
  stageLabels,
  taskStages,
  priorityLabels,
  taskPriorities,
  type TaskPlanningSearch,
} from "@/lib/task-planning";
import { runQueueOptions } from "@/lib/run-queue";
import { RunQueue } from "@/components/console/run-queue";
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

export interface TaskDetailProps {
  detail: TaskPlanningDetail;
  view: TaskPlanningSearch["view"];
  onViewChange(view: TaskPlanningSearch["view"]): void;
  onShowTimeline(): void;
  timelineHidden?: boolean;
  focusSchedule?: boolean;
  onScheduleFocused(): void;
  draft: TaskPlanningDraft;
  repositories: Repository[];
  agents: Agent[];
  dirty: boolean;
  pending: boolean;
  moveUnavailable?: "refreshing" | "error";
  onRetryOrder(): void;
  error?: { message: string; conflict: boolean };
  onDraftChange(patch: Partial<TaskPlanningDraft>): void;
  onSave(): void;
  onReload(): void;
  onClose(): void;
  onMove(id: string, input: TaskMove): void;
}
export function TaskDetail(p: TaskDetailProps) {
  const { detail, draft, pending } = p;
  const task = detail.task;
  useEffect(() => {
    if (!p.focusSchedule) return;
    const frame = requestAnimationFrame(() => {
      document.getElementById("detail-planned_start_date")?.focus();
      p.onScheduleFocused();
    });
    return () => cancelAnimationFrame(frame);
  }, [p.focusSchedule]);
  const [stage, setStage] = useState(task.status);
  useEffect(() => setStage(task.status), [task.status]);
  const [cursor, setCursor] = useState("");
  const runs = useQuery(
    runQueueOptions(task.project_id, { task_id: task.id, limit: 20, cursor }),
  );
  const refs = task.external_refs?.linear;
  const source =
    refs &&
    typeof refs === "object" &&
    "url" in refs &&
    typeof refs.url === "string" &&
    /^https?:\/\//.test(refs.url)
      ? refs.url
      : undefined;
  const agentOptions = p.agents.map((a) => ({
    value: a.id,
    label: `${a.name}${a.enabled ? "" : " · Disabled"}`,
    disabled: !a.enabled,
  }));
  if (
    draft.preferred_agent_id &&
    !agentOptions.some((a) => a.value === draft.preferred_agent_id)
  )
    agentOptions.push({
      value: draft.preferred_agent_id,
      label: "Unavailable preferred Agent",
      disabled: true,
    });
  const repoOptions = p.repositories.map((r) => ({
    value: r.id,
    label: r.name,
    disabled: false,
  }));
  if (
    draft.repository_id &&
    !repoOptions.some((r) => r.value === draft.repository_id)
  )
    repoOptions.push({
      value: draft.repository_id,
      label: "Unavailable Repository",
      disabled: true,
    });
  const locked =
    detail.content_lock_reason === "source_managed"
      ? "Content is managed by its source."
      : detail.content_lock_reason === "active_work"
        ? "Instructions are locked while this Task has an owner."
        : detail.content_lock_reason === "active_run"
          ? "Instructions are locked while a Run is active."
          : undefined;
  const valid =
    !scheduleError(draft.planned_start_date, draft.planned_finish_date) &&
    !!draft.title.trim() &&
    Array.from(draft.title.trim()).length <= 500 &&
    (draft.description === task.description ||
      Array.from(draft.description).length <= 20000);
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) p.onClose();
      }}
    >
      <DialogContent
        className="max-w-4xl"
        onEscapeKeyDown={(e) => {
          if (pending) e.preventDefault();
        }}
        onInteractOutside={(e) => {
          if (pending) e.preventDefault();
        }}
      >
        <div className="mb-6 space-y-2 pr-8">
          <DialogTitle className="text-xl font-semibold">
            Task details
          </DialogTitle>
          <DialogDescription>
            Plan the work and inspect its attempts. Workflow is independent of
            Run status.
          </DialogDescription>
          <div className="flex flex-wrap gap-2">
            {(["list", "board"] as const)
              .filter((view) => view !== p.view)
              .map((view) => (
                <Button
                  key={view}
                  variant="outline"
                  size="sm"
                  disabled={pending}
                  onClick={() => p.onViewChange(view)}
                >
                  Show in {view === "list" ? "List" : "Kanban"}
                </Button>
              ))}
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={p.onShowTimeline}
            >
              Show in Gantt
            </Button>
          </div>
          {p.timelineHidden && (
            <p role="status" className="text-xs text-muted-foreground">
              This Task is outside the current Gantt page, window or filters.
              Your detail stays open; change the filters or page to find it.
            </p>
          )}
          {p.view === "timeline" && p.dirty && (
            <p className="text-xs text-muted-foreground">
              Gantt bars reflect saved dates. Save changes to update the
              schedule.
            </p>
          )}
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) p.onSave();
          }}
          className="space-y-5"
        >
          <fieldset disabled={pending} className="space-y-4">
            <div className="grid gap-2">
              <Label htmlFor="detail-title">Task title</Label>
              <Input
                id="detail-title"
                value={draft.title}
                readOnly={!detail.content_editable}
                onChange={(e) => p.onDraftChange({ title: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="detail-description">Description</Label>
              <Textarea
                id="detail-description"
                rows={5}
                value={draft.description}
                readOnly={!detail.content_editable}
                onChange={(e) =>
                  p.onDraftChange({ description: e.target.value })
                }
              />
            </div>
            {locked && (
              <p className="text-xs text-muted-foreground">
                {locked}{" "}
                {source && (
                  <a
                    href={source}
                    target="_blank"
                    rel="noreferrer"
                    className="underline"
                  >
                    Open source
                  </a>
                )}
              </p>
            )}
            <div className="grid gap-4 sm:grid-cols-3">
              <ResourceSelect
                id="detail-repository"
                label="Repository"
                value={draft.repository_id ?? "none"}
                onValueChange={(value) =>
                  p.onDraftChange({
                    repository_id: value === "none" ? null : value,
                  })
                }
                options={[
                  { value: "none", label: "No Repository" },
                  ...repoOptions,
                ]}
                placeholder="No Repository"
                disabled={!detail.repository_editable}
              />
              <ResourceSelect
                id="detail-priority"
                label="Priority"
                value={draft.priority}
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
              <ResourceSelect
                id="detail-agent"
                label="Preferred Agent"
                value={draft.preferred_agent_id ?? "none"}
                onValueChange={(value) =>
                  p.onDraftChange({
                    preferred_agent_id: value === "none" ? null : value,
                  })
                }
                options={[
                  { value: "none", label: "No preferred Agent" },
                  ...agentOptions,
                ]}
                placeholder="No preferred Agent"
              />
            </div>
            {detail.repository_lock_reason === "has_runs" && (
              <p className="text-xs text-muted-foreground">
                Repository is fixed after the first Run.
              </p>
            )}
            {detail.repository_lock_reason === "has_work_sessions" && (
              <p className="text-xs text-muted-foreground">
                Repository is fixed after work has been claimed.
              </p>
            )}
            <TaskScheduleFields draft={draft} onChange={p.onDraftChange} />
          </fieldset>
          {p.error && (
            <ErrorAlert>
              {p.error.conflict
                ? "This Task changed elsewhere. Reload its latest version before saving."
                : p.error.message}{" "}
              <Button
                type="button"
                variant="link"
                disabled={pending}
                onClick={p.onReload}
              >
                Reload latest
              </Button>
            </ErrorAlert>
          )}
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span className="text-xs text-muted-foreground">
              {p.dirty ? "Unsaved changes" : "Changes saved"}
            </span>
            <Button
              type="submit"
              disabled={pending || !p.dirty || !valid || p.error?.conflict}
            >
              {pending ? "Saving…" : "Save changes"}
            </Button>
          </div>
        </form>
        <div className="my-6 flex flex-wrap items-end gap-3 border-y py-5">
          <ResourceSelect
            id="detail-workflow"
            label="Workflow"
            value={stage}
            onValueChange={(value) =>
              setStage(taskStages.find((s) => s === value) ?? task.status)
            }
            options={taskStages.map((value) => ({
              value,
              label: stageLabels[value],
            }))}
            placeholder="Workflow"
            disabled={pending || p.dirty}
            className="min-w-44"
          />
          <Button
            variant="outline"
            disabled={
              pending || p.dirty || !!p.moveUnavailable || stage === task.status
            }
            onClick={() =>
              p.onMove(task.id, {
                expected_revision: task.revision,
                expected_board_revision: detail.board_revision,
                status: stage,
                placement: "end",
              })
            }
          >
            Move to {stageLabels[stage]}
          </Button>
          {p.moveUnavailable && (
            <div role="status" className="w-full text-xs text-muted-foreground">
              <span>
                {p.moveUnavailable === "error"
                  ? "Task order could not refresh."
                  : "Refreshing Task order…"}
              </span>
              {p.moveUnavailable === "error" && (
                <Button
                  variant="link"
                  disabled={pending}
                  onClick={p.onRetryOrder}
                >
                  Retry Task order
                </Button>
              )}
            </div>
          )}
          <div className="ml-auto">
            <Button asChild disabled={pending}>
              <Link
                to="/"
                search={{ taskId: task.id }}
                onClick={(e) => {
                  if (pending) e.preventDefault();
                }}
              >
                Start Run
              </Link>
            </Button>
          </div>
          {(stage === "cancelled" || task.status === "cancelled") && (
            <p className="w-full text-xs text-muted-foreground">
              Cancelling this Task does not cancel its active Runs. Cancel each
              Run separately.
            </p>
          )}
        </div>
        <section aria-label="Task Run history" className="min-w-0 space-y-3">
          <h2 className="font-medium">Run history</h2>
          <p className="text-xs text-muted-foreground">
            Each attempt records its actual Agent. A successful Run does not
            mark the Task Done.
          </p>
          {runs.error && (
            <ErrorAlert>
              {runs.data && "Showing the last update. "}
              {runs.error.message}{" "}
              <Button variant="link" onClick={() => void runs.refetch()}>
                Retry history
              </Button>
            </ErrorAlert>
          )}
          {runs.isPending ? (
            <p role="status">Loading Runs…</p>
          ) : runs.data?.items.length ? (
            <div className="overflow-hidden rounded-lg border">
              <RunQueue items={runs.data.items} />
            </div>
          ) : (
            !runs.error && (
              <p className="text-sm text-muted-foreground">
                No Runs yet. Start one when the Task is ready.
              </p>
            )
          )}
          <div className="flex gap-2">
            {cursor && (
              <Button variant="outline" onClick={() => setCursor("")}>
                Newest Runs
              </Button>
            )}
            {runs.data?.next_cursor && (
              <Button
                variant="outline"
                onClick={() => setCursor(runs.data!.next_cursor)}
              >
                Older Runs
              </Button>
            )}
          </div>
        </section>
      </DialogContent>
    </Dialog>
  );
}
