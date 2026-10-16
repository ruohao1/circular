import { Link } from "@tanstack/react-router";
import type { Task, TaskPlanningItem, TaskPlanningPage } from "@/api";
import { stageLabels, priorityLabels } from "@/lib/task-planning";
import { scheduleLabel } from "@/lib/task-schedules";
import { RunStatus } from "@/components/run-status";
import { Button } from "@/components/ui/button";

export function TaskExecution({ item }: { item: TaskPlanningItem }) {
  return (
    <div className="space-y-1 text-xs text-muted-foreground">
      {item.latest_run ? (
        <>
          <RunStatus value={item.latest_run.status} />
          <p className="break-words">
            {item.latest_run.agent_name} · Attempt {item.latest_run.attempt}
          </p>
        </>
      ) : (
        <span>No Runs yet</span>
      )}
      {item.active_run_count > 0 && (
        <p className="font-medium text-primary">
          {item.active_run_count} active{" "}
          {item.active_run_count === 1 ? "Run" : "Runs"}
        </p>
      )}
    </div>
  );
}
export function TaskList({
  page,
  onLoadMore,
  loadingGroups,
}: {
  page: TaskPlanningPage;
  onLoadMore(status: Task["status"]): void;
  loadingGroups: string[];
}) {
  return (
    <div className="space-y-3">
      {page.groups.map((group) => (
        <section
          key={group.status}
          aria-label={stageLabels[group.status]}
          className="min-w-0"
        >
          <div className="flex h-9 items-center gap-2 rounded-md bg-muted/40 px-3">
            <h2 className="text-[13px] font-medium">
              {stageLabels[group.status]}
            </h2>
            <span className="text-xs tabular-nums text-muted-foreground">
              {group.total}
            </span>
          </div>
          {group.items.length ? (
            <ul role="list">
              {group.items.map((item) => {
                const preferredAgent = item.preferred_agent
                  ? `${item.preferred_agent.name}${item.preferred_agent.enabled ? "" : " · Disabled"}`
                  : "No preferred Agent";
                const latestRun = item.latest_run
                  ? `${item.latest_run.status.replaceAll("_", " ")} · ${item.latest_run.agent_name} · Attempt ${item.latest_run.attempt}`
                  : "No Runs yet";
                const activeRuns = `${item.active_run_count} active ${item.active_run_count === 1 ? "Run" : "Runs"}`;
                return (
                  <li key={item.task.id}>
                    <Link
                      to="/tasks"
                      search={(previous) => ({
                        ...previous,
                        taskId: item.task.id,
                      })}
                      aria-label={item.task.title}
                      aria-description={[
                        `Workflow: ${stageLabels[group.status]}`,
                        `Priority: ${priorityLabels[item.task.priority]}`,
                        `Repository: ${item.repository?.name ?? "No Repository"}`,
                        `Preferred Agent: ${preferredAgent}`,
                        scheduleLabel(item.task),
                        `Latest Run: ${latestRun}`,
                        activeRuns,
                      ].join(". ")}
                      className="grid min-h-11 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 rounded-md px-3 py-2 text-[13px] hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring sm:grid-cols-[minmax(0,1fr)_4.5rem_12rem] lg:grid-cols-[minmax(0,1fr)_4.5rem_7rem_12rem] xl:grid-cols-[minmax(0,1fr)_4.5rem_7rem_9rem_11rem_12rem]"
                    >
                      <span
                        className="line-clamp-2 min-w-0 break-words font-medium sm:line-clamp-1"
                        title={item.task.title}
                      >
                        {item.task.title}
                      </span>
                      <span className="hidden text-xs text-muted-foreground sm:block">
                        {priorityLabels[item.task.priority]}
                      </span>
                      <span
                        className="hidden truncate text-xs text-muted-foreground lg:block"
                        title={`Repository: ${item.repository?.name ?? "No Repository"}`}
                      >
                        {item.repository?.name ?? "No Repository"}
                      </span>
                      <span
                        className="hidden min-w-0 items-center gap-1 text-xs text-muted-foreground xl:flex"
                        title={`Preferred Agent: ${preferredAgent}`}
                      >
                        <span className="truncate">
                          {item.preferred_agent?.name ?? "No preferred Agent"}
                        </span>
                        {item.preferred_agent &&
                          !item.preferred_agent.enabled && (
                            <span className="shrink-0">Disabled</span>
                          )}
                      </span>
                      <span
                        className="hidden truncate text-xs text-muted-foreground xl:block"
                        title={scheduleLabel(item.task)}
                      >
                        {scheduleLabel(item.task)}
                      </span>
                      <span
                        className="flex min-w-0 max-w-48 items-center justify-end gap-1.5 text-xs text-muted-foreground sm:justify-start"
                        title={`Latest Run: ${latestRun}. ${activeRuns}`}
                      >
                        {item.latest_run ? (
                          <>
                            <span>Run</span>
                            <RunStatus value={item.latest_run.status} />
                          </>
                        ) : (
                          <span>No Runs yet</span>
                        )}
                        {item.active_run_count > 0 && (
                          <span className="truncate font-medium text-primary">
                            {item.active_run_count} active
                          </span>
                        )}
                      </span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="px-3 py-3 text-xs text-muted-foreground">
              No Tasks in this stage match your filters.
            </p>
          )}
          {group.next_cursor && (
            <div className="px-3 py-2">
              <Button
                variant="ghost"
                size="sm"
                disabled={loadingGroups.includes(group.status)}
                onClick={() => onLoadMore(group.status)}
              >
                Load more {stageLabels[group.status]} Tasks
              </Button>
            </div>
          )}
        </section>
      ))}
    </div>
  );
}
