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
import { api } from "@/api";
import { launchTask, LaunchError } from "@/launch";
import { useProject, useProjectSelectionLock } from "@/use-project";
import {
  TaskLauncherDialog,
  type DraftFields,
  type LaunchDraft,
} from "@/components/console/task-launcher";

const LauncherContext = createContext<{
  openNewTask(): void;
  openImportedTask(taskId: string): void;
} | null>(null);
const emptyDraft = (projectId: string): LaunchDraft => ({
  projectId,
  repositoryId: "",
  agentId: "",
  title: "",
  description: "",
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
  const openNewTask = useCallback(() => {
    if (!selectedProject) return;
    rememberOpener();
    const key = `new:${selectedProject}`;
    setDrafts((current) =>
      current[key]
        ? current
        : { ...current, [key]: emptyDraft(selectedProject) },
    );
    setOpenKey(key);
  }, [selectedProject]);
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
    setDrafts((current) =>
      current[openKey]
        ? current
        : {
            ...current,
            [openKey]: {
              ...emptyDraft(task.project_id),
              imported: task,
              title: task.title,
              description: task.description,
              repositoryId: task.repository_id ?? "",
            },
          },
    );
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
    const repositoryId =
      !draft.imported && !draft.repositoryId
        ? repositories.data?.[0]?.id
        : undefined;
    const enabled = agents.data?.filter((a) => a.enabled);
    const agentId = !draft.agentId
      ? suggested
        ? enabled?.find((a) => a.id === suggested)?.id
        : (enabled?.find((a) => !a.preset) ?? enabled?.[0])?.id
      : undefined;
    if (!repositoryId && !agentId) return;
    setDrafts((current) => ({
      ...current,
      [openKey]: {
        ...current[openKey],
        ...(repositoryId ? { repositoryId } : {}),
        ...(agentId ? { agentId } : {}),
      },
    }));
  }, [openKey, draft, repositories.data, agents.data, suggested]);
  const repository =
    draft?.partial?.repository ??
    repositories.data?.find((r) => r.id === draft?.repositoryId);
  const agent =
    draft?.partial?.agent ??
    agents.data?.find((a) => a.id === draft?.agentId && a.enabled);
  const canLaunch =
    !!draft &&
    !!project &&
    (!!draft.partial ||
      (!!repository &&
        !!agent &&
        !!draft.title.trim() &&
        draft.title.trim().length <= 500));
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
      void navigate({ to: "/runs/$runId", params: { runId: run.id } });
    },
  });
  function close() {
    if (launch.isPending) return;
    setOpenKey(undefined);
    clearImportSearch();
  }
  function change(patch: Partial<DraftFields>) {
    if (!openKey || !draft || launch.isPending || draft.partial) return;
    // Imported inputs belong to the saved Task. Select's form synchronization
    // can emit an empty value before its async options arrive, even if disabled.
    if (draft.imported) patch = patch.agentId ? { agentId: patch.agentId } : {};
    if (!Object.keys(patch).length) return;
    setDrafts((current) => ({
      ...current,
      [openKey]: { ...current[openKey], ...patch, error: undefined },
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
        pending={launch.isPending}
        loading={
          !!openKey &&
          !importError &&
          !optionsError &&
          (!draft || repositories.isPending || agents.isPending)
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
          if (canLaunch && !launch.isPending) launch.mutate();
        }}
        onOpenChange={(open) => {
          if (!open) close();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          const target = opener.current?.isConnected
            ? opener.current
            : document.getElementById("new-task");
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
