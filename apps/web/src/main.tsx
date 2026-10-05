import { ConsoleShell } from "@/components/console/shell";
import { TaskLauncherProvider } from "@/components/console/task-launcher-context";
import { EmptyState } from "@/components/empty-state";
import { ErrorAlert } from "@/components/error-alert";
import { RequestsPage } from "@/components/external-request-list";
import { Markdown } from "@/components/markdown";
import { PRReviewReport } from "@/components/pr-review-report";
import { RunAgentProposals } from "@/components/run-agent-proposals";
import { RunGitHubDelivery } from "@/components/run-github-delivery";
import { RunLinearDelivery } from "@/components/run-linear-delivery";
import { RunPRReviews } from "@/components/run-pr-reviews";
import { RunReport } from "@/components/run-report";
import { RunStatus as Status } from "@/components/run-status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { parseRunQueueSearch } from "@/lib/run-queue";
import { cn } from "@/lib/utils";
import { ConsoleOverview } from "@/pages/overview";
import { RequestPage } from "@/pages/request";
import { RunsPage } from "@/pages/runs";
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
  stripSearchParams,
  useNavigate,
  useRouterState,
  type SearchSchemaInput,
} from "@tanstack/react-router";
import { ArrowLeft, Download, Files, LoaderCircle } from "lucide-react";
import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { api, type Run } from "./api";
import "./index.css";
import type { ConnectionNotice } from "./pages/integrations";
import { SetupPage, type SetupSection } from "./pages/setup";
import { outputFrom } from "./run-events";
import {
  ProjectProvider,
  useProject,
  useProjectSelectionLock,
} from "./use-project";
import { useRunEvents } from "./use-run-events";

const queryClient = new QueryClient();
const terminal = (status: string) =>
  ["succeeded", "failed", "cancelled"].includes(status);

function RootLayout() {
  const inDocs = useRouterState({
    select: (state) =>
      state.location.pathname === "/docs" ||
      state.location.pathname.startsWith("/docs/"),
  });
  if (inDocs) return <Outlet />;
  return (
    <ProjectProvider>
      <TaskLauncherProvider>
        <ConsoleShell>
          <Outlet />
        </ConsoleShell>
      </TaskLauncherProvider>
    </ProjectProvider>
  );
}
function Overview() {
  const { taskId } = indexRoute.useSearch();
  return <ConsoleOverview taskId={taskId} />;
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
  const { selectProject } = useProject();
  useProjectSelectionLock(detail.isPending);
  useEffect(() => {
    if (snapshot?.task.project_id) selectProject(snapshot.task.project_id);
  }, [snapshot?.task.project_id, selectProject]);
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
          <Link to="/runs">
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
            <Link to="/runs">Runs</Link>
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
const runsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs",
  validateSearch: (
    search: SearchSchemaInput & {
      group?: unknown;
      q?: unknown;
      cursor?: unknown;
    },
  ) => parseRunQueueSearch(search),
  search: {
    middlewares: [stripSearchParams({ group: "all", q: "", cursor: "" })],
  },
  component: RunsPage,
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
    runsRoute,
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
