import { useEffect, useRef, useState } from "react";
import {
  useIsMutating,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  api,
  ApiError,
  type Task,
  type TaskMove,
  type TaskPlanningDetail,
  type TaskPlanningPage,
  type TaskPlanningUpdate,
} from "@/api";
import {
  scheduleError,
  schedulePatch,
  type TaskPlanningDraft,
} from "@/lib/task-schedules";
import {
  mergeTaskPlanningPage,
  taskPlanningDetailOptions,
  taskPlanningOptions,
  type TaskPlanningSearch,
} from "@/lib/task-planning";

import { useTaskTimeline } from "./use-task-timeline";
import {
  scheduleIntent,
  scheduleSnapshot,
  type TaskScheduleIntent,
} from "@/lib/task-timeline";

function taskDraft(task: Task): TaskPlanningDraft {
  return {
    title: task.title,
    description: task.description,
    repository_id: task.repository_id ?? null,
    preferred_agent_id: task.preferred_agent_id ?? null,
    priority: task.priority,
    planned_start_date: task.planned_start_date ?? "",
    planned_finish_date: task.planned_finish_date ?? "",
  };
}
type DraftState = {
  id: string;
  base: TaskPlanningDetail;
  value: TaskPlanningDraft;
};
type Write = { id: string; project: string } & (
  | { kind: "edit"; input: TaskPlanningUpdate }
  | { kind: "move"; input: TaskMove }
  | {
      kind: "schedule";
      input: TaskPlanningUpdate;
      before: { planned_start_date: string; planned_finish_date: string };
      undo?: boolean;
    }
);
export function useTaskPlanning(projectId: string, search: TaskPlanningSearch) {
  const client = useQueryClient();
  const [interacting, setInteracting] = useState(false);
  const scheduleInFlight = useRef(false);
  const [scheduleUndo, setScheduleUndo] =
    useState<Extract<Write, { kind: "schedule" }>>();
  useEffect(() => setScheduleUndo(undefined), [projectId]);
  const pending = useIsMutating({ mutationKey: ["task-planning-write"] }) > 0;
  const query = {
    scope: search.scope,
    q: search.q,
    status: search.status || undefined,
    priority: search.priority || undefined,
    repository_id: search.repository_id,
    preferred_agent_id: search.preferred_agent_id,
  };
  const options = taskPlanningOptions(projectId, query);
  const scope = JSON.stringify(options.queryKey);
  const current = useRef({ scope, taskId: search.taskId, projectId });
  current.current = { scope, taskId: search.taskId, projectId };
  const collection = useQuery({
    ...options,
    enabled: !!projectId && search.view !== "timeline",
    refetchInterval: interacting || pending ? false : 15000,
    refetchOnWindowFocus: !interacting && !pending,
  });
  const timeline = useTaskTimeline({
    projectId,
    search,
    enabled: search.view === "timeline",
    paused: interacting || pending,
  });
  const activeCollection = search.view === "timeline" ? timeline : collection;
  const detailQuery = useQuery({
    ...taskPlanningDetailOptions(search.taskId),
    refetchInterval: interacting || pending ? false : 15000,
    refetchOnWindowFocus: !interacting && !pending,
  });
  const detail =
    detailQuery.data?.task.id === search.taskId ? detailQuery.data : undefined;
  const [loaded, setLoaded] = useState<{
    scope: string;
    page: TaskPlanningPage;
    requests: { status: Task["status"]; cursor: string }[];
  }>();
  const [loadingGroups, setLoadingGroups] = useState<string[]>([]);
  const [notice, setNotice] = useState<{ scope: string; text: string }>();
  const [draftState, setDraftState] = useState<DraftState>();
  const [failure, setFailure] = useState<{
    id: string;
    message: string;
    conflict: boolean;
  }>();
  // Leaving a detail has already passed the router's discard guard. A later
  // reopening starts from saved data, never the explicitly discarded draft.
  useEffect(() => {
    setDraftState(undefined);
    setFailure(undefined);
  }, [search.taskId]);
  const draftRecord = draftState?.id === search.taskId ? draftState : undefined;
  const draft =
    draftRecord?.value ?? (detail ? taskDraft(detail.task) : undefined);
  const dirty =
    !!draftRecord &&
    JSON.stringify(draftRecord.value) !==
      JSON.stringify(taskDraft(draftRecord.base.task));
  const base = dirty ? draftRecord!.base : detail;
  useEffect(() => {
    if (detail && !dirty)
      setDraftState({
        id: detail.task.id,
        base: detail,
        value: taskDraft(detail.task),
      });
  }, [detail, dirty]);
  useEffect(() => {
    setLoadingGroups([]);
    setInteracting(false);
  }, [scope]);
  const initial = collection.data;
  useEffect(() => {
    if (
      search.view !== "timeline" &&
      loaded?.scope === scope &&
      initial &&
      loaded.page.board_revision !== initial.board_revision
    ) {
      setLoaded(undefined);
      setNotice({
        scope,
        text: "Tasks changed. Loaded pages were refreshed; load more to continue.",
      });
    }
  }, [scope, initial?.board_revision, loaded, search.view]);
  // Run activity does not change board_revision. Refresh every loaded page
  // after each idle first-page poll so visible tail summaries stay current too.
  useEffect(() => {
    if (
      search.view === "timeline" ||
      !initial ||
      loaded?.scope !== scope ||
      loaded.page.board_revision !== initial.board_revision ||
      interacting ||
      pending
    )
      return;
    let cancelled = false;
    const revision = initial.board_revision;
    void Promise.all(
      loaded.requests.map(({ status, cursor }) =>
        api.taskPlanning(projectId, { ...query, status, cursor }),
      ),
    )
      .then((pages) => {
        if (cancelled || current.current.scope !== scope) return;
        if (pages.some((p) => p.board_revision !== revision))
          throw new ApiError("Task list changed; reloading all groups.", 409);
        const items = new Map(
          pages.flatMap((p) =>
            p.groups.flatMap((g) =>
              g.items.map((item) => [item.task.id, item] as const),
            ),
          ),
        );
        setLoaded((previous) =>
          previous?.scope === scope && previous.page.board_revision === revision
            ? {
                ...previous,
                page: {
                  ...previous.page,
                  groups: previous.page.groups.map((g) => ({
                    ...g,
                    items: g.items.map(
                      (item) => items.get(item.task.id) ?? item,
                    ),
                  })),
                },
              }
            : previous,
        );
      })
      .catch((error) => {
        if (cancelled || current.current.scope !== scope) return;
        setNotice({
          scope,
          text: `Loaded Tasks could not refresh. ${error instanceof Error ? error.message : "Try again."}`,
        });
        if (error instanceof ApiError && error.status === 409) {
          setLoaded(undefined);
          void collection.refetch();
        }
      });
    return () => {
      cancelled = true;
    };
  }, [
    scope,
    collection.dataUpdatedAt,
    loaded?.requests,
    interacting,
    pending,
    search.view,
  ]);
  // Loaded tails belong to this exact filter and revision. A fresh first page
  // replaces its own rows (including live Run summaries) without losing tails.
  const page =
    initial &&
    loaded?.scope === scope &&
    loaded.page.board_revision === initial.board_revision
      ? {
          ...initial,
          groups: initial.groups.map((g) => {
            const tail = loaded.page.groups.find((x) => x.status === g.status);
            const ids = new Set(g.items.map((i) => i.task.id));
            return {
              ...g,
              items: [
                ...g.items,
                ...(tail?.items.filter((i) => !ids.has(i.task.id)) ?? []),
              ],
              next_cursor: tail ? tail.next_cursor : g.next_cursor,
            };
          }),
        }
      : initial;
  async function refresh(project = projectId, id = search.taskId) {
    await Promise.all([
      client.invalidateQueries({ queryKey: ["task-planning", project] }),
      client.invalidateQueries({ queryKey: ["task-timeline", project] }),
      client.invalidateQueries({ queryKey: ["task-planning-detail", id] }),
      client.invalidateQueries({ queryKey: ["task", id] }),
      client.invalidateQueries({ queryKey: ["tasks", project] }),
      client.invalidateQueries({ queryKey: ["run-queue", project] }),
    ]);
  }
  const mutation = useMutation({
    mutationKey: ["task-planning-write"],
    mutationFn: (write: Write) =>
      write.kind === "move"
        ? api.moveTask(write.id, write.input)
        : api.updateTaskPlanning(write.id, write.input),
    onSuccess: async (result, write) => {
      if (
        current.current.taskId === write.id &&
        current.current.projectId === write.project
      ) {
        setDraftState(undefined);
        setFailure(undefined);
      }
      await refresh(write.project, write.id);
      if (current.current.projectId === write.project) {
        setScheduleUndo(
          write.kind === "schedule" && !write.undo
            ? {
                ...write,
                undo: true,
                input: {
                  expected_revision: result.task.revision,
                  expected_board_revision: result.board_revision,
                  ...write.before,
                },
              }
            : undefined,
        );
        setNotice({
          scope: current.current.scope,
          text:
            write.kind === "schedule"
              ? write.undo
                ? "Schedule change undone."
                : `Schedule saved: ${result.task.planned_start_date} – ${result.task.planned_finish_date}.`
              : write.kind === "edit"
                ? "Task saved."
                : "Task moved.",
        });
      }
    },
    onError: async (error, write) => {
      if (current.current.projectId === write.project) {
        setFailure({
          id: write.id,
          message: error.message,
          conflict: error instanceof ApiError && error.status === 409,
        });
        if (write.kind === "move")
          setNotice({
            scope: current.current.scope,
            text: `Move could not be saved. ${error.message}`,
          });
        if (write.kind === "schedule") {
          setScheduleUndo(undefined);
          setNotice({
            scope: current.current.scope,
            text: `${write.undo ? "Undo" : "Schedule change"} could not be saved. ${error.message} Dates were refreshed; review them before trying again.`,
          });
        }
      }
      if (write.kind === "schedule") {
        await refresh(write.project, write.id);
        return;
      }
      await client.invalidateQueries({
        queryKey: ["task-timeline", write.project],
      });
      // No optimistic rollback or automatic replay. Drafts retain their base.
      await client.invalidateQueries({
        queryKey: ["task-planning", write.project],
      });
    },
    onSettled: () => {
      scheduleInFlight.current = false;
    },
  });
  function change(patch: Partial<TaskPlanningDraft>) {
    if (!base || !draft || pending) return;
    setDraftState({ id: base.task.id, base, value: { ...draft, ...patch } });
  }
  function save() {
    if (
      !base ||
      !draft ||
      pending ||
      !dirty ||
      scheduleError(draft.planned_start_date, draft.planned_finish_date) ||
      (failure?.id === base.task.id && failure.conflict)
    )
      return;
    const original = taskDraft(base.task);
    const input: TaskPlanningUpdate = {
      expected_revision: base.task.revision,
      ...schedulePatch(base.task, draft, base.board_revision),
    };
    for (const key of [
      "title",
      "description",
      "priority",
      "repository_id",
      "preferred_agent_id",
    ] as const) {
      if (draft[key] !== original[key])
        Object.assign(input, {
          [key]: key === "title" ? draft.title.trim() : draft[key],
        });
    }
    mutation.mutate({
      kind: "edit",
      id: base.task.id,
      project: base.task.project_id,
      input,
    });
  }
  function move(id: string, input: TaskMove) {
    if (
      pending ||
      dirty ||
      activeCollection.isError ||
      activeCollection.isFetching
    )
      return;
    mutation.mutate({ kind: "move", id, project: projectId, input });
  }
  const scheduleBlocked =
    pending || dirty || activeCollection.isError || activeCollection.isFetching;
  function reschedule(intent: TaskScheduleIntent): boolean {
    if (
      scheduleBlocked ||
      scheduleInFlight.current ||
      search.view !== "timeline" ||
      timeline.boardRevision === null
    )
      return false;
    const task = timeline.groups
      .find((g) => g.section === "scheduled")
      ?.items.find((i) => i.task.id === intent.taskId)?.task;
    const valid =
      task &&
      scheduleIntent(
        intent,
        scheduleSnapshot(task, timeline.scope, timeline.boardRevision),
        intent.nextStartDate,
        intent.nextFinishDate,
      );
    if (!valid || valid.projectId !== projectId) return false;
    scheduleInFlight.current = true;
    setNotice({ scope, text: "Saving schedule…" });
    mutation.mutate({
      kind: "schedule",
      id: valid.taskId,
      project: valid.projectId,
      before: {
        planned_start_date: valid.plannedStartDate,
        planned_finish_date: valid.plannedFinishDate,
      },
      input: {
        expected_revision: valid.revision,
        expected_board_revision: valid.boardRevision,
        planned_start_date: valid.nextStartDate,
        planned_finish_date: valid.nextFinishDate,
      },
    });
    return true;
  }
  function undoSchedule() {
    if (
      scheduleBlocked ||
      interacting ||
      scheduleInFlight.current ||
      scheduleUndo?.project !== projectId
    )
      return;
    scheduleInFlight.current = true;
    setNotice({ scope, text: "Restoring schedule…" });
    mutation.mutate(scheduleUndo);
  }
  async function reload() {
    if (pending) return;
    setDraftState(undefined);
    setFailure(undefined);
    await refresh();
  }
  async function loadMore(status: Task["status"]) {
    const group = page?.groups.find((g) => g.status === status);
    if (
      search.view === "timeline" ||
      !page ||
      !group?.next_cursor ||
      pending ||
      interacting ||
      loadingGroups.includes(status)
    )
      return;
    const captured = { scope, page };
    setLoadingGroups((groups) => [...groups, status]);
    try {
      const next = await client.fetchQuery(
        taskPlanningOptions(projectId, {
          ...query,
          status,
          cursor: group.next_cursor,
        }),
      );
      if (current.current.scope !== captured.scope) return;
      const merged = mergeTaskPlanningPage(captured.page, next);
      if (!merged)
        throw new ApiError("Task list changed; reloading all groups.", 409);
      setLoaded((previous) => {
        const base =
          previous?.scope === captured.scope &&
          previous.page.board_revision === captured.page.board_revision
            ? previous.page
            : captured.page;
        return {
          scope: captured.scope,
          page: mergeTaskPlanningPage(base, next) ?? merged,
          requests: [
            ...(previous?.scope === captured.scope &&
            previous.page.board_revision === captured.page.board_revision
              ? previous.requests
              : []),
            { status, cursor: group.next_cursor! },
          ],
        };
      });
    } catch (error) {
      if (current.current.scope !== captured.scope) return;
      setNotice({
        scope,
        text:
          error instanceof Error ? error.message : "Could not load more Tasks",
      });
      if (error instanceof ApiError && error.status === 409) {
        setLoaded(undefined);
        await collection.refetch();
      }
    } finally {
      if (current.current.scope === captured.scope)
        setLoadingGroups((groups) => groups.filter((s) => s !== status));
    }
  }
  return {
    page,
    collection,
    timeline,
    activeCollection,
    detail,
    detailQuery,
    draft,
    dirty,
    pending,
    interacting,
    setInteracting,
    loadingGroups,
    loadMore,
    change,
    save,
    move,
    reschedule,
    undoSchedule,
    hasScheduleUndo: scheduleUndo?.project === projectId,
    canUndoSchedule:
      scheduleUndo?.project === projectId && !scheduleBlocked && !interacting,
    scheduleBlocked,
    reload,
    refresh,
    notice: notice?.scope === scope ? notice.text : undefined,
    error: failure?.id === search.taskId ? failure : undefined,
  };
}
