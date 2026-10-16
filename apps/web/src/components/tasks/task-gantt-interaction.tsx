import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { useGantt, useGanttSelector } from "@/components/reui/gantt/gantt";
import { cancelActiveGanttGestures } from "@/components/reui/gantt/gantt-dnd";
import { ganttDates } from "@/lib/task-timeline";

// This child subscribes inside the existing Gantt provider; the shared planning
// controller still owns all writes and the navigation/polling lock.
export function TaskGanttInteraction({
  root,
  commitTask,
  disabled,
  pending,
  scope,
  onInteracting,
}: {
  root: RefObject<HTMLDivElement | null>;
  commitTask: RefObject<string | null>;
  disabled: boolean;
  pending: boolean;
  scope: string;
  onInteracting: (value: boolean) => void;
}) {
  const gantt = useGantt();
  const drag = useGanttSelector((s) => s.drag);
  const wasDragging = useRef(false);
  const [announcement, setAnnouncement] = useState("");
  useLayoutEffect(() => {
    onInteracting(!!drag);
    if (drag) {
      const dates = ganttDates(drag.proposedStart, drag.proposedEnd);
      setAnnouncement(
        `${drag.occurrence.event.title}: ${dates ? `${dates.start} – ${dates.finish}` : "Invalid dates"}. Release to save or press Escape to cancel.`,
      );
    } else if (wasDragging.current) {
      setAnnouncement(commitTask.current ? "" : "Schedule change cancelled.");
    }
    wasDragging.current = !!drag;
  }, [drag, onInteracting, commitTask]);
  useLayoutEffect(() => {
    if (disabled) cancelActiveGanttGestures();
  }, [disabled]);
  useLayoutEffect(
    () => () => {
      cancelActiveGanttGestures();
      onInteracting(false);
    },
    [scope, onInteracting],
  );
  useLayoutEffect(() => {
    let geometry: number[] = [];
    const capture = (event: PointerEvent) => {
      if (
        !(event.target instanceof Element) ||
        !root.current?.contains(event.target) ||
        !event.target.closest('[data-slot="gantt-bar"]')
      )
        return;
      const bounds = root.current
        .querySelector('[data-slot="gantt-timeline-pane"]')!
        .getBoundingClientRect();
      geometry = [
        innerWidth,
        innerHeight,
        bounds.x,
        bounds.y,
        bounds.width,
        bounds.height,
      ];
    };
    const outside = (event: PointerEvent) => {
      if (!gantt.getState().drag) return;
      const bounds = root.current
        ?.querySelector('[data-slot="gantt-timeline-pane"]')
        ?.getBoundingClientRect();
      if (
        !bounds ||
        [
          innerWidth,
          innerHeight,
          bounds.x,
          bounds.y,
          bounds.width,
          bounds.height,
        ].some((value, index) => value !== geometry[index]) ||
        event.clientX < bounds.left ||
        event.clientX > bounds.right ||
        event.clientY < bounds.top ||
        event.clientY > bounds.bottom
      )
        cancelActiveGanttGestures();
    };
    window.addEventListener("pointerdown", capture, true);
    window.addEventListener("pointerup", outside, true);
    window.addEventListener("resize", cancelActiveGanttGestures);
    window.addEventListener("wheel", cancelActiveGanttGestures, {
      capture: true,
      passive: true,
    });
    return () => {
      window.removeEventListener("pointerdown", capture, true);
      window.removeEventListener("pointerup", outside, true);
      window.removeEventListener("resize", cancelActiveGanttGestures);
      window.removeEventListener("wheel", cancelActiveGanttGestures, true);
    };
  }, [gantt, root]);
  useLayoutEffect(() => {
    if (pending || disabled || !commitTask.current) return;
    const id = commitTask.current;
    commitTask.current = null;
    const bar = root.current?.querySelector<HTMLButtonElement>(
      `[data-gantt-resource="${CSS.escape(id)}"] [data-slot="gantt-bar"]`,
    );
    (
      bar ?? document.querySelector<HTMLButtonElement>("[data-schedule-today]")
    )?.focus({ preventScroll: true });
    if (!bar) setAnnouncement("The Task is outside this page or date window.");
  }, [pending, disabled, root, commitTask]);
  return (
    <p role="status" className="sr-only">
      {announcement}
    </p>
  );
}
