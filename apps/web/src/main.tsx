import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import {
  ArrowLeft,
  ArrowRight,
  BookOpen,
  CircleDot,
  Download,
  Files,
  Inbox,
  LoaderCircle,
  SlidersHorizontal,
} from "lucide-react";
import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { EmptyState } from "@/components/empty-state";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { RunStatus as Status } from "@/components/run-status";
import { RunReport } from "@/components/run-report";
import { RunAgentProposals } from "@/components/run-agent-proposals";
import { RunLinearDelivery } from "@/components/run-linear-delivery";
import { PRReviewReport } from "@/components/pr-review-report";
import { RunPRReviews } from "@/components/run-pr-reviews";
import { RunGitHubDelivery } from "@/components/run-github-delivery";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { api, type Run } from "./api";
import { LaunchError, launchTask } from "./launch";
import { outputFrom } from "./run-events";
import { useRunEvents } from "./use-run-events";
import { ProjectProvider, useProject } from "./use-project";
import { SetupPage, type SetupSection } from "./pages/setup";
import type { ConnectionNotice } from "./pages/integrations";
import { RequestsPage } from "@/components/external-request-list";
import { RequestPage } from "@/pages/request";
import "./index.css";

const queryClient = new QueryClient();
const terminal = (status: string) =>
  ["succeeded", "failed", "cancelled"].includes(status);

function WorkspaceNavigation({ mobile = false }: { mobile?: boolean }) {
  const inSetup = useRouterState({
    select: (state) => state.location.pathname === "/setup",
  });
  const inRequests = useRouterState({
    select: (state) => state.location.pathname.startsWith("/requests"),
  });
  return (
    <nav
      aria-label="Workspace"
      className={mobile ? "flex flex-wrap gap-1" : "grid gap-1"}
    >
      <Button
        variant={!inSetup && !inRequests ? "secondary" : "ghost"}
        asChild
        className="justify-start"
      >
        <Link
          to="/"
          aria-current={!inSetup && !inRequests ? "page" : undefined}
        >
          <CircleDot aria-hidden="true" />
          Runs
        </Link>
      </Button>
      <Button
        variant={inSetup ? "secondary" : "ghost"}
        asChild
        className="justify-start"
      >
        <Link
          to="/setup"
          search={{ section: "projects" }}
          aria-current={inSetup ? "page" : undefined}
        >
          <SlidersHorizontal aria-hidden="true" />
          Setup
        </Link>
      </Button>
      <Button
        variant={inRequests ? "secondary" : "ghost"}
        asChild
        className="justify-start"
      >
        <Link to="/requests" aria-current={inRequests ? "page" : undefined}>
          <Inbox aria-hidden="true" />
          Requests
        </Link>
      </Button>
      <Button variant="ghost" asChild className="justify-start">
        <Link to="/docs">
          <BookOpen aria-hidden="true" />
          Docs
        </Link>
      </Button>
    </nav>
  );
}

function RootLayout() {
  const inDocs = useRouterState({
    select: (state) =>
      state.location.pathname === "/docs" ||
      state.location.pathname.startsWith("/docs/"),
  });
  if (inDocs) return <Outlet />;
  return (
    <ProjectProvider>
      <div className="min-h-svh sm:grid sm:grid-cols-[190px_minmax(0,1fr)] lg:grid-cols-[210px_minmax(0,1fr)]">
        <aside className="hidden flex-col border-r border-sidebar-border bg-sidebar p-4 text-sidebar-foreground sm:flex">
          <Link
            className="flex items-center gap-2.5 rounded-lg px-2 py-3 text-lg font-semibold tracking-tight outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            to="/"
          >
            <span className="grid size-7 place-items-center rounded-lg bg-primary text-sm text-primary-foreground">
              C
            </span>
            Circular
          </Link>
          <p className="mb-3 mt-9 px-2 text-[10px] font-medium tracking-widest text-muted-foreground">
            WORKSPACE
          </p>
          <WorkspaceNavigation />
          <div className="mt-auto space-y-3 px-2 pt-12">
            <Separator />
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span
                className="size-1.5 rounded-full bg-success"
                aria-hidden="true"
              />{" "}
              Local execution
            </div>
            <p className="text-xs leading-relaxed text-muted-foreground">
              One task. One isolated Run.
            </p>
          </div>
        </aside>
        <main className="min-w-0">
          <div className="flex h-14 items-center justify-between gap-3 border-b bg-sidebar px-4 sm:hidden">
            <Link
              to="/"
              aria-label="Circular home"
              className="grid size-7 place-items-center rounded-lg bg-primary text-sm font-semibold text-primary-foreground"
            >
              C
            </Link>
            <WorkspaceNavigation mobile />
          </div>
          <Outlet />
        </main>
      </div>
    </ProjectProvider>
  );
}

