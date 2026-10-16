import { useEffect, useMemo, useRef } from "react";
import { Link } from "@tanstack/react-router";
import { DropdownMenu } from "radix-ui";
import { GripVertical, MoreHorizontal, Plus } from "lucide-react";
import type { Task, TaskMove, TaskPlanningItem, TaskPlanningPage } from "@/api";
import { scheduleLabel } from "@/lib/task-schedules";
import {
  stageLabels,
  priorityLabels,
  taskStages,
  taskDropIntent,
} from "@/lib/task-planning";
import {
  Kanban,
  KanbanBoard,
  KanbanColumn,
  KanbanColumnContent,
  KanbanItem,
  KanbanItemHandle,
  KanbanOverlay,
  type KanbanMoveEvent,
} from "@/components/reui/kanban";
import { TaskExecution } from "./task-list";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";

export function TaskBoard({
  page,
  pending,
  onMove,
  onLoadMore,
  loadingGroups,
  onInteracting,
  onAdd,
}: {
  page: TaskPlanningPage;
  pending: boolean;
  onMove(id: string, input: TaskMove): void;
  onLoadMore(status: Task["status"]): void;
  loadingGroups: string[];
  onInteracting(active: boolean): void;
  onAdd(status: Task["status"]): void;
}) {
  const frozen = useRef<TaskPlanningPage | undefined>(undefined);
  const root = useRef<HTMLDivElement>(null);
  const restoreFocus = useRef<string | null>(null);
  useEffect(() => {
    frozen.current = undefined;
    onInteracting(false);
  }, [page.board_revision, onInteracting]);
  useEffect(() => {
    if (pending || !restoreFocus.current) return;
    const target = root.current?.querySelector<HTMLButtonElement>(
      `[data-task-focus="${CSS.escape(restoreFocus.current)}"]`,
    );
    (target ?? root.current)?.focus({ preventScroll: true });
    restoreFocus.current = null;
  }, [pending, page.board_revision]);
  const columns = useMemo(
    () => Object.fromEntries(page.groups.map((g) => [g.status, g.items])),
    [page],
  );
  useEffect(() => () => onInteracting(false), [onInteracting]);
  function move(
    item: TaskPlanningItem,
    status: Task["status"],
    placement: TaskMove["placement"],
  ) {
    if (!pending) {
      restoreFocus.current = `move-${item.task.id}`;
      onMove(item.task.id, {
        expected_revision: item.task.revision,
        expected_board_revision: page.board_revision,
        status,
        placement,
      });
    }
  }
  function drop({ event, overContainer, overIndex }: KanbanMoveEvent) {
    const snapshot = frozen.current;
    frozen.current = undefined;
    if (
      pending ||
      !snapshot ||
      snapshot.board_revision !== page.board_revision ||
      !event.over ||
      event.active.id === event.over.id
    )
      return;
    const id = String(event.active.id);
    if (event.activatorEvent instanceof KeyboardEvent)
      restoreFocus.current = `drag-${id}`;
    const target = snapshot.groups.find((g) => g.status === overContainer);
    const task = snapshot.groups
      .flatMap((g) => g.items)
      .find((i) => i.task.id === id)?.task;
    if (!target || !task) return;
    // A column/header drop means the global end, even when its visible slice is empty.
    if (event.over.id === overContainer) {
      onMove(id, {
        expected_revision: task.revision,
        expected_board_revision: snapshot.board_revision,
        status: target.status,
        placement: "end",
      });
      return;
    }
    let index = overIndex;
    const activator = event.activatorEvent;
    const pointerY =
      "clientY" in activator
        ? Number(activator.clientY)
        : "touches" in activator
          ? (activator as TouchEvent).touches[0]?.clientY
          : undefined;
    if (pointerY !== undefined) {
      const remaining = target.items.filter((i) => i.task.id !== id);
      index = remaining.findIndex((i) => i.task.id === event.over!.id);
      if (index < 0) return;
      if (
        pointerY + event.delta.y >
        event.over.rect.top + event.over.rect.height / 2
      )
        index++;
    }
    const input = taskDropIntent(snapshot, id, target.status, index);
    if (input) onMove(id, input);
  }
  return (
    <div className="space-y-3" ref={root} tabIndex={-1}>
      <p className="text-xs text-muted-foreground">
        Drag a handle or use the Move menu. Workflow stages are manual; moving a
        Task does not change its Runs.
      </p>
      <div className="overflow-x-auto pb-4" aria-label="Task board">
        <Kanban
          key={page.board_revision}
          value={columns}
          getItemValue={(i) => i.task.id}
          onValueChange={() => {}}
          onMove={drop}
          onDragStart={() => {
            frozen.current = page;
            onInteracting(true);
          }}
          onDragEnd={() => onInteracting(false)}
          onDragCancel={() => {
            frozen.current = undefined;
            onInteracting(false);
          }}
          accessibility={{
            screenReaderInstructions: {
              draggable:
                "Press Space to pick up this Task. Use arrow keys to move, Space to drop, or Escape to cancel. The Move menu offers the same actions.",
            },
            announcements: {
              onDragStart: ({ active }) =>
                `Picked up ${page.groups.flatMap((g) => g.items).find((i) => i.task.id === active.id)?.task.title ?? "Task"}.`,
              onDragOver: ({ over }) =>
                over
                  ? `Over ${stageLabels[over.id as Task["status"]] ?? page.groups.flatMap((g) => g.items).find((i) => i.task.id === over.id)?.task.title ?? "Task"}.`
                  : "Outside the board. Release to cancel.",
              onDragEnd: ({ over }) =>
                over
                  ? "Drop finished. Waiting for saved Task order."
                  : "Move cancelled.",
              onDragCancel: () => "Move cancelled. Task order unchanged.",
            },
          }}
        >
          <KanbanBoard className="flex min-h-[max(20rem,calc(100svh-10rem))] items-stretch gap-3">
            {page.groups.map((group) => (
              <KanbanColumn
                key={group.status}
                value={group.status}
                role="region"
                aria-label={stageLabels[group.status]}
                className="min-w-[270px] flex-1 basis-[270px] rounded-xl border bg-muted/30 p-3"
              >
                <div className="mb-3 flex min-h-9 items-center gap-2 px-1">
                  <h2 className="text-sm font-semibold">
                    {stageLabels[group.status]}
                  </h2>
                  <Badge variant="secondary">{group.total}</Badge>
                  <Button
                    id={`task-add-${group.status}`}
                    variant="ghost"
                    size="icon-sm"
                    className="ml-auto"
                    aria-label={`Add Task to ${stageLabels[group.status]}`}
                    title={`Add Task to ${stageLabels[group.status]}`}
                    disabled={pending}
                    onClick={() => onAdd(group.status)}
                  >
                    <Plus aria-hidden="true" />
                  </Button>
                </div>
                <KanbanColumnContent value={group.status} className="gap-3">
                  {group.items.map((item) => (
                    <KanbanItem
                      key={item.task.id}
                      value={item.task.id}
                      disabled={pending}
                      aria-busy={pending}
                      className="min-w-0 space-y-3 rounded-lg border bg-card p-3 shadow-sm"
                    >
                      <div className="flex items-start gap-1">
                        <Link
                          to="/tasks"
                          search={(previous) => ({
                            ...previous,
                            taskId: item.task.id,
                          })}
                          className="min-w-0 flex-1 break-words text-sm font-medium leading-5 hover:text-primary"
                        >
                          {item.task.title}
                        </Link>
                        <KanbanItemHandle asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={pending}
                            className="size-7 shrink-0 touch-none"
                            data-task-focus={`drag-${item.task.id}`}
                            aria-label={`Drag ${item.task.title}`}
                          >
                            <GripVertical className="size-4" />
                          </Button>
                        </KanbanItemHandle>
                        <DropdownMenu.Root>
                          <DropdownMenu.Trigger asChild>
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={pending}
                              className="size-7 shrink-0"
                              data-task-focus={`move-${item.task.id}`}
                              aria-label={`Move ${item.task.title}`}
                            >
                              <MoreHorizontal className="size-4" />
                            </Button>
                          </DropdownMenu.Trigger>
                          <DropdownMenu.Portal>
                            <DropdownMenu.Content
                              align="end"
                              sideOffset={5}
                              className="z-50 min-w-44 rounded-md border bg-popover p-1 text-sm text-popover-foreground shadow-lg"
                            >
                              {taskStages
                                .filter((status) => status !== item.task.status)
                                .map((status) => (
                                  <DropdownMenu.Item
                                    key={status}
                                    onSelect={() => move(item, status, "end")}
                                    className="cursor-default rounded-sm px-3 py-2 outline-none focus:bg-accent"
                                  >
                                    Move to {stageLabels[status]}
                                  </DropdownMenu.Item>
                                ))}
                              <DropdownMenu.Separator className="my-1 h-px bg-border" />
                              {(
                                [
                                  ["up", "Move up"],
                                  ["down", "Move down"],
                                  ["start", "Move to top"],
                                  ["end", "Move to bottom"],
                                ] as const
                              ).map(([placement, label]) => (
                                <DropdownMenu.Item
                                  key={placement}
                                  onSelect={() =>
                                    move(item, item.task.status, placement)
                                  }
                                  className="cursor-default rounded-sm px-3 py-2 outline-none focus:bg-accent"
                                >
                                  {label}
                                </DropdownMenu.Item>
                              ))}
                            </DropdownMenu.Content>
                          </DropdownMenu.Portal>
                        </DropdownMenu.Root>
                      </div>
                      <div className="space-y-1 text-xs text-muted-foreground">
                        <p className="break-words">
                          {item.repository?.name ?? "No Repository"}
                        </p>
                        <p>{priorityLabels[item.task.priority]}</p>
                        <p className="break-words">
                          {scheduleLabel(item.task)}
                        </p>
                        <p className="break-words">
                          Preferred: {item.preferred_agent?.name ?? "No Agent"}
                          {item.preferred_agent &&
                            !item.preferred_agent.enabled &&
                            " · Disabled"}
                        </p>
                      </div>
                      <div className="border-t pt-3">
                        <TaskExecution item={item} />
                      </div>
                    </KanbanItem>
                  ))}
                </KanbanColumnContent>
                {!group.items.length && (
                  <p className="px-1 py-5 text-xs text-muted-foreground">
                    No matching Tasks. Drop here to move to the end of this
                    stage.
                  </p>
                )}
                {group.next_cursor && (
                  <Button
                    variant="outline"
                    className="mt-3 whitespace-normal"
                    disabled={pending || loadingGroups.includes(group.status)}
                    onClick={() => onLoadMore(group.status)}
                  >
                    Load more {stageLabels[group.status]} Tasks
                  </Button>
                )}
              </KanbanColumn>
            ))}
          </KanbanBoard>
          <KanbanOverlay>
            {({ value }) => (
              <div className="max-w-[270px] break-words rounded-lg border bg-card p-4 text-sm shadow-lg">
                {
                  page.groups
                    .flatMap((g) => g.items)
                    .find((i) => i.task.id === value)?.task.title
                }
              </div>
            )}
          </KanbanOverlay>
        </Kanban>
      </div>
    </div>
  );
}
