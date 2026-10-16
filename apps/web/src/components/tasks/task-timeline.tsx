import { useEffect, useMemo, useRef, useState } from "react";
import { Gantt } from "@/components/reui/gantt/gantt";
import { GanttView } from "@/components/reui/gantt/gantt-view";
import type {
  GanttEvent,
  GanttProposedUpdate,
} from "@/components/reui/gantt/gantt-types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { priorityLabels, stageLabels } from "@/lib/task-planning";
import { parseCivilDate, scheduleLabel } from "@/lib/task-schedules";
import {
  civilToUTC,
  inclusiveEnd,
  localToday,
  rendererScale,
  stepTimeline,
  timelineWindow,
  ganttDates,
  scheduleIntent,
  scheduleSnapshot,
  type TaskScheduleSnapshot,
  type TaskScheduleIntent,
  type TimelineScale,
  type TimelineSection,
} from "@/lib/task-timeline";
import { TaskExecution } from "./task-list";
import type { TaskTimelineState } from "./use-task-timeline";
import { TaskGanttInteraction } from "./task-gantt-interaction";

function useLocalToday() {
  const [today, setToday] = useState(localToday);
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout>;
    const refresh = () => {
      clearTimeout(timer);
      setToday(localToday());
      const now = new Date(),
        midnight = new Date(now);
      midnight.setHours(24, 0, 0, 0);
      timer = setTimeout(refresh, midnight.getTime() - now.getTime() + 50);
    };
    refresh();
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, []);
  return today;
}
export function TaskTimelineControls({
  anchor,
  scale,
  locked,
  onChange,
}: {
  anchor: string;
  scale: TimelineScale;
  locked: boolean;
  onChange: (patch: { date?: string; scale?: TimelineScale }) => void;
}) {
  const previous = stepTimeline(anchor, scale, -1),
    next = stepTimeline(anchor, scale, 1);
  return (
    <div className="flex max-w-full flex-wrap items-center gap-1 sm:flex-nowrap">
      <Button
        variant="outline"
        size="sm"
        disabled={locked || !previous}
        onClick={() => previous && onChange({ date: previous })}
        aria-label="Previous period"
      >
        ←
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={locked}
        onClick={() => onChange({ date: localToday() })}
        data-schedule-today=""
      >
        Today
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={locked || !next}
        onClick={() => next && onChange({ date: next })}
        aria-label="Next period"
      >
        →
      </Button>
      <Input
        type="date"
        aria-label="Gantt anchor"
        value={anchor}
        min="0001-01-01"
        max="9999-12-31"
        className="h-8 w-36"
        disabled={locked}
        onChange={(e) => {
          if (parseCivilDate(e.target.value))
            onChange({ date: e.target.value });
        }}
      />
      <div role="group" aria-label="Gantt scale" className="flex gap-1">
        {(["day", "week", "month"] as const).map((value) => (
          <Button
            key={value}
            size="sm"
            variant={scale === value ? "secondary" : "ghost"}
            aria-pressed={scale === value}
            disabled={locked}
            onClick={() => onChange({ scale: value })}
          >
            {value[0].toUpperCase() + value.slice(1)}
          </Button>
        ))}
      </div>
    </div>
  );
}
export function TaskTimeline({
  state,
  anchor,
  scale,
  locked,
  onOpen,
  scheduleDisabled,
  pending,
  onReschedule,
  onInteracting,
}: {
  state: TaskTimelineState;
  anchor: string;
  scale: TimelineScale;
  locked: boolean;
  onOpen: (id: string, schedule?: boolean) => void;
  scheduleDisabled: boolean;
  pending: boolean;
  onReschedule: (intent: TaskScheduleIntent) => boolean;
  onInteracting: (value: boolean) => void;
}) {
  const root = useRef<HTMLDivElement>(null);
  const commitTask = useRef<string | null>(null);
  const today = useLocalToday();
  const todayDate = useMemo(() => civilToUTC(today), [today]);
  const date = useMemo(() => civilToUTC(anchor), [anchor]);
  const window = timelineWindow(anchor, scale);
  const scheduled = state.groups.find((g) => g.section === "scheduled");
  const unscheduled = state.groups.find((g) => g.section === "unscheduled");
  const events = useMemo<GanttEvent<TaskScheduleSnapshot>[]>(
    () =>
      (scheduled?.items ?? []).map((item) => ({
        id: item.task.id,
        resourceId: item.task.id,
        title: item.task.title,
        start: civilToUTC(item.task.planned_start_date!),
        end: inclusiveEnd(item.task.planned_finish_date!),
        allDay: true,
        readOnly: scheduleDisabled,
        data: scheduleSnapshot(item.task, state.scope, state.boardRevision!)!,
      })),
    [scheduled, state.scope, state.boardRevision, scheduleDisabled],
  );
  const resources = useMemo(
    () => events.map((e) => ({ id: e.id, title: e.title })),
    [events],
  );
  const proposal = (update: GanttProposedUpdate<TaskScheduleSnapshot>) => {
    const snapshot = update.event.data;
    const dates = ganttDates(update.start, update.end);
    if (
      scheduleDisabled ||
      !snapshot ||
      !dates ||
      (update.resourceId && update.resourceId !== snapshot.taskId)
    )
      return null;
    const item = scheduled?.items.find((i) => i.task.id === snapshot.taskId);
    if (!item || state.boardRevision === null) return null;
    return scheduleIntent(
      snapshot,
      scheduleSnapshot(item.task, state.scope, state.boardRevision),
      dates.start,
      dates.finish,
    );
  };
  const paging = (section: TimelineSection) => {
    const group = state.groups.find((g) => g.section === section);
    const start = state.pageIndex(section) * 50;
    return (
      <div className="flex flex-wrap items-center justify-between gap-3 p-3 text-xs text-muted-foreground">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          {section === "scheduled" && (
            <p
              data-testid="timeline-window"
              className="font-medium text-foreground"
            >
              {window.from} – {window.to}
            </p>
          )}
          <span>
            {group?.items.length
              ? `${start + 1}–${start + group.items.length}`
              : "0"}{" "}
            of {group?.total ?? 0} {section} Tasks
          </span>
          {section === "scheduled" && state.outsideWindowTotal > 0 && (
            <span>{state.outsideWindowTotal} outside this window</span>
          )}
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={
              locked ||
              state.isFetching ||
              state.isError ||
              !state.canPrevious(section)
            }
            onClick={() => state.previousPage(section)}
            aria-label={`Previous ${section} Tasks`}
          >
            Previous
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={
              locked || state.isFetching || state.isError || !group?.next_cursor
            }
            onClick={() => state.nextPage(section)}
            aria-label={`Next ${section} Tasks`}
          >
            Next
          </Button>
        </div>
      </div>
    );
  };
  return (
    <div ref={root} className="min-w-0 space-y-3" data-testid="task-timeline">
      {state.notice && (
        <p role="status" className="text-sm text-muted-foreground">
          {state.notice}
        </p>
      )}
      <section
        aria-label="Scheduled Tasks"
        className="min-w-0 overflow-hidden rounded-xl border bg-card"
      >
        {paging("scheduled")}
        <Gantt<TaskScheduleSnapshot>
          key={`${anchor}:${scale}`}
          events={events}
          resources={resources}
          date={date}
          scale={rendererScale[scale]}
          timeZone="UTC"
          weekStartsOn={1}
          todayDate={todayDate}
          i18n={{ labels: { resources: "Tasks" } }}
          interactions={{
            drag: !scheduleDisabled,
            resize: !scheduleDisabled,
            selectSlot: false,
          }}
          enforceCanDrop
          canDropEvent={(update) => !!proposal(update)}
          onEventUpdate={(update) => {
            const intent = proposal(update);
            if (intent && onReschedule(intent))
              commitTask.current = intent.taskId;
            return false;
          }}
          scheduleMode="single"
          rowCheckboxes={false}
          infiniteScroll={false}
          dragCreate={false}
          displayScheduleHint={false}
          displayCreateTaskHint={false}
          parentScheduling={false}
          summaryBars={false}
          baselineBars={false}
          dependencyLines={false}
          zoomControl={false}
          wheelZoom={false}
          nowIndicator={false}
          initialCenter="anchor"
          scrollbars="native"
          rangeBounds={{
            min: civilToUTC("0001-01-01"),
            max: inclusiveEnd("9999-12-31"),
          }}
          treePanel={{ width: 280, minWidth: 160, maxWidth: 400 }}
          metrics={{ minRowHeight: 7.5 }}
          className="h-[65svh] min-h-96 border-t"
          // Vertical swipes scroll; an intentional horizontal hold moves dates.
          classNames={{
            event: scheduleDisabled ? "touch-auto" : "touch-pan-y",
          }}
          onEventClick={(occurrence) => onOpen(String(occurrence.event.id))}
          renderResourceLabel={({ resource }) => {
            const item = scheduled?.items.find(
              (i) => i.task.id === resource.id,
            );
            return (
              <div className="min-w-0 space-y-1">
                <button
                  type="button"
                  className="block max-w-full truncate text-left font-medium hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={() => onOpen(String(resource.id))}
                  title={resource.title}
                >
                  {resource.title}
                </button>
                {item && (
                  <>
                    <p className="truncate text-[10px] text-muted-foreground">
                      {stageLabels[item.task.status]} ·{" "}
                      {priorityLabels[item.task.priority]}
                    </p>
                    <p className="truncate text-[10px] text-muted-foreground">
                      {scheduleLabel(item.task)}
                    </p>
                    <TaskExecution item={item} />
                  </>
                )}
              </div>
            );
          }}
        >
          <TaskGanttInteraction
            root={root}
            commitTask={commitTask}
            disabled={scheduleDisabled}
            pending={pending}
            scope={`${state.scope}:${state.boardRevision}`}
            onInteracting={onInteracting}
          />
          <GanttView />
        </Gantt>
        {events.length === 0 && (
          <div className="border-t p-6 text-sm text-muted-foreground">
            <p>No scheduled Tasks match this window and your filters.</p>
            <p className="mt-1 text-xs">
              Use Schedule below to add start and finish dates.
            </p>
          </div>
        )}
      </section>
      <section
        aria-label="Unscheduled Tasks"
        className="rounded-xl border bg-card"
      >
        <div className="border-b p-4">
          <h2 className="font-medium">Unscheduled</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Tasks missing a start or finish date, across all dates.
          </p>
        </div>
        {paging("unscheduled")}
        <div className="divide-y">
          {unscheduled?.items.map((item) => (
            <div
              key={item.task.id}
              className="flex flex-wrap items-center justify-between gap-3 p-4"
              data-testid="unscheduled-task"
            >
              <div className="min-w-0 flex-1">
                <button
                  type="button"
                  className="max-w-full break-words text-left text-sm font-medium hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={() => onOpen(item.task.id)}
                >
                  {item.task.title}
                </button>
                <p className="mt-1 text-xs text-muted-foreground">
                  {stageLabels[item.task.status]} ·{" "}
                  {priorityLabels[item.task.priority]}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {scheduleLabel(item.task)}
                </p>
                <TaskExecution item={item} />
              </div>
              <Button
                variant="outline"
                onClick={() => onOpen(item.task.id, true)}
                disabled={locked}
                aria-label={`Schedule ${item.task.title}`}
              >
                Schedule
              </Button>
            </div>
          ))}
        </div>
        {!unscheduled?.items.length && (
          <p className="p-6 text-sm text-muted-foreground">
            No Unscheduled Tasks match your filters.
          </p>
        )}
      </section>
    </div>
  );
}