function Overview() {
  const { taskId } = indexRoute.useSearch();
  return <RunOverview key={taskId ?? "new"} taskId={taskId} />;
}

function RunOverview({ taskId }: { taskId?: string }) {
  const navigate = useNavigate();
  const { projects, project, selectedProject, selectProject } = useProject();
  const imported = useQuery({
    queryKey: ["task", taskId],
    queryFn: () => api.task(taskId!),
    enabled: !!taskId,
    retry: false,
  });
  useEffect(() => {
    if (
      imported.data &&
      projects.data?.some((item) => item.id === imported.data.project_id)
    )
      selectProject(imported.data.project_id);
  }, [imported.data, projects.data, selectProject]);
  const [repositoryId, setRepositoryId] = useState("");
  const [agentId, setAgentId] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [partial, setPartial] = useState<{ taskId: string; agentId: string }>();
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
  const runs = useQuery({
    queryKey: ["runs", selectedProject],
    queryFn: () => api.runs(selectedProject),
    enabled: !!selectedProject,
    refetchInterval: 2000,
  });
  const repository = taskId
    ? repositories.data?.find(
        (item) => item.id === imported.data?.repository_id,
      )
    : (repositories.data?.find((item) => item.id === repositoryId) ??
      repositories.data?.[0]);
  const enabledAgents = agents.data?.filter((item) => item.enabled) ?? [];
  const circularRef = imported.data?.external_refs?.circular;
  const discovery =
    circularRef &&
    typeof circularRef === "object" &&
    "kind" in circularRef &&
    circularRef.kind === "repository-discovery";
  const suggestedAgent =
    discovery &&
    "agent_id" in circularRef &&
    typeof circularRef.agent_id === "string"
      ? circularRef.agent_id
      : undefined;
  const agent =
    enabledAgents.find((item) => item.id === agentId) ??
    (suggestedAgent
      ? enabledAgents.find((item) => item.id === suggestedAgent)
      : (enabledAgents.find((item) => !item.preset) ?? enabledAgents[0]));
  const launch = useMutation({
    mutationFn: async () => {
      if (partial)
        return api.createRun({
          task_id: partial.taskId,
          agent_id: partial.agentId,
        });
      if (!project || !repository || !agent)
        throw new Error("Select a project, repository and agent.");
      if (taskId) {
        if (!imported.data || imported.data.project_id !== project.id)
          throw new Error("Wait for the Task’s project to load.");
        return api.createRun({ task_id: imported.data.id, agent_id: agent.id });
      }
      try {
        return await launchTask({
          project,
          repository,
          agent,
          title,
          description,
        });
      } catch (error) {
        if (error instanceof LaunchError)
          setPartial({ taskId: error.taskId, agentId: agent.id });
        throw error;
      }
    },
    onSuccess: (run) => {
      void navigate({ to: "/runs/$runId", params: { runId: run.id } });
    },
  });

  return (
    <>
      <header className="flex h-14 items-center justify-between gap-4 border-b bg-sidebar/50 px-4 text-xs sm:px-6 lg:px-8">
        <span>
          Workspace <span className="text-muted-foreground">/ Runs</span>
        </span>
        <span className="hidden text-muted-foreground sm:inline">
          Execution control plane
        </span>
      </header>
      <div className="mx-auto max-w-[1450px] space-y-7 px-4 py-7 sm:px-6 lg:px-8 lg:py-9">
        <div className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
          <div className="space-y-2">
            <p className="text-[10px] font-medium tracking-[0.18em] text-muted-foreground">
              EXECUTION
            </p>
            <h1 className="text-2xl font-semibold tracking-tight">
              Your work, in motion.
            </h1>
            <p className="text-sm text-muted-foreground">
              Launch a task and follow its execution from start to finish.
            </p>
          </div>
          <ResourceSelect
            id="project"
            label="Project"
            className="w-full md:w-64"
            value={selectedProject}
            disabled={launch.isPending || !!partial || !!taskId}
            onValueChange={(value) => {
              selectProject(value);
              setRepositoryId("");
              setAgentId("");
            }}
            options={(projects.data ?? []).map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            placeholder={
              projects.isPending ? "Loading projects…" : "No projects available"
            }
          />
        </div>
        {projects.error && (
          <ErrorAlert>
            Could not load projects: {projects.error.message}
          </ErrorAlert>
        )}
        {projects.isSuccess && !projects.data.length ? (
          <Card>
            <EmptyState
              title="No projects yet"
              icon={<Files aria-hidden="true" />}
              description={
                <>
                  <span>
                    Create a project and add a repository. The included
                    discovery agent can help you understand the codebase and
                    plan your first tasks.
                  </span>
                  <Button asChild variant="link" className="mt-2">
                    <Link to="/setup" search={{ section: "projects" }}>
                      Open setup <ArrowRight aria-hidden="true" />
                    </Link>
                  </Button>
                </>
              }
            />
          </Card>
        ) : (
          <Card className="max-w-4xl gap-0 py-0">
            <CardHeader className="border-b px-5 py-5 sm:px-6">
              <CardTitle>
                <h2>
                  {discovery
                    ? "Repository discovery"
                    : taskId
                      ? "Imported task"
                      : "New task"}
                </h2>
              </CardTitle>
              <CardDescription>
                {discovery
                  ? "Get a project brief, priorities, and recommended agents from the repository."
                  : "Starts in an isolated workspace"}
              </CardDescription>
              {taskId && (
                <CardAction>
                  <Button asChild variant="outline" size="sm">
                    <Link to="/" search={{}}>
                      New task
                    </Link>
                  </Button>
                </CardAction>
              )}
            </CardHeader>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                launch.mutate();
              }}
            >
              <CardContent className="space-y-5 px-5 py-6 sm:px-6">
                <fieldset
                  disabled={
                    launch.isPending ||
                    !!partial ||
                    (!!taskId && !imported.data)
                  }
                  className="grid min-w-0 gap-5"
                >
                  <div className="grid gap-2">
                    <Label htmlFor="task-title">Task title</Label>
                    <Input
                      id="task-title"
                      aria-label="Task title"
                      required
                      maxLength={500}
                      placeholder="What should the agent work on?"
                      value={taskId ? (imported.data?.title ?? "") : title}
                      readOnly={!!taskId}
                      onChange={(e) => setTitle(e.target.value)}
                    />
                  </div>
                  <div className="grid min-w-0 gap-5 sm:grid-cols-2">
                    <ResourceSelect
                      id="repository"
                      label="Repository"
                      value={repository?.id ?? ""}
                      onValueChange={setRepositoryId}
                      options={(repositories.data ?? []).map((item) => ({
                        value: item.id,
                        label: item.name,
                      }))}
                      placeholder="No repositories available"
                      disabled={launch.isPending || !!partial || !!taskId}
                      required
                    />
                    <ResourceSelect
                      id="agent"
                      label="Agent"
                      value={agent?.id ?? ""}
                      onValueChange={setAgentId}
                      options={enabledAgents.map((item) => ({
                        value: item.id,
                        label: `${item.name} · ${item.backend}`,
                      }))}
                      placeholder="No enabled agents"
                      disabled={launch.isPending || !!partial}
                      required
                    />
                  </div>
                  {((repositories.isSuccess && !repositories.data.length) ||
                    (agents.isSuccess && !enabledAgents.length)) && (
                    <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-muted/30 p-3">
                      <p className="text-sm text-muted-foreground">
                        {!repositories.data?.length
                          ? "Add a repository to start working on this project."
                          : "Create an enabled agent to run tasks in this project."}
                      </p>
                      <Button asChild variant="outline" size="sm">
                        <Link
                          to="/setup"
                          search={{
                            section: !repositories.data?.length
                              ? "repositories"
                              : "agents",
                          }}
                        >
                          {!repositories.data?.length
                            ? "Add repository"
                            : "Create agent"}
                          <ArrowRight aria-hidden="true" />
                        </Link>
                      </Button>
                    </div>
                  )}
                  <div className="grid gap-2">
                    <Label htmlFor="description">
                      Description{" "}
                      <span className="font-normal text-muted-foreground">
                        (optional)
                      </span>
                    </Label>
                    <Textarea
                      id="description"
                      aria-label="Description"
                      rows={3}
                      className="min-h-24 resize-y"
                      placeholder="Add context, constraints or a definition of done."
                      value={
                        taskId
                          ? (imported.data?.description ?? "")
                          : description
                      }
                      readOnly={!!taskId}
                      onChange={(e) => setDescription(e.target.value)}
                    />
                  </div>
                </fieldset>
                {taskId && imported.isPending && (
                  <p role="status" className="text-sm text-muted-foreground">
                    Loading Task…
                  </p>
                )}
                {imported.error && (
                  <ErrorAlert>
                    Could not load this Task: {imported.error.message}
                  </ErrorAlert>
                )}
                {taskId &&
                  imported.isSuccess &&
                  repositories.isSuccess &&
                  !repository && (
                    <ErrorAlert>
                      The Task’s repository is unavailable in this project.
                    </ErrorAlert>
                  )}
                {taskId && imported.data && (
                  <p className="text-xs text-muted-foreground">
                    This starts a Run for the saved Task. Its description and
                    repository are preserved.
                  </p>
                )}
                {(repositories.error || agents.error) && (
                  <ErrorAlert>
                    Could not load launch options. Refresh to try again.
                  </ErrorAlert>
                )}
                {launch.error && (
                  <ErrorAlert>{launch.error.message}</ErrorAlert>
                )}
              </CardContent>
              <CardFooter className="flex flex-wrap justify-between gap-4 px-5 py-4 sm:px-6">
                <span className="text-xs text-muted-foreground">
                  {partial
                    ? `Task saved · ${partial.taskId.slice(0, 8)}`
                    : agent
                      ? `${agent.backend === "fake" ? "Network disabled" : "Network enabled"} · Dedicated workspace`
                      : "Select a repository and agent to start"}
                </span>
                <Button
                  type="submit"
                  disabled={
                    launch.isPending ||
                    (!partial &&
                      (!(taskId ? imported.data?.title : title.trim()) ||
                        !repository ||
                        !agent ||
                        !project ||
                        (!!taskId && imported.data?.project_id !== project.id)))
                  }
                >
                  {launch.isPending && (
                    <LoaderCircle className="animate-spin" aria-hidden="true" />
                  )}
                  {launch.isPending
                    ? "Starting…"
                    : partial
                      ? "Retry starting Run"
                      : "Start Run"}
                  {!launch.isPending && (
                    <ArrowRight aria-hidden="true" data-icon="inline-end" />
                  )}
                </Button>
              </CardFooter>
            </form>
          </Card>
        )}
        <section className="space-y-3" aria-labelledby="recent-runs">
          <div className="flex items-center justify-between">
            <h2 id="recent-runs" className="text-base font-medium">
              Recent Runs
            </h2>
            <Badge variant="secondary">{runs.data?.length ?? 0} total</Badge>
          </div>
          <Card className="gap-0 py-0">
            {runs.error && (
              <div className="p-4">
                <ErrorAlert>{runs.error.message}</ErrorAlert>
              </div>
            )}
            <Table>
              <TableHeader>
                <TableRow className="bg-muted/40 hover:bg-muted/40">
                  <TableHead className="px-5">Run</TableHead>
                  <TableHead>Backend</TableHead>
                  <TableHead className="hidden lg:table-cell">
                    Created
                  </TableHead>
                  <TableHead className="pr-5 text-right">Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.data?.map((run) => (
                  <TableRow key={run.id} className="relative">
                    <TableCell className="px-5 py-4">
                      <Link
                        to="/runs/$runId"
                        params={{ runId: run.id }}
                        className="rounded-sm font-mono text-xs outline-none after:absolute after:inset-0 hover:text-primary focus-visible:ring-3 focus-visible:ring-ring/50"
                      >
                        {run.id.slice(0, 8)}{" "}
                        <span className="hidden text-muted-foreground sm:inline">
                          / attempt {run.attempt}
                        </span>
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{run.backend}</Badge>
                      {run.kind === "pr_review" && (
                        <Badge variant="secondary">PR review</Badge>
                      )}
                    </TableCell>
                    <TableCell className="hidden text-xs text-muted-foreground lg:table-cell">
                      {new Date(run.created_at).toLocaleString()}
                    </TableCell>
                    <TableCell className="pr-5 text-right">
                      <Status value={run.status} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {runs.isPending && selectedProject ? (
              <div
                className="space-y-3 p-5"
                role="status"
                aria-label="Loading Runs"
              >
                <Skeleton className="h-6 w-full" />
                <Skeleton className="h-6 w-3/4" />
              </div>
            ) : (
              !runs.data?.length && (
                <EmptyState
                  icon={<Inbox aria-hidden="true" />}
                  description="Your execution history will appear here."
                />
              )
            )}
          </Card>
        </section>
      </div>
    </>
  );
}

function Elapsed({ run }: { run: Run }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (terminal(run.status)) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [run.status]);
  const seconds = Math.max(
    0,
    Math.floor(
      ((run.finished_at ? Date.parse(run.finished_at) : now) -
        Date.parse(run.started_at ?? run.created_at)) /
        1000,
    ),
  );
  return (
    <span>
      {Math.floor(seconds / 60)}m {seconds % 60}s
    </span>
  );
}

