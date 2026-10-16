import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError, type TaskTimelinePage } from "@/api";
import type { TaskPlanningSearch } from "@/lib/task-planning";
import { timelineWindow, type TimelineSection } from "@/lib/task-timeline";

export type ActivePlanningCollection = {
  isPending: boolean;
  isFetching: boolean;
  isError: boolean;
  error: Error | null;
  refetch: () => Promise<unknown>;
};
export type TaskTimelineState = ActivePlanningCollection & {
  scope: string;
  boardRevision: number | null;
  groups: TaskTimelinePage["groups"];
  outsideWindowTotal: number;
  nextPage: (section: TimelineSection) => void;
  previousPage: (section: TimelineSection) => void;
  canPrevious: (section: TimelineSection) => boolean;
  resetPages: () => void;
  pageIndex: (section: TimelineSection) => number;
  notice?: string;
};
export function useTaskTimeline({
  projectId,
  search,
  enabled,
  paused,
}: {
  projectId: string;
  search: TaskPlanningSearch;
  enabled: boolean;
  paused: boolean;
}): TaskTimelineState {
  const values = {
    ...timelineWindow(search.date ?? "0001-01-01", search.scale),
    q: search.q,
    status: search.status || undefined,
    priority: search.priority || undefined,
    repository_id: search.repository_id,
    preferred_agent_id: search.preferred_agent_id,
    limit: 50,
  };
  const scope = JSON.stringify([projectId, values]);
  const [pagination, setPagination] = useState({
    scope,
    scheduled: [""],
    unscheduled: [""],
    generation: 0,
    notice: "",
  });
  const pages =
    pagination.scope === scope
      ? pagination
      : {
          scope,
          scheduled: [""],
          unscheduled: [""],
          generation: 0,
          notice: "",
        };
  useEffect(() => {
    if (pagination.scope !== scope) setPagination(pages);
  }, [scope]);
  const requests = (["scheduled", "unscheduled"] as const).map((section) => ({
    section,
    cursor: pages[section].at(-1)!,
  }));
  const collection = useQuery({
    queryKey: ["task-timeline", projectId, values, requests, pages.generation],
    queryFn: async ({ signal }): Promise<TaskTimelinePage> => {
      if (requests.every((r) => !r.cursor))
        return api.taskTimeline(projectId, values, signal);
      const results = await Promise.all(
        requests.map((r) =>
          api.taskTimeline(projectId, { ...values, ...r }, signal),
        ),
      );
      if (results[0].board_revision !== results[1].board_revision)
        throw new ApiError("Timeline changed; reload both sections.", 409);
      return { ...results[0], groups: results.flatMap((r) => r.groups) };
    },
    enabled: enabled && !!projectId && !!search.date,
    retry: false,
    gcTime: 0,
    refetchInterval: paused ? false : 15000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: !paused,
  });
  const revision = useRef<{ scope: string; value: number } | undefined>(
    undefined,
  );
  const page = collection.data;
  function resetPages(notice = "") {
    setPagination({
      scope,
      scheduled: [""],
      unscheduled: [""],
      generation: pages.generation + 1,
      notice,
    });
  }
  useEffect(() => {
    if (!enabled) return;
    if (
      collection.error instanceof ApiError &&
      collection.error.status === 409
    ) {
      resetPages("Tasks changed. Both sections returned to their first page.");
      return;
    }
    if (!page) return;
    const previous = revision.current;
    revision.current = { scope, value: page.board_revision };
    if (previous?.scope === scope && previous.value !== page.board_revision) {
      if (pages.scheduled.length > 1 || pages.unscheduled.length > 1)
        resetPages(
          "Tasks changed. Both sections returned to their first page.",
        );
      else
        setPagination({
          ...pages,
          notice:
            pages.notice || "Tasks changed. Both sections were refreshed.",
        });
    }
  }, [scope, enabled, page, collection.error]);
  function navigate(section: TimelineSection, next: boolean) {
    if (paused || collection.isFetching || collection.isError || !page) return;
    const cursor = page.groups.find((g) => g.section === section)?.next_cursor;
    if ((next && !cursor) || (!next && pages[section].length === 1)) return;
    setPagination({
      ...pages,
      [section]: next
        ? [...pages[section], cursor!]
        : pages[section].slice(0, -1),
    });
  }
  return {
    scope: JSON.stringify([scope, search.scale, requests, pages.generation]),
    isPending: collection.isPending,
    isFetching: collection.isFetching,
    isError: collection.isError,
    error: collection.error,
    refetch: collection.refetch,
    boardRevision: page?.board_revision ?? null,
    groups: page?.groups ?? [],
    outsideWindowTotal: page?.outside_window_total ?? 0,
    nextPage: (s) => navigate(s, true),
    previousPage: (s) => navigate(s, false),
    canPrevious: (s) => pages[s].length > 1,
    resetPages: () => resetPages(),
    pageIndex: (s) => pages[s].length - 1,
    notice: pages.notice || undefined,
  };
}
