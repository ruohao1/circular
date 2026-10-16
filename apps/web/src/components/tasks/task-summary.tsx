import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  stageLabels,
  taskPlanningOptions,
  type TaskPlanningSearch,
} from "@/lib/task-planning";
import { Button } from "@/components/ui/button";
import { ErrorAlert } from "@/components/error-alert";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

export function TaskSummary({
  projectId,
  search,
  locked,
  onStageChange,
  onClose,
}: {
  projectId: string;
  search: TaskPlanningSearch;
  locked: boolean;
  onStageChange(status: TaskPlanningSearch["status"]): void;
  onClose(): void;
}) {
  const [docked, setDocked] = useState(
    () => window.matchMedia("(min-width: 1024px)").matches,
  );
  useEffect(() => {
    const media = window.matchMedia("(min-width: 1024px)");
    const update = () => setDocked(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const summary = useQuery({
    ...taskPlanningOptions(projectId, {
      scope: search.scope,
      q: search.q,
      priority: search.priority || undefined,
      repository_id: search.repository_id,
      preferred_agent_id: search.preferred_agent_id,
      limit: 1,
    }),
    refetchInterval: locked ? false : 15000,
    refetchOnWindowFocus: !locked,
  });
  const total = summary.data?.groups.reduce(
    (sum, group) =>
      sum +
      (!search.status || group.status === search.status ? group.total : 0),
    0,
  );
  const body = !projectId ? (
    <p className="text-sm text-muted-foreground">
      Choose a Project to see its Tasks.
    </p>
  ) : summary.isError ? (
    <ErrorAlert>
      Could not load Task totals.{" "}
      <Button variant="link" onClick={() => void summary.refetch()}>
        Retry totals
      </Button>
    </ErrorAlert>
  ) : summary.isPending ? (
    <p role="status" className="text-sm text-muted-foreground">
      Loading totals…
    </p>
  ) : (
    <>
      <p className="mb-5 text-sm font-medium">
        {total} {total === 1 ? "Task" : "Tasks"}
      </p>
      <h3 className="mb-2 text-xs font-medium text-muted-foreground">
        Workflow
      </h3>
      <div className="grid gap-1">
        {summary.data.groups.map((group) => (
          <Button
            key={group.status}
            variant={search.status === group.status ? "secondary" : "ghost"}
            className="w-full justify-between text-[13px]"
            aria-label={`Filter by ${stageLabels[group.status]}`}
            aria-pressed={search.status === group.status}
            disabled={locked}
            onClick={() =>
              onStageChange(search.status === group.status ? "" : group.status)
            }
          >
            <span>{stageLabels[group.status]}</span>
            <span className="tabular-nums text-muted-foreground">
              {group.total}
            </span>
          </Button>
        ))}
      </div>
      <p className="mt-4 text-xs leading-relaxed text-muted-foreground">
        Stage counts follow this view’s search and property filters, across all
        planned dates.
      </p>
    </>
  );
  return docked ? (
    <aside
      id="task-summary"
      aria-label="Task summary"
      className="sticky top-3 max-h-[calc(100svh-7rem)] w-64 shrink-0 overflow-y-auto rounded-lg border bg-card p-4"
    >
      <h2 className="mb-4 text-sm font-medium">Task summary</h2>
      {body}
    </aside>
  ) : (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        id="task-summary"
        className="top-0 right-0 left-auto h-svh max-h-svh max-w-sm translate-x-0 translate-y-0 rounded-r-none"
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          document.getElementById("task-summary-toggle")?.focus();
        }}
      >
        <DialogTitle className="mb-1 text-sm font-medium">
          Task summary
        </DialogTitle>
        <DialogDescription className="mb-5 text-xs text-muted-foreground">
          Choose a workflow stage to filter Tasks.
        </DialogDescription>
        {body}
      </DialogContent>
    </Dialog>
  );
}