function RunDetail() {
  const { runId } = runRoute.useParams();
  const detail = useQuery({
    queryKey: ["execution", runId],
    queryFn: () => api.execution(runId),
    refetchInterval: (query) => {
      const data = query.state.data;
      return data &&
        terminal(data.run.status) &&
        (!data.workspace || data.workspace.status === "released")
        ? false
        : 750;
    },
  });
  const snapshot = detail.data;
  const stream = useRunEvents(
    runId,
    snapshot && terminal(snapshot.run.status)
      ? snapshot.last_event_sequence
      : 0,
  );
  const cancel = useMutation({
    mutationFn: () => api.cancel(runId),
    onSuccess: () => {
      void detail.refetch();
    },
  });
  const diffArtifact = snapshot?.artifacts.find(
    (artifact) => artifact.kind === "diff",
  );
  const diff = useQuery({
    queryKey: ["diff", diffArtifact?.id],
    enabled: !!diffArtifact,
    queryFn: async () => {
      if (!diffArtifact) return "";
      const response = await fetch(api.artifactUrl(diffArtifact));
      if (!response.ok)
        throw new Error(`Could not read diff (${response.status})`);
      return response.text();
    },
  });
  if (detail.error)
    return (
      <div className="space-y-5 p-6 lg:p-8">
        <Button variant="ghost" asChild>
          <Link to="/">
            <ArrowLeft aria-hidden="true" />
            Runs
          </Link>
        </Button>
        <ErrorAlert>{detail.error.message}</ErrorAlert>
      </div>
    );
  if (!snapshot)
    return (
      <div
        className="space-y-6 p-6 lg:p-8"
        role="status"
        aria-label="Loading Run"
      >
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-4 w-40" />
        <Skeleton className="h-96 w-full" />
        <span className="sr-only">Loading Run…</span>
      </div>
    );
  const { run, task, agent, workspace, artifacts, usage } = snapshot;
  const output = outputFrom(stream.events);
  return (
    <>
      <header className="flex h-14 items-center justify-between gap-4 border-b bg-sidebar/50 px-4 text-xs sm:px-6 lg:px-8">
        <div className="flex items-center gap-2">
          <Button
            variant="link"
            size="sm"
            className="h-auto p-0 text-foreground"
            asChild
          >
            <Link to="/">Runs</Link>
          </Button>
          <span className="text-muted-foreground">/ {runId.slice(0, 8)}</span>
        </div>
        <Badge variant="outline" className="gap-1.5 text-muted-foreground">
          <span
            aria-hidden="true"
            className={cn(
              "size-1.5 rounded-full bg-muted-foreground",
              stream.connection === "Live" && "bg-success",
            )}
          />
          {stream.connection}
        </Badge>
      </header>
      <div className="mx-auto max-w-[1450px] space-y-6 px-4 py-7 sm:px-6 lg:px-8 lg:py-9">
        <div className="flex flex-col justify-between gap-5 md:flex-row md:items-center">
          <div className="min-w-0 space-y-2">
            <p className="text-[10px] font-medium tracking-[0.18em] text-muted-foreground">
              RUN · {runId.slice(0, 8)}
            </p>
            <h1 className="text-2xl font-semibold tracking-tight [overflow-wrap:anywhere]">
              {task.title}
            </h1>
            <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              <span>{agent.name}</span>
              <Separator orientation="vertical" className="h-3" />
              <Badge variant="outline">{run.backend}</Badge>
              {run.kind === "pr_review" && (
                <Badge variant="secondary">PR review</Badge>
              )}
              <Separator orientation="vertical" className="h-3" />
              <span>Attempt {run.attempt}</span>
            </div>
          </div>
          <div className="heading-actions flex shrink-0 items-center gap-3">
            <Status value={run.status} />
            <Button
              variant="destructive"
              disabled={terminal(run.status) || cancel.isPending}
              onClick={() => cancel.mutate()}
            >
              {cancel.isPending && (
                <LoaderCircle className="animate-spin" aria-hidden="true" />
              )}
              {cancel.isPending ? "Cancelling…" : "Cancel Run"}
            </Button>
          </div>
        </div>
        {(run.error || cancel.error) && (
          <ErrorAlert>{run.error || cancel.error?.message}</ErrorAlert>
        )}
        {run.kind !== "pr_review" && (
          <RunAgentProposals
            key={runId}
            runID={runId}
            projectID={task.project_id}
            report={output}
            finished={terminal(run.status)}
          />
        )}
        {snapshot.pr_review_id && (
          <PRReviewReport
            key={snapshot.pr_review_id}
            reviewID={snapshot.pr_review_id}
          />
        )}
        <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_280px]">
          <Card className="min-w-0 gap-0 py-0">
            <Tabs defaultValue="output" className="gap-0">
              <div className="border-b px-3 py-2 sm:px-4">
                <TabsList
                  variant="line"
                  aria-label="Execution output"
                  className="w-full justify-start sm:w-auto"
                >
                  <TabsTrigger value="output" className="px-3">
                    Agent output
                  </TabsTrigger>
                  <TabsTrigger value="diff" className="px-3">
                    Changes
                  </TabsTrigger>
                  <TabsTrigger value="timeline" className="px-3">
                    Timeline{" "}
                    <Badge
                      variant="secondary"
                      className="ml-1 px-1.5 text-[10px]"
                    >
                      {stream.events.length}
                    </Badge>
                  </TabsTrigger>
                </TabsList>
              </div>
              <TabsContent value="output" className="min-h-96 min-w-0">
                <RunReport
                  events={stream.events}
                  finished={terminal(run.status)}
                  runID={runId}
                />
              </TabsContent>
              <TabsContent
                value="diff"
                className="min-h-96 max-h-[70vh] overflow-auto"
              >
                {diff.error ? (
                  <div className="p-5">
                    <ErrorAlert>{diff.error.message}</ErrorAlert>
                  </div>
                ) : diffArtifact ? (
                  diff.data ? (
                    <pre className="diff-output p-5 font-mono text-xs leading-7 sm:p-6">
                      {diff.data.split("\n").map((line, index) => (
                        <span
                          key={index}
                          className={cn(
                            line.startsWith("+")
                              ? "bg-success/10 text-success"
                              : line.startsWith("-")
                                ? "bg-destructive/10 text-destructive"
                                : line.startsWith("@@")
                                  ? "text-primary"
                                  : "text-muted-foreground",
                          )}
                        >
                          {line}
                          {"\n"}
                        </span>
                      ))}
                    </pre>
                  ) : (
                    <EmptyState
                      description={
                        diff.isPending ? "Loading diff…" : "No file changes."
                      }
                    />
                  )
                ) : (
                  <EmptyState
                    icon={<Files aria-hidden="true" />}
                    description="The final diff will be available when execution finishes."
                  />
                )}
              </TabsContent>
              <TabsContent
                value="timeline"
                className="min-h-96 max-h-[70vh] overflow-auto"
              >
                <ol className="timeline divide-y px-4 sm:px-6">
                  {stream.events.map((event) => (
                    <li
                      key={event.sequence}
                      className="grid grid-cols-[24px_minmax(0,1fr)] gap-3 py-4 sm:grid-cols-[24px_minmax(0,1fr)_auto]"
                    >
                      <span className="font-mono text-[10px] leading-6 text-muted-foreground">
                        {String(event.sequence).padStart(2, "0")}
                      </span>
                      <div className="min-w-0">
                        <strong className="text-xs font-medium [overflow-wrap:anywhere]">
                          {event.type}
                        </strong>
                        <p className="mt-1 font-mono text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
                          {event.type.startsWith("agent.message")
                            ? String(
                                event.data.delta ?? event.data.content ?? "",
                              )
                            : JSON.stringify(event.data)}
                        </p>
                      </div>
                      <time className="col-start-2 font-mono text-[10px] leading-6 text-muted-foreground sm:col-start-auto">
                        {new Date(event.recorded_at).toLocaleTimeString()}
                      </time>
                    </li>
                  ))}
                </ol>
                {!stream.events.length && (
                  <EmptyState description="Waiting for execution events…" />
                )}
              </TabsContent>
            </Tabs>
          </Card>
          <aside className="details-column grid min-w-0 gap-5 sm:grid-cols-2 xl:grid-cols-1">
            {run.kind !== "pr_review" && (
              <RunGitHubDelivery
                runID={runId}
                task={task}
                runStatus={run.status}
              />
            )}
            {run.kind !== "pr_review" && run.status === "succeeded" && (
              <RunPRReviews sourceRunID={runId} />
            )}
            <RunLinearDelivery
              runID={runId}
              task={task}
              review={run.kind === "pr_review"}
            />
            <Card>
              <CardHeader className="border-b">
                <CardTitle>
                  <h2>Execution</h2>
                </CardTitle>
              </CardHeader>
              <CardContent>
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-4 gap-y-4 text-xs [&>dd]:min-w-0 [&>dd]:text-right [&>dd]:[overflow-wrap:anywhere] [&>dt]:text-muted-foreground">
                  <dt>Agent</dt>
                  <dd>{agent.name}</dd>
                  <dt>Backend</dt>
                  <dd>
                    <Badge variant="outline">{run.backend}</Badge>
                    {run.kind === "pr_review" && (
                      <Badge variant="secondary">PR review</Badge>
                    )}
                  </dd>
                  <dt>Duration</dt>
                  <dd className="tabular-nums">
                    <Elapsed run={run} />
                  </dd>
                  <dt>Workspace</dt>
                  <dd>
                    {workspace ? (
                      <Status value={workspace.status} />
                    ) : (
                      <span className="text-muted-foreground">
                        Not allocated
                      </span>
                    )}
                  </dd>
                  <dt>Input tokens</dt>
                  <dd className="tabular-nums">{usage.input_tokens ?? 0}</dd>
                  <dt>Output tokens</dt>
                  <dd className="tabular-nums">{usage.output_tokens ?? 0}</dd>
                </dl>
              </CardContent>
            </Card>
            <Card>
              <CardHeader className="border-b">
                <CardTitle>
                  <h2>Artifacts</h2>
                </CardTitle>
                <CardAction>
                  <Badge variant="secondary">{artifacts.length}</Badge>
                </CardAction>
              </CardHeader>
              <CardContent className="grid gap-2">
                {artifacts.length ? (
                  artifacts.map((artifact) => (
                    <Button
                      key={artifact.id}
                      variant="ghost"
                      asChild
                      className="h-auto justify-start gap-3 py-3"
                    >
                      <a href={api.artifactUrl(artifact)} download>
                        <Download
                          className="text-muted-foreground"
                          aria-hidden="true"
                        />
                        <span className="min-w-0 text-left text-xs">
                          {artifact.kind === "diff"
                            ? "Final diff"
                            : "Workspace output"}
                          <span className="mt-1 block text-[10px] font-normal text-muted-foreground">
                            {Math.ceil(
                              Number(artifact.metadata.size_bytes ?? 0) / 1024,
                            )}{" "}
                            KB · {artifact.kind === "diff" ? "PATCH" : "TAR"}
                          </span>
                        </span>
                      </a>
                    </Button>
                  ))
                ) : (
                  <EmptyState description="No artifacts yet." />
                )}
              </CardContent>
            </Card>
            {task.description && (
              <Card>
                <CardHeader className="border-b">
                  <CardTitle>
                    <h2>Task context</h2>
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  <Markdown className="text-xs text-muted-foreground">
                    {task.description}
                  </Markdown>
                </CardContent>
              </Card>
            )}
          </aside>
        </div>
      </div>
    </>
  );
}

