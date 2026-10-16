import { Link } from "@tanstack/react-router";
import type { Task, TaskPlanningItem, TaskPlanningPage } from "@/api";
import { stageLabels, priorityLabels } from "@/lib/task-planning";
import { scheduleLabel } from "@/lib/task-schedules";
import { RunStatus } from "@/components/run-status";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";

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
    <div className="space-y-5">
      {page.groups.map((group) => (
        <section
          key={group.status}
          aria-label={stageLabels[group.status]}
          className="min-w-0 overflow-hidden rounded-xl border bg-card"
        >
          <div className="flex items-center gap-3 border-b px-5 py-3">
            <h2 className="text-sm font-medium">{stageLabels[group.status]}</h2>
            <Badge variant="secondary">{group.total}</Badge>
          </div>
          {group.items.length ? (
            <Table className="min-w-[760px] table-fixed">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[38%] px-5">Task</TableHead>
                  <TableHead className="w-[14%]">Priority</TableHead>
                  <TableHead className="w-[22%]">Preferred Agent</TableHead>
                  <TableHead className="w-[26%]">Execution</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {group.items.map((item) => (
                  <TableRow key={item.task.id}>
                    <TableCell className="px-5 py-4 whitespace-normal">
                      <Link
                        to="/tasks"
                        search={(previous) => ({
                          ...previous,
                          taskId: item.task.id,
                        })}
                        className="block break-words font-medium hover:text-primary"
                      >
                        {item.task.title}
                      </Link>
                      <p className="mt-1 break-words text-xs text-muted-foreground">
                        {item.repository?.name ?? "No Repository"}
                      </p>
                      <p className="mt-1 break-words text-xs text-muted-foreground">
                        {scheduleLabel(item.task)}
                      </p>
                    </TableCell>
                    <TableCell className="whitespace-normal text-xs">
                      {priorityLabels[item.task.priority]}
                    </TableCell>
                    <TableCell className="whitespace-normal text-xs break-words">
                      {item.preferred_agent?.name ?? "No preferred Agent"}
                      {item.preferred_agent &&
                        !item.preferred_agent.enabled && (
                          <span className="block text-muted-foreground">
                            Disabled
                          </span>
                        )}
                    </TableCell>
                    <TableCell className="whitespace-normal">
                      <TaskExecution item={item} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="px-5 py-6 text-sm text-muted-foreground">
              No Tasks in this stage match your filters.
            </p>
          )}
          {group.next_cursor && (
            <div className="border-t px-5 py-3">
              <Button
                variant="outline"
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
