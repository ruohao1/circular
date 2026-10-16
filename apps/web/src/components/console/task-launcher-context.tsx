import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { api, type Task } from "@/api";
import { scheduleError } from "@/lib/task-schedules";
import { launchTask, LaunchError, validateTaskDraft } from "@/launch";
import { useProject, useProjectSelectionLock } from "@/use-project";
import {
  TaskLauncherDialog,
  type DraftFields,
  type LaunchDraft,
} from "./task-launcher";

const LauncherContext = createContext<{
  openNewTask(stage?: Task["status"]): void;
  openImportedTask(taskId: string): void;
} | null>(null);
const emptyDraft = (
  projectId: string,
  boardStage?: Task["status"],
): LaunchDraft => ({
  projectId,
  repositoryId: "",
  agentId: "",
  title: "",
  description: "",
  priority: "none",
  boardStage,
  planned_start_date: "",
  planned_finish_date: "",
});
export function TaskLauncherProvider({ children }: { children: ReactNode }) {
  const { selectedProject, projects, selectProject } = useProject();
  const [drafts, setDrafts] = useState<Record<string, LaunchDraft>>({});
  const [openKey, setOpenKey] = useState<string>();
  const opener = useRef<HTMLElement | null>(null);
  const client = useQueryClient();
  const navigate = useNavigate();
  const location = useRouterState({ select: (state) => state.location });
  const draft = openKey ? drafts[openKey] : undefined;
  const importedId = openKey?.startsWith("task:")
    ? openKey.slice(5)
    : undefined;
  const imported = useQuery({
    queryKey: ["task", importedId],
    queryFn: () => api.task(importedId!),
    enabled: !!importedId,
    retry: false,
  });
  useProjectSelectionLock(!!openKey);
  const rememberOpener = () => {
    opener.current =
      document.activeElement instanceof HTMLElement &&
      document.activeElement !== document.body
        ? document.activeElement
        : null;
  };
  const openNewTask = useCallback(
    (stage?: Task["status"]) => {
      if (!selectedProject) return;
      rememberOpener();
      const key = stage
        ? `board:${selectedProject}:${stage}`
        : `new:${selectedProject}`;
      setDrafts((current) =>
        current[key]
          ? current
          : { ...current, [key]: emptyDraft(selectedProject, stage) },
      );
      setOpenKey(key);
    },
    [selectedProject],
  );
  const openImportedTask = useCallback((taskId: string) => {
    rememberOpener();
    setOpenKey(`task:${taskId}`);
  }, []);
  useEffect(() => {
    const task = imported.data;
    if (
      !openKey ||
      !importedId ||
      !task ||
      task.id !== importedId ||
      !projects.data?.some((p) => p.id === task.project_id)
    )
      return;
    selectProject(task.project_id);
    setDrafts((current) => {
      const previous = current[openKey];
      if (previous?.partial || previous?.imported === task) return current;
      const preferenceChanged =
        previous?.imported?.preferred_agent_id !== task.preferred_agent_id;
      return {
        ...current,
        [openKey]: {
          ...(previous ?? emptyDraft(task.project_id)),
          imported: task,
          title: task.title,
          description: task.description,
          repositoryId: task.repository_id ?? "",
          repositoryInitialized: true,
          priority: task.priority ?? "none",
          ...(preferenceChanged && !previous?.agentExplicitlySelected
            ? { agentInitialized: false }
            : {}),
        },
      };
    });
  }, [openKey, importedId, imported.data, projects.data, selectProject]);
  const project = projects.data?.find((p) => p.id === draft?.projectId);
  const projectId = draft?.projectId ?? "";
  const repositories = useQuery({
    queryKey: ["repositories", projectId],
    queryFn: () => api.repositories(projectId),
    enabled: !!projectId && !!openKey,
  });
  const agents = useQuery({
    queryKey: ["agents", projectId],
    queryFn: () => api.agents(projectId),
    enabled: !!projectId && !!openKey,
  });
  const circular = draft?.imported?.external_refs?.circular;
  const discovery =
    !!circular &&
    typeof circular === "object" &&
    "kind" in circular &&
    circular.kind === "repository-discovery";
  const suggested =
    discovery && "agent_id" in circular && typeof circular.agent_id === "string"
      ? circular.agent_id
      : undefined;
  // Persist actual initial IDs. Once selected, removal/disablement cannot silently
  // redirect a draft to a different Repository or Agent after a refetch.
  useEffect(() => {
    if (!openKey || !draft || draft.partial) return;
    const initRepository =
      !draft.repositoryInitialized && repositories.isSuccess;
    const initAgent = !draft.agentInitialized && agents.isSuccess;
    if (!initRepository && !initAgent) return;
    const repositoryId =
      initRepository && !draft.imported
        ? (repositories.data?.[0]?.id ?? "")
        : draft.repositoryId;
    const enabled = agents.data?.filter((a) => a.enabled);
    const agentId = initAgent
      ? (suggested ??
        draft.imported?.preferred_agent_id ??
        (enabled?.find((a) => !a.preset) ?? enabled?.[0])?.id ??
        "")
      : draft.agentId;
    setDrafts((current) => ({
      ...current,
      [openKey]: {
        ...current[openKey],
        repositoryId,
        agentId,
        ...(initRepository ? { repositoryInitialized: true } : {}),
        ...(initAgent ? { agentInitialized: true } : {}),
      },
    }));
  }, [
    openKey,
    draft,
    repositories.data,
    repositories.isSuccess,
    agents.data,
    agents.isSuccess,
    suggested,
  ]);
  const repository =
    draft?.partial?.repository ??
    repositories.data?.find((r) => r.id === draft?.repositoryId);
  const agent =
    draft?.partial?.agent ??
    agents.data?.find((a) => a.id === draft?.agentId && a.enabled);
  const importedRefreshing =
    !!importedId && (imported.isFetching || draft?.imported !== imported.data);
  const canLaunch =
    !importedRefreshing &&
    !!draft &&
    (!importedId || !!draft.agentInitialized) &&
    !!project &&
    (!!draft.partial ||
      (!!repository &&
        !!agent &&
        !!draft.title.trim() &&
        Array.from(draft.title.trim()).length <= 500));
  const canSave =
    !!draft &&
    !draft.imported &&
    !draft.partial &&
    !!project &&
    !!draft.title.trim() &&
    Array.from(draft.title.trim()).length <= 500 &&
    (!draft.boardStage ||
      !scheduleError(draft.planned_start_date, draft.planned_finish_date));
  const clearImportSearch = () => {
    if (location.pathname === "/" && "taskId" in location.search) {
      void navigate({
        to: "/",
        search: (previous) => ({ ...previous, taskId: undefined }),
        replace: true,
      });
    }
  };
  const launch = useMutation({
    mutationKey: ["task-launch"],
    mutationFn: async () => {
      if (!openKey || !draft || !project || !repository || !agent)
        throw new Error("Select a Project, Repository, and enabled Agent.");
      const key = openKey;
      try {
        const run = draft.partial
          ? await api.createRun({
              task_id: draft.partial.taskId,
              agent_id: draft.partial.agent.id,
            })
          : draft.imported
            ? await api.createRun({
                task_id: draft.imported.id,
                agent_id: agent.id,
              })
            : await launchTask({
                project,
                repository,
                agent,
                title: draft.title,
                description: draft.description,
                priority: draft.priority,
              });
        return { run, key, projectId: project.id };
      } catch (error) {
        setDrafts((current) => ({
          ...current,
          [key]: {
            ...current[key],
            error:
              error instanceof Error ? error.message : "Run could not start",
            ...(error instanceof LaunchError
              ? { partial: { taskId: error.taskId, agent, repository } }
              : {}),
          },
        }));
        throw error;
      }
    },
    onSuccess: ({ run, key, projectId }) => {
      setDrafts((current) => {
        const next = { ...current };
        delete next[key];
        return next;
      });
      setOpenKey(undefined);
      void client.invalidateQueries({ queryKey: ["run-queue", projectId] });
      void client.invalidateQueries({ queryKey: ["runs", projectId] });
      void client.invalidateQueries({ queryKey: ["task-planning", projectId] });
      void navigate({ to: "/runs/$runId", params: { runId: run.id } });
    },
  });
  const save = useMutation({
    mutationKey: ["task-save"],
    mutationFn: async () => {
      if (!openKey || !draft || !project || draft.imported || draft.partial)
        throw new Error("Enter a new Task.");
      const key = openKey;
      try {
        const invalidSchedule =
          draft.boardStage &&
          scheduleError(draft.planned_start_date, draft.planned_finish_date);
        if (invalidSchedule) throw new Error(invalidSchedule);
        const task = await api.createTask({
          ...validateTaskDraft(
            {
              title: draft.title,
              description: draft.description,
              repository_id: draft.repositoryId || null,
              preferred_agent_id: draft.agentId || null,
              priority: draft.priority,
            },
            project,
            repositories.data ?? [],
            agents.data ?? [],
          ),
          ...(draft.boardStage
            ? {
                status: draft.boardStage,
                planned_start_date: draft.planned_start_date || null,
                planned_finish_date: draft.planned_finish_date || null,
              }
            : {}),
        });
        return { task, key, fromBoard: !!draft.boardStage };
      } catch (error) {
        setDrafts((current) => ({
          ...current,
          [key]: {
            ...current[key],
            error:
              error instanceof Error
                ? error.message
                : "Task could not be saved",
          },
        }));
        throw error;
      }
    },
    onSuccess: async ({ task, key, fromBoard }) => {
      await Promise.all([
        client.invalidateQueries({
          queryKey: ["task-planning", task.project_id],
        }),
        client.invalidateQueries({
          queryKey: ["task-timeline", task.project_id],
        }),
      ]);
      setDrafts((current) => {
        const next = { ...current };
        delete next[key];
        return next;
      });
      setOpenKey(undefined);
      if (fromBoard) return;
      void navigate({
        to: "/tasks",
        search: { view: "list", taskId: task.id },
        state: { taskPlanningProject: task.project_id },
      });
    },
  });
  const pending = launch.isPending || save.isPending;
  function close() {
    if (pending) return;
    setOpenKey(undefined);
    clearImportSearch();
  }
  function change(patch: Partial<DraftFields>) {
    if (!openKey || !draft || pending || draft.partial) return;
    // Imported inputs belong to the saved Task. Select's form synchronization
    // can emit an empty value before its async options arrive, even if disabled.
    if (draft.imported)
      patch = "agentId" in patch ? { agentId: patch.agentId } : {};
    if (!Object.keys(patch).length) return;
    setDrafts((current) => ({
      ...current,
      [openKey]: {
        ...current[openKey],
        ...patch,
        ...("agentId" in patch
          ? { agentInitialized: true, agentExplicitlySelected: true }
          : {}),
        ...("repositoryId" in patch ? { repositoryInitialized: true } : {}),
        error: undefined,
      },
    }));
  }
  const importError =
    importedId && projects.error
      ? `Could not load this Task’s Project: ${projects.error.message}`
      : importedId && imported.error
        ? `Could not load this Task: ${imported.error.message}`
        : importedId &&
            imported.data &&
            projects.isSuccess &&
            !projects.data.some((p) => p.id === imported.data.project_id)
          ? "The Task’s Project is unavailable."
          : undefined;
  const missingRepository =
    draft?.imported && repositories.isSuccess && !repository
      ? "The Task’s repository is unavailable in this project."
      : undefined;
  const optionsError =
    repositories.error || agents.error
      ? "Could not load launch options. Try again."
      : undefined;
  const retryLoading =
    (importedId && (projects.error || imported.error)) || optionsError
      ? () => {
          if (projects.error) void projects.refetch();
          if (importedId && imported.error) void imported.refetch();
          if (repositories.error) void repositories.refetch();
          if (agents.error) void agents.refetch();
        }
      : undefined;
  return (
    <LauncherContext.Provider value={{ openNewTask, openImportedTask }}>
      {children}
      <TaskLauncherDialog
        open={!!openKey}
        pending={pending}
        saving={save.isPending}
        canSave={canSave && !importError}
        onSave={() => {
          if (canSave && !pending) save.mutate();
        }}
        loading={
          !!openKey &&
          !importError &&
          !optionsError &&
          (!draft ||
            importedRefreshing ||
            repositories.isPending ||
            agents.isPending)
        }
        project={project}
        draft={draft}
        repositories={repositories.data ?? []}
        agents={agents.data ?? []}
        discovery={discovery}
        canLaunch={canLaunch && !importError}
        error={importError || missingRepository || draft?.error}
        optionsError={optionsError}
        onRetry={retryLoading}
        onDraftChange={change}
        onSubmit={() => {
          if (canLaunch && !pending) launch.mutate();
        }}
        onOpenChange={(open) => {
          if (!open) close();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          const target = opener.current?.isConnected
            ? opener.current
            : (opener.current?.id &&
                document.getElementById(opener.current.id)) ||
              document.getElementById("new-task");
          target?.focus();
        }}
        onSetup={(section) => {
          close();
          void navigate({ to: "/setup", search: { section } });
        }}
        onStartAnother={() => {
          if (!draft?.partial || launch.isPending) return;
          const key = `new:${draft.projectId}`;
          setDrafts((current) => ({
            ...current,
            [key]: {
              ...emptyDraft(draft.projectId),
              savedTaskId: draft.partial!.taskId,
            },
          }));
          setOpenKey(key);
          clearImportSearch();
        }}
      />
    </LauncherContext.Provider>
  );
}
export function useTaskLauncher() {
  const context = useContext(LauncherContext);
  if (!context) throw new Error("TaskLauncherProvider is required");
  return context;
}