const rootRoute = createRootRoute({ component: RootLayout });
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  validateSearch: (search: Record<string, unknown>): { taskId?: string } => ({
    taskId: typeof search.taskId === "string" ? search.taskId : undefined,
  }),
  component: Overview,
});
const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: RunDetail,
});
const docsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/docs/$",
  component: lazyRouteComponent(() => import("./pages/docs")),
});
const docsIndexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/docs",
  component: lazyRouteComponent(() => import("./pages/docs")),
});
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  validateSearch: (
    search: Record<string, unknown>,
  ): {
    section: SetupSection;
    provider?: "github" | "linear";
    connection_result?: "connected" | "cancelled" | "failed";
    project_id?: string;
  } => ({
    section:
      search.section === "repositories" ||
      search.section === "agents" ||
      search.section === "integrations" ||
      search.section === "mcp"
        ? search.section
        : "projects",
    provider:
      search.provider === "github" || search.provider === "linear"
        ? search.provider
        : undefined,
    connection_result:
      search.connection_result === "connected" ||
      search.connection_result === "cancelled" ||
      search.connection_result === "failed"
        ? search.connection_result
        : undefined,
    project_id:
      typeof search.project_id === "string" ? search.project_id : undefined,
  }),
  component: SetupRoute,
});

function SetupRoute() {
  const { section, provider, connection_result, project_id } =
    setupRoute.useSearch();
  const navigate = useNavigate();
  const client = useQueryClient();
  const { projects, selectedProject, selectProject } = useProject();
  const [notice, setNotice] = useState<
    ConnectionNotice & { project: string }
  >();
  useEffect(() => {
    if (!provider || !connection_result || projects.isPending) return;
    const known =
      project_id && projects.data?.some((item) => item.id === project_id);
    if (known) selectProject(project_id);
    const target = known ? project_id : selectedProject;
    setNotice({
      provider,
      result: project_id && !known ? "failed" : connection_result,
      project: target,
    });
    void client.invalidateQueries({ queryKey: ["connections", target] });
    void client.invalidateQueries({
      queryKey: ["integration", target, provider],
    });
    void navigate({
      to: "/setup",
      search: { section: "integrations" },
      replace: true,
    });
  }, [
    client,
    provider,
    connection_result,
    project_id,
    projects.isPending,
    projects.data,
    selectedProject,
    selectProject,
    navigate,
  ]);
  return (
    <SetupPage
      section={section}
      notice={notice?.project === selectedProject ? notice : undefined}
      onSectionChange={(next) => {
        void navigate({
          to: "/setup",
          search: { section: next },
          replace: true,
        });
      }}
    />
  );
}

const requestsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/requests",
  component: RequestsPage,
});
const requestRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/requests/$requestID",
  component: RequestPage,
});
const router = createRouter({
  routeTree: rootRoute.addChildren([
    indexRoute,
    requestsRoute,
    requestRoute,
    runRoute,
    setupRoute,
    docsIndexRoute,
    docsRoute,
  ]),
  scrollRestoration: true,
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </React.StrictMode>,
);
