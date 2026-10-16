import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChartGantt,
  Columns3,
  List,
  Search,
  SlidersHorizontal,
} from "lucide-react";
import { Popover } from "radix-ui";
import {
  useBlocker,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import { api } from "@/api";
import { useProject, useProjectSelectionLock } from "@/use-project";
import {
  parseTaskPlanningSearch,
  priorityLabels,
  stageLabels,
  taskPriorities,
  taskStages,
  type TaskPlanningSearch,
} from "@/lib/task-planning";
import { useTaskPlanning } from "@/components/tasks/use-task-planning";
import { useTaskLauncher } from "@/components/console/task-launcher-context";
import {
  TaskTimeline,
  TaskTimelineControls,
} from "@/components/tasks/task-timeline";
import { localToday } from "@/lib/task-timeline";
import { TaskBoard } from "@/components/tasks/task-board";
import { TaskList } from "@/components/tasks/task-list";
import { TaskDetail } from "@/components/tasks/task-detail";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";

declare module "@tanstack/react-router" {
  interface HistoryState {
    taskPlanningProject?: string;
  }
}
export function TasksPage() {
  const { openNewTask } = useTaskLauncher();
  const { selectedProject, selectProject } = useProject();
  const navigate = useNavigate();
  const location = useRouterState({ select: (s) => s.location });
  const urlSearch = useMemo(
    () => parseTaskPlanningSearch(location.search),
    [location.search],
  );
  const foreignHistory =
    !!selectedProject &&
    !!location.state.taskPlanningProject &&
    location.state.taskPlanningProject !== selectedProject;
  const search = foreignHistory
    ? { ...urlSearch, repository_id: "", preferred_agent_id: "", taskId: "" }
    : urlSearch;
  const planning = useTaskPlanning(selectedProject, search);
  const projectOfDetail = planning.detail?.task.project_id;
  const previousProject = useRef(selectedProject);
  const alignedTask = useRef("");
  const [scheduleFocus, setScheduleFocus] = useState("");
  const [query, setQuery] = useState(search.q);
  const taskViews = useRef<HTMLDivElement>(null);
  const undoFocus = useRef(false);
  const repositories = useQuery({
    queryKey: ["repositories", selectedProject],
    queryFn: () => api.repositories(selectedProject),
    enabled: !!selectedProject,
  });
  const agents = useQuery({
    queryKey: ["agents", selectedProject],
    queryFn: () => api.agents(selectedProject),
    enabled: !!selectedProject,
  });
  const change = (patch: Partial<TaskPlanningSearch>, replace = false) =>
    void navigate({
      to: "/tasks",
      search: { ...search, ...patch },
      state: { taskPlanningProject: selectedProject },
      replace,
    });
  useEffect(() => {
    if (search.view === "timeline" && !search.date)
      change({ date: localToday() }, true);
  }, [search.view, search.date, selectedProject]);
  const locked = planning.pending || planning.interacting;
  useEffect(() => {
    if (!locked && undoFocus.current && !planning.hasScheduleUndo) {
      undoFocus.current = false;
      taskViews.current
        ?.querySelector<HTMLButtonElement>('[aria-selected="true"]')
        ?.focus();
    }
  }, [locked, planning.hasScheduleUndo]);
  useProjectSelectionLock(
    locked ||
      planning.dirty ||
      (!!search.taskId &&
        (planning.detailQuery.isPending ||
          (!!projectOfDetail && projectOfDetail !== selectedProject))),
  );
  useBlocker({
    enableBeforeUnload: planning.dirty || planning.pending,
    shouldBlockFn: ({ next }) => {
      if (locked) return true;
      if (
        !planning.dirty ||
        (next.pathname === "/tasks" &&
          "taskId" in next.search &&
          next.search.taskId === search.taskId)
      )
        return false;
      return !window.confirm("Discard unsaved Task changes?");
    },
  });
  useEffect(() => {
    if (
      projectOfDetail &&
      alignedTask.current !== search.taskId &&
      !foreignHistory
    ) {
      alignedTask.current = search.taskId;
      previousProject.current = projectOfDetail;
      if (projectOfDetail !== selectedProject) selectProject(projectOfDetail);
      return;
    }
    if (
      foreignHistory ||
      (previousProject.current && previousProject.current !== selectedProject)
    ) {
      previousProject.current = selectedProject;
      alignedTask.current = "";
      void navigate({
        to: "/tasks",
        search: {
          ...urlSearch,
          taskId: "",
          repository_id: "",
          preferred_agent_id: "",
        },
        state: { taskPlanningProject: selectedProject },
        replace: true,
      });
    } else if (selectedProject) previousProject.current = selectedProject;
  }, [
    selectedProject,
    projectOfDetail,
    search.taskId,
    foreignHistory,
    selectProject,
    navigate,
    urlSearch,
  ]);
  useEffect(() => setQuery(search.q), [search.q]);
  useEffect(() => {
    if (query === search.q) return;
    const timer = setTimeout(
      () =>
        change({ q: Array.from(query.trim()).slice(0, 200).join("") }, true),
      250,
    );
    return () => clearTimeout(timer);
  }, [query, search, selectedProject, navigate]);
  const close = () => change({ taskId: "" });
  const filterCount = [
    search.status,
    search.priority,
    search.repository_id,
    search.preferred_agent_id,
  ].filter(Boolean).length;
  return (
    <div className="min-w-0 space-y-3 p-3 sm:p-4">
      <Tabs
        value={search.view}
        onValueChange={(view) =>
          change({
            view:
              view === "timeline"
                ? "timeline"
                : view === "board"
                  ? "board"
                  : "list",
          })
        }
      >
        <div
          role="group"
          aria-label="Task controls"
          className="flex flex-wrap items-center gap-2 border-b pb-2"
        >
          <div className="flex items-center gap-4">
            <h1 className="text-base font-semibold tracking-tight">Tasks</h1>
            <TabsList ref={taskViews} aria-label="Task views" variant="line">
              <TabsTrigger value="list" disabled={locked}>
                <List aria-hidden="true" />
                List
              </TabsTrigger>
              <TabsTrigger value="board" disabled={locked}>
                <Columns3 aria-hidden="true" />
                Kanban
              </TabsTrigger>
              <TabsTrigger value="timeline" disabled={locked}>
                <ChartGantt aria-hidden="true" />
                Gantt
              </TabsTrigger>
            </TabsList>
          </div>
          {search.view === "timeline" && search.date && (
            <TaskTimelineControls
              anchor={search.date}
              scale={search.scale}
              locked={locked}
              onChange={change}
            />
          )}
          <div className="relative min-w-32 flex-1 sm:ml-auto sm:max-w-48">
            <Search
              aria-hidden="true"
              className="pointer-events-none absolute left-2.5 top-2 size-4 text-muted-foreground"
            />
            <Input
              aria-label="Search Tasks"
              placeholder="Search Tasks…"
              className="h-8 pl-8"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              disabled={locked}
            />
          </div>
          <Popover.Root>
            <Popover.Trigger asChild>
              <Button
                variant={filterCount ? "secondary" : "outline"}
                disabled={locked}
              >
                <SlidersHorizontal aria-hidden="true" />
                Filters{filterCount > 0 && ` (${filterCount})`}
              </Button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                aria-label="Task filters"
                align="end"
                sideOffset={8}
                className="z-50 grid max-h-[var(--radix-popover-content-available-height)] w-80 max-w-[calc(100vw-2rem)] gap-4 overflow-y-auto rounded-xl border bg-popover p-4 text-popover-foreground shadow-lg"
              >
                <ResourceSelect
                  id="workflow-filter"
                  label="Workflow filter"
                  value={search.status || "all"}
                  onValueChange={(value) =>
                    change({
                      status: taskStages.find((s) => s === value) ?? "",
                    })
                  }
                  options={[
                    { value: "all", label: "All active stages" },
                    ...taskStages.map((value) => ({
                      value,
                      label: stageLabels[value],
                    })),
                  ]}
                  placeholder="Workflow"
                  disabled={locked}
                />
                <ResourceSelect
                  id="priority-filter"
                  label="Priority filter"
                  value={search.priority || "all"}
                  onValueChange={(value) =>
                    change({
                      priority: taskPriorities.find((p) => p === value) ?? "",
                    })
                  }
                  options={[
                    { value: "all", label: "All priorities" },
                    ...taskPriorities.map((value) => ({
                      value,
                      label: priorityLabels[value],
                    })),
                  ]}
                  placeholder="Priority"
                  disabled={locked}
                />
                <ResourceSelect
                  id="repository-filter"
                  label="Repository filter"
                  value={search.repository_id || "all"}
                  onValueChange={(value) =>
                    change({ repository_id: value === "all" ? "" : value })
                  }
                  options={[
                    { value: "all", label: "All Repositories" },
                    ...(repositories.data ?? []).map((r) => ({
                      value: r.id,
                      label: r.name,
                    })),
                  ]}
                  placeholder="Repository"
                  disabled={locked}
                />
                <ResourceSelect
                  id="agent-filter"
                  label="Preferred Agent filter"
                  value={search.preferred_agent_id || "all"}
                  onValueChange={(value) =>
                    change({ preferred_agent_id: value === "all" ? "" : value })
                  }
                  options={[
                    { value: "all", label: "All Agents" },
                    ...(agents.data ?? []).map((a) => ({
                      value: a.id,
                      label: a.name,
                    })),
                  ]}
                  placeholder="Preferred Agent"
                  disabled={locked}
                />
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
        </div>
        {(repositories.error || agents.error) && (
          <ErrorAlert>
            Filter options could not load.{" "}
            <Button
              variant="link"
              onClick={() => {
                void repositories.refetch();
                void agents.refetch();
              }}
            >
              Retry options
            </Button>
          </ErrorAlert>
        )}
        {planning.activeCollection.error && (
          <ErrorAlert>
            {planning.page && "Showing the last update. "}
            {planning.activeCollection.error.message}{" "}
            <Button
              variant="link"
              onClick={() => void planning.activeCollection.refetch()}
            >
              Retry Tasks
            </Button>
          </ErrorAlert>
        )}
        {(planning.notice || planning.hasScheduleUndo) && (
          <div className="mb-3 flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
            <p role="status">{planning.notice}</p>
            {planning.hasScheduleUndo && (
              <Button
                variant="outline"
                size="sm"
                aria-label="Undo schedule change"
                disabled={!planning.canUndoSchedule}
                onClick={() => {
                  undoFocus.current = true;
                  planning.undoSchedule();
                }}
              >
                Undo
              </Button>
            )}
          </div>
        )}
        {!selectedProject ? (
          <p>Choose a Project to plan its Tasks.</p>
        ) : planning.activeCollection.isPending ? (
          <p role="status">Loading Tasks…</p>
        ) : search.view === "timeline" ? (
          search.date &&
          planning.timeline.boardRevision !== null && (
            <TabsContent value="timeline">
              <TaskTimeline
                state={planning.timeline}
                anchor={search.date}
                scale={search.scale}
                locked={locked}
                scheduleDisabled={planning.scheduleBlocked}
                pending={planning.pending}
                onReschedule={planning.reschedule}
                onInteracting={planning.setInteracting}
                onOpen={(id, schedule) => {
                  setScheduleFocus(schedule ? id : "");
                  change({ taskId: id });
                }}
              />
            </TabsContent>
          )
        ) : (
          planning.page && (
            <>
              <TabsContent value="list">
                <TaskList
                  page={planning.page}
                  onLoadMore={planning.loadMore}
                  loadingGroups={planning.loadingGroups}
                />
              </TabsContent>
              <TabsContent value="board">
                <TaskBoard
                  key={JSON.stringify([
                    selectedProject,
                    search.q,
                    search.status,
                    search.priority,
                    search.repository_id,
                    search.preferred_agent_id,
                  ])}
                  page={planning.page}
                  pending={
                    planning.pending ||
                    planning.dirty ||
                    planning.activeCollection.isError ||
                    planning.activeCollection.isFetching
                  }
                  onMove={planning.move}
                  onAdd={openNewTask}
                  onLoadMore={planning.loadMore}
                  loadingGroups={planning.loadingGroups}
                  onInteracting={planning.setInteracting}
                />
              </TabsContent>
            </>
          )
        )}
      </Tabs>
      {search.taskId && planning.detailQuery.isPending && (
        <p role="status">Loading Task details…</p>
      )}
      {search.taskId && planning.detailQuery.error && (
        <ErrorAlert>
          {planning.detailQuery.error.message}{" "}
          <Button
            variant="link"
            onClick={() => void planning.detailQuery.refetch()}
          >
            Retry detail
          </Button>
          <Button variant="link" onClick={close}>
            Close detail
          </Button>
        </ErrorAlert>
      )}
      {planning.detail &&
        planning.draft &&
        projectOfDetail === selectedProject && (
          <TaskDetail
            key={planning.detail.task.id}
            detail={planning.detail}
            draft={planning.draft}
            dirty={planning.dirty}
            pending={planning.pending}
            repositories={repositories.data ?? []}
            agents={agents.data ?? []}
            error={planning.error}
            onDraftChange={planning.change}
            onSave={planning.save}
            onReload={planning.reload}
            onMove={planning.move}
            moveUnavailable={
              planning.activeCollection.isFetching
                ? "refreshing"
                : planning.activeCollection.isError
                  ? "error"
                  : undefined
            }
            onRetryOrder={() => void planning.activeCollection.refetch()}
            onClose={close}
            view={search.view}
            onViewChange={(view) => change({ view })}
            onShowTimeline={() =>
              change({
                view: "timeline",
                ...(planning.detail!.task.planned_start_date &&
                planning.detail!.task.planned_finish_date
                  ? { date: planning.detail!.task.planned_start_date }
                  : {}),
              })
            }
            focusSchedule={scheduleFocus === search.taskId}
            onScheduleFocused={() => setScheduleFocus("")}
            timelineHidden={
              search.view === "timeline" &&
              !planning.timeline.isPending &&
              !planning.timeline.isError &&
              !planning.timeline.groups.some((g) =>
                g.items.some((i) => i.task.id === search.taskId),
              )
            }
          />
        )}
    </div>
  );
}
