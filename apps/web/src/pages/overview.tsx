import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, CircleDot, CircleAlert } from "lucide-react";
import { useProject } from "@/use-project";
import { runQueueOptions } from "@/lib/run-queue";
import { useTaskLauncher } from "@/components/console/task-launcher-context";
import { RunQueue } from "@/components/console/run-queue";
import { RequestAttention } from "@/components/console/request-attention";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

function RunSection({
  projectId,
  group,
}: {
  projectId: string;
  group: "active" | "failed";
}) {
  const rows = useQuery(
    runQueueOptions(projectId, { group, limit: group === "active" ? 10 : 5 }),
  );
  const title = group === "active" ? "Active Runs" : "Recent failures";
  const Icon = group === "active" ? CircleDot : CircleAlert;
  return (
    <section aria-label={title} className="min-w-0">
      <Card className="min-w-0 gap-0 overflow-hidden py-0">
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-3 border-b py-4">
          <CardTitle>
            <h2 className="flex items-center gap-2 text-sm">
              <Icon
                className="size-4 text-muted-foreground"
                aria-hidden="true"
              />
              {title}
            </h2>
          </CardTitle>
          <Button asChild variant="link" className="h-auto p-0 text-xs">
            <Link to="/runs" search={{ group, q: "", cursor: "" }}>
              View all Runs
              <ArrowUpRight aria-hidden="true" />
            </Link>
          </Button>
        </CardHeader>
        {rows.error && (
          <div className="p-5">
            <ErrorAlert>
              {rows.data && <span>Showing the last update. </span>}
              {rows.error.message}{" "}
              <Button variant="link" onClick={() => void rows.refetch()}>
                Retry
              </Button>
            </ErrorAlert>
          </div>
        )}
        {!projectId ? (
          <p className="p-6 text-sm text-muted-foreground">
            Choose a Project to see its Runs.
          </p>
        ) : rows.isPending ? (
          <div
            className="space-y-4 p-5"
            role="status"
            aria-label={`Loading ${title}`}
          >
            <Skeleton className="h-7 w-full" />
            <Skeleton className="h-7 w-3/4" />
          </div>
        ) : rows.data?.items.length ? (
          <RunQueue items={rows.data.items} />
        ) : (
          !rows.error && (
            <div className="space-y-2 px-6 py-9">
              <p className="text-sm font-medium">
                {group === "active" ? "No active Runs." : "No failed Runs."}
              </p>
              <p className="text-xs leading-relaxed text-muted-foreground">
                {group === "active"
                  ? "Start a new Task when you’re ready. Its progress will appear here."
                  : "Recent failed attempts will appear here with their Task context."}
              </p>
            </div>
          )
        )}
      </Card>
    </section>
  );
}
export function ConsoleOverview({ taskId }: { taskId?: string }) {
  const { selectedProject, project } = useProject();
  const { openImportedTask } = useTaskLauncher();
  useEffect(() => {
    if (taskId) openImportedTask(taskId);
  }, [taskId, openImportedTask]);
  return (
    <div className="mx-auto max-w-[1450px] space-y-7 px-4 py-7 sm:px-6 lg:px-8 lg:py-9">
      <div>
        <p className="mb-2 text-[10px] font-medium tracking-[0.16em] text-muted-foreground">
          {project?.name ?? "YOUR WORKSPACE"}
        </p>
        <h1 className="text-2xl font-semibold tracking-tight">Overview</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Follow active Runs and find what needs your attention.
        </p>
      </div>
      <div className="grid min-w-0 items-start gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <div className="grid min-w-0 gap-6">
          <RunSection projectId={selectedProject} group="active" />
          <RunSection projectId={selectedProject} group="failed" />
        </div>
        <div className="grid min-w-0 gap-6">
          <RequestAttention projectId={selectedProject} />
          <RequestAttention projectId="" unrouted />
        </div>
      </div>
    </div>
  );
}
