import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
} from "@tanstack/react-query";
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  useNavigate,
} from "@tanstack/react-router";
import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { api, type Run } from "./api";
import { LaunchError, launchTask } from "./launch";
import { outputFrom } from "./run-events";
import { useRunEvents } from "./use-run-events";
import "./index.css";

const queryClient = new QueryClient();
const terminal = (status: string) =>
  ["succeeded", "failed", "cancelled"].includes(status);
function Status({ value }: { value: string }) {
  return (
    <span className={`status ${value}`}>
      <i />
      {value.replaceAll("_", " ")}
    </span>
  );
}

function RootLayout() {
  return (
    <div className="shell">
      <aside className="sidebar">
        <Link className="brand" to="/">
          <span className="brand-mark">C</span>Circular
        </Link>
        <div className="sidebar-caption">WORKSPACE</div>
        <Link className="nav-link" to="/">
          ◉ <span>Runs</span>
        </Link>
        <div className="sidebar-foot">
          <span className="online-dot" /> Local execution
          <br />
          <small>One task. One isolated Run.</small>
        </div>
      </aside>
      <main>
        <Outlet />
      </main>
    </div>
  );
}

function Overview() {
  const navigate = useNavigate();
  const [projectId, setProjectId] = useState("");
  const [repositoryId, setRepositoryId] = useState("");
  const [agentId, setAgentId] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [partial, setPartial] = useState<{ taskId: string; agentId: string }>();
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects });
  const selectedProject = projectId || projects.data?.[0]?.id || "";
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
  const project = projects.data?.find((item) => item.id === selectedProject);
  const repository =
    repositories.data?.find((item) => item.id === repositoryId) ??
    repositories.data?.[0];
  const enabledAgents = agents.data?.filter((item) => item.enabled) ?? [];
  const agent =
    enabledAgents.find((item) => item.id === agentId) ?? enabledAgents[0];
  const launch = useMutation({
    mutationFn: async () => {
      if (partial)
        return api.createRun({
          task_id: partial.taskId,
          agent_id: partial.agentId,
        });
      if (!project || !repository || !agent)
        throw new Error("Select a project, repository and agent.");
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
      <header className="topbar">
        <span>
          Workspace <span className="muted">/ Runs</span>
        </span>
        <span className="small muted">Execution control plane</span>
      </header>
      <div className="page">
        <div className="page-heading">
          <div>
            <p className="eyebrow">EXECUTION</p>
            <h1>Your work, in motion.</h1>
            <p className="muted">
              Launch a task and follow its execution from start to finish.
            </p>
          </div>
          <label className="project-picker">
            Project
            <select
              aria-label="Project"
              value={selectedProject}
              disabled={launch.isPending || !!partial}
              onChange={(e) => {
                setProjectId(e.target.value);
                setRepositoryId("");
                setAgentId("");
              }}
            >
              {projects.data?.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </select>
          </label>
        </div>
        {projects.error && (
          <p role="alert" className="error">
            Could not load projects: {projects.error.message}
          </p>
        )}
        {projects.isSuccess && !projects.data.length ? (
          <div className="empty panel">
            No projects yet. Register a project, repository and agent in the API
            to start your first Run.
          </div>
        ) : (
          <form
            className="panel launch-form"
            onSubmit={(e) => {
              e.preventDefault();
              launch.mutate();
            }}
          >
            <div className="panel-heading">
              <h2>New task</h2>
              <span className="muted small">
                Starts in an isolated workspace
              </span>
            </div>
            <fieldset disabled={launch.isPending || !!partial}>
              <label>
                Task title
                <input
                  aria-label="Task title"
                  required
                  maxLength={500}
                  placeholder="What should the agent work on?"
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                />
              </label>
              <div className="form-grid">
                <label>
                  Repository
                  <select
                    aria-label="Repository"
                    value={repository?.id ?? ""}
                    onChange={(e) => setRepositoryId(e.target.value)}
                    required
                  >
                    {!repository && (
                      <option value="">No repositories available</option>
                    )}
                    {repositories.data?.map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Agent
                  <select
                    aria-label="Agent"
                    value={agent?.id ?? ""}
                    onChange={(e) => setAgentId(e.target.value)}
                    required
                  >
                    {!agent && <option value="">No enabled agents</option>}
                    {enabledAgents.map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.name} · {item.backend}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <label>
                Description <span className="muted">(optional)</span>
                <textarea
                  aria-label="Description"
                  rows={2}
                  placeholder="Add context, constraints or a definition of done."
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>
            </fieldset>
            {(repositories.error || agents.error) && (
              <p role="alert" className="error">
                Could not load launch options. Refresh to try again.
              </p>
            )}
            {launch.error && (
              <p role="alert" className="error">
                {launch.error.message}
              </p>
            )}
            <div className="form-actions">
              <span className="muted small">
                {partial
                  ? `Task saved · ${partial.taskId.slice(0, 8)}`
                  : "Network disabled · Dedicated workspace"}
              </span>
              <button
                className="primary"
                disabled={
                  launch.isPending ||
                  (!partial &&
                    (!title.trim() || !repository || !agent || !project))
                }
              >
                {launch.isPending
                  ? "Starting…"
                  : partial
                    ? "Retry starting Run"
                    : "Start Run →"}
              </button>
            </div>
          </form>
        )}
        <div className="section-title">
          <h2>Recent Runs</h2>
          <span className="muted small">{runs.data?.length ?? 0} total</span>
        </div>
        <div className="panel run-list">
          <div className="run-row list-heading">
            <span>Run</span>
            <span>Backend</span>
            <span>Created</span>
            <span>Status</span>
          </div>
          {runs.error && (
            <p role="alert" className="error">
              {runs.error.message}
            </p>
          )}
          {runs.data?.map((run) => (
            <Link
              key={run.id}
              className="run-row"
              to="/runs/$runId"
              params={{ runId: run.id }}
            >
              <span className="mono">
                {run.id.slice(0, 8)}{" "}
                <span className="muted">/ attempt {run.attempt}</span>
              </span>
              <span>{run.backend}</span>
              <span className="muted">
                {new Date(run.created_at).toLocaleString()}
              </span>
              <Status value={run.status} />
            </Link>
          ))}
          {!runs.data?.length && (
            <div className="empty">
              Your execution history will appear here.
            </div>
          )}
        </div>
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
  const [tab, setTab] = useState<"output" | "diff" | "timeline">("output");
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
      <div className="page">
        <Link to="/">← Runs</Link>
        <p role="alert" className="error">
          {detail.error.message}
        </p>
      </div>
    );
  if (!snapshot) return <div className="page muted">Loading Run…</div>;
  const { run, task, agent, workspace, artifacts, usage } = snapshot;
  const output = outputFrom(stream.events);
  return (
    <>
      <header className="topbar">
        <span>
          <Link to="/">Runs</Link>{" "}
          <span className="muted">/ {runId.slice(0, 8)}</span>
        </span>
        <span className="small stream-label">
          <i className={stream.connection === "Live" ? "online-dot" : ""} />
          {stream.connection}
        </span>
      </header>
      <div className="page execution-page">
        <div className="page-heading">
          <div>
            <p className="eyebrow">RUN · {runId.slice(0, 8)}</p>
            <h1>{task.title}</h1>
            <p className="muted">
              {agent.name} <span className="separator">/</span> {run.backend}{" "}
              <span className="separator">/</span> Attempt {run.attempt}
            </p>
          </div>
          <div className="heading-actions">
            <Status value={run.status} />
            <button
              className="danger"
              disabled={terminal(run.status) || cancel.isPending}
              onClick={() => cancel.mutate()}
            >
              {cancel.isPending ? "Cancelling…" : "Cancel Run"}
            </button>
          </div>
        </div>
        {(run.error || cancel.error) && (
          <div role="alert" className="error panel">
            {run.error || cancel.error?.message}
          </div>
        )}
        <div className="execution-grid">
          <section className="panel activity">
            <div className="tabs" role="tablist" aria-label="Execution output">
              {(["output", "diff", "timeline"] as const).map((name) => (
                <button
                  key={name}
                  role="tab"
                  aria-selected={tab === name}
                  className={tab === name ? "active" : ""}
                  onClick={() => setTab(name)}
                >
                  {name === "output"
                    ? "Agent output"
                    : name === "diff"
                      ? "Changes"
                      : "Timeline"}
                  {name === "timeline" && <small>{stream.events.length}</small>}
                </button>
              ))}
            </div>
            <div role="tabpanel" className="activity-content">
              {tab === "output" &&
                (output ? (
                  <pre className="agent-output">{output}</pre>
                ) : (
                  <div className="empty">
                    {terminal(run.status)
                      ? "No agent output was recorded."
                      : "Waiting for agent output…"}
                  </div>
                ))}
              {tab === "diff" && (
                <>
                  {diff.error ? (
                    <p role="alert" className="error">
                      {diff.error.message}
                    </p>
                  ) : diffArtifact ? (
                    diff.data ? (
                      <pre className="diff-output">
                        {diff.data.split("\n").map((line, index) => (
                          <span
                            key={index}
                            className={
                              line.startsWith("+")
                                ? "addition"
                                : line.startsWith("-")
                                  ? "deletion"
                                  : line.startsWith("@@")
                                    ? "hunk"
                                    : ""
                            }
                          >
                            {line}
                            {"\n"}
                          </span>
                        ))}
                      </pre>
                    ) : (
                      <div className="empty">
                        {diff.isPending ? "Loading diff…" : "No file changes."}
                      </div>
                    )
                  ) : (
                    <div className="empty">
                      The final diff will be available when execution finishes.
                    </div>
                  )}
                </>
              )}
              {tab === "timeline" && (
                <ol className="timeline">
                  {stream.events.map((event) => (
                    <li key={event.sequence}>
                      <span className="event-number">
                        {String(event.sequence).padStart(2, "0")}
                      </span>
                      <div>
                        <strong>{event.type}</strong>
                        <p>
                          {event.type.startsWith("agent.message")
                            ? String(
                                event.data.delta ?? event.data.content ?? "",
                              )
                            : JSON.stringify(event.data)}
                        </p>
                      </div>
                      <time>
                        {new Date(event.recorded_at).toLocaleTimeString()}
                      </time>
                    </li>
                  ))}
                </ol>
              )}
            </div>
          </section>
          <aside className="details-column">
            <section className="panel">
              <div className="panel-heading">
                <h2>Execution</h2>
              </div>
              <dl>
                <dt>Agent</dt>
                <dd>{agent.name}</dd>
                <dt>Backend</dt>
                <dd>{run.backend}</dd>
                <dt>Duration</dt>
                <dd>
                  <Elapsed run={run} />
                </dd>
                <dt>Workspace</dt>
                <dd>
                  {workspace ? (
                    <Status value={workspace.status} />
                  ) : (
                    <span className="muted">Not allocated</span>
                  )}
                </dd>
                <dt>Input tokens</dt>
                <dd>{usage.input_tokens ?? 0}</dd>
                <dt>Output tokens</dt>
                <dd>{usage.output_tokens ?? 0}</dd>
              </dl>
            </section>
            <section className="panel">
              <div className="panel-heading">
                <h2>Artifacts</h2>
                <span className="small muted">{artifacts.length}</span>
              </div>
              <div className="artifact-list">
                {artifacts.length ? (
                  artifacts.map((artifact) => (
                    <a
                      key={artifact.id}
                      href={api.artifactUrl(artifact)}
                      download
                    >
                      <span className="artifact-icon">↧</span>
                      <span>
                        {artifact.kind === "diff"
                          ? "Final diff"
                          : "Workspace output"}
                        <small>
                          {Math.ceil(
                            Number(artifact.metadata.size_bytes ?? 0) / 1024,
                          )}{" "}
                          KB · {artifact.kind === "diff" ? "PATCH" : "TAR"}
                        </small>
                      </span>
                    </a>
                  ))
                ) : (
                  <p className="muted small">No artifacts yet.</p>
                )}
              </div>
            </section>
            {task.description && (
              <section className="panel">
                <div className="panel-heading">
                  <h2>Task context</h2>
                </div>
                <p className="task-context">{task.description}</p>
              </section>
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
  component: Overview,
});
const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: RunDetail,
});
const router = createRouter({
  routeTree: rootRoute.addChildren([indexRoute, runRoute]),
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
