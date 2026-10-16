import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Link,
  useNavigate,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import {
  ArrowRight,
  Bot,
  ChartGantt,
  Columns3,
  List,
  Search,
  Settings,
} from "lucide-react";
import { api, type Agent } from "@/api";
import { useProject } from "@/use-project";
import { modelSettings } from "@/components/codex-model-fields";
import { Markdown } from "@/components/markdown";
import { RunQueue } from "@/components/console/run-queue";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { runQueueOptions } from "@/lib/run-queue";
import { stageLabels, taskPlanningOptions } from "@/lib/task-planning";

function backendLabel(agent: Agent) {
  return agent.backend === "codex"
    ? "Codex"
    : agent.backend === "fake"
      ? "Test (fake)"
      : agent.backend;
}

function useAgents() {
  const { selectedProject } = useProject();
  return useQuery({
    queryKey: ["agents", selectedProject],
    queryFn: () => api.agents(selectedProject),
    enabled: !!selectedProject,
  });
}

export function AgentsPage() {
  const { selectedProject } = useProject();
  const agents = useAgents();
  const search = useSearch({ from: "/agents" });
  const navigate = useNavigate();
  const needle = search.q.trim().toLocaleLowerCase();
  const filtered = agents.data?.filter(
    (agent) =>
      (search.status === "all" ||
        agent.enabled === (search.status === "enabled")) &&
      `${agent.name} ${agent.instructions}`
        .toLocaleLowerCase()
        .includes(needle),
  );
  return (
    <div className="min-w-0 space-y-3 p-3 sm:p-4">
      <div className="flex flex-wrap items-center gap-2 border-b pb-3">
        <div
          role="group"
          aria-label="Agent views"
          className="mr-auto flex gap-1"
        >
          {(
            [
              ["all", "All Agents"],
              ["enabled", "Enabled"],
              ["disabled", "Disabled"],
            ] as const
          ).map(([status, label]) => (
            <Button
              key={status}
              size="sm"
              variant={search.status === status ? "secondary" : "ghost"}
              className="rounded-full text-xs"
              aria-pressed={search.status === status}
              onClick={() =>
                void navigate({ to: "/agents", search: { ...search, status } })
              }
            >
              {label}
            </Button>
          ))}
        </div>
        <div className="relative min-w-32 flex-1 sm:max-w-64">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute left-2.5 top-2 size-4 text-muted-foreground"
          />
          <Input
            aria-label="Search Agents"
            placeholder="Search Agents…"
            className="h-8 pl-8"
            value={search.q}
            onChange={(event) =>
              void navigate({
                to: "/agents",
                search: { ...search, q: event.target.value },
                replace: true,
              })
            }
          />
        </div>
        <Button asChild variant="ghost" size="icon-sm">
          <Link
            to="/setup"
            search={{ section: "agents" }}
            aria-label="Manage Agents"
            title="Manage Agents"
          >
            <Settings aria-hidden="true" />
          </Link>
        </Button>
      </div>
      {!selectedProject ? (
        <p className="p-4 text-sm text-muted-foreground">
          Choose a Project to see its Agents.
        </p>
      ) : agents.isError ? (
        <ErrorAlert>
          Could not load Agents.{" "}
          <Button variant="link" onClick={() => void agents.refetch()}>
            Retry Agents
          </Button>
        </ErrorAlert>
      ) : agents.isPending ? (
        <p role="status" className="p-4 text-sm text-muted-foreground">
          Loading Agents…
        </p>
      ) : !filtered?.length ? (
        <div className="space-y-2 p-8 text-center">
          <h2 className="text-sm font-medium">
            {agents.data.length
              ? "No Agents match these filters."
              : "No Agents yet."}
          </h2>
          <Button asChild variant="link">
            <Link to="/setup" search={{ section: "agents" }}>
              Manage Agents <ArrowRight aria-hidden="true" />
            </Link>
          </Button>
        </div>
      ) : (
        <ul aria-label="Project Agents" className="divide-y divide-border/50">
          {filtered.map((agent) => {
            const model = modelSettings(agent.backend_config).model;
            return (
              <li key={agent.id}>
                <Link
                  to="/agents/$agentId"
                  params={{ agentId: agent.id }}
                  aria-label={agent.name}
                  className="flex min-h-16 min-w-0 items-center gap-3 rounded-md px-3 py-3 hover:bg-muted/40 focus-visible:outline-2 focus-visible:outline-ring"
                >
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border bg-muted/30 text-muted-foreground">
                    <Bot aria-hidden="true" className="size-4" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-[13px] font-medium">
                        {agent.name}
                      </span>
                      {agent.preset && (
                        <span className="hidden text-xs text-muted-foreground sm:inline">
                          Built-in
                        </span>
                      )}
                    </div>
                    <p className="mt-1 truncate text-xs text-muted-foreground">
                      {agent.instructions || "No additional instructions"}
                    </p>
                  </div>
                  <span className="hidden max-w-56 truncate text-xs text-muted-foreground md:block">
                    {backendLabel(agent)}
                    {model ? ` · ${model}` : ""}
                  </span>
                  <Badge variant="outline" className="shrink-0 text-[11px]">
                    {agent.enabled ? "Enabled" : "Disabled"}
                  </Badge>
                  <ArrowRight
                    aria-hidden="true"
                    className="hidden size-3.5 shrink-0 text-muted-foreground sm:block"
                  />
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

export function AgentPage() {
  const { agentId } = useParams({ from: "/agents/$agentId" });
  const { selectedProject } = useProject();
  const agents = useAgents();
  const search = useSearch({ from: "/agents/$agentId" });
  const navigate = useNavigate();
  const agent = agents.data?.find(
    (item) => item.id === agentId && item.project_id === selectedProject,
  );
  return (
    <div className="min-w-0 space-y-4 p-3 sm:p-4">
      {!selectedProject ? (
        <p className="text-sm text-muted-foreground">
          Choose a Project to see its Agents.
        </p>
      ) : agents.isError ? (
        <ErrorAlert>
          Could not load Agents.{" "}
          <Button variant="link" onClick={() => void agents.refetch()}>
            Retry Agents
          </Button>
        </ErrorAlert>
      ) : agents.isPending ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading Agent…
        </p>
      ) : !agent ? (
        <div className="space-y-2 p-8 text-center">
          <h2 className="font-medium">Agent not found in this Project.</h2>
          <Button asChild variant="link">
            <Link to="/agents">Back to Agents</Link>
          </Button>
        </div>
      ) : (
        <Tabs
          value={search.tab}
          onValueChange={(tab) =>
            void navigate({
              to: "/agents/$agentId",
              params: { agentId },
              search: {
                ...search,
                tab: tab === "tasks" || tab === "runs" ? tab : "overview",
              },
            })
          }
        >
          <div className="flex flex-wrap items-center gap-2 border-b pb-3">
            <TabsList
              aria-label="Agent views"
              variant="line"
              className="mr-auto"
            >
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="tasks">Tasks</TabsTrigger>
              <TabsTrigger value="runs">Runs</TabsTrigger>
            </TabsList>
            <Badge variant="outline">
              {agent.enabled ? "Enabled" : "Disabled"}
            </Badge>
            <Button asChild variant="ghost" size="sm">
              <Link to="/setup" search={{ section: "agents" }}>
                <Settings aria-hidden="true" />
                Settings
              </Link>
            </Button>
          </div>
          <TabsContent value="overview" className="pt-3">
            <div className="grid min-w-0 items-start gap-6 lg:grid-cols-[minmax(0,1fr)_260px]">
              <section
                className="min-w-0 space-y-4"
                aria-label="Agent instructions"
              >
                <h2 className="text-sm font-medium">Instructions</h2>
                {agent.instructions ? (
                  <Markdown>{agent.instructions}</Markdown>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No additional instructions.
                  </p>
                )}
              </section>
              <section
                aria-label="Agent configuration"
                className="min-w-0 rounded-lg border bg-muted/10 p-4"
              >
                <h2 className="mb-4 text-sm font-medium">Configuration</h2>
                <dl className="space-y-4 text-xs">
                  <div>
                    <dt className="text-muted-foreground">Backend</dt>
                    <dd className="mt-1 break-words">{backendLabel(agent)}</dd>
                  </div>
                  {agent.backend === "codex" && (
                    <>
                      <div>
                        <dt className="text-muted-foreground">Model</dt>
                        <dd className="mt-1 break-words">
                          {modelSettings(agent.backend_config).model ||
                            "Backend default"}
                        </dd>
                      </div>
                      <div>
                        <dt className="text-muted-foreground">
                          Reasoning effort
                        </dt>
                        <dd className="mt-1 break-words">
                          {modelSettings(agent.backend_config)
                            .reasoning_effort || "Model default"}
                        </dd>
                      </div>
                    </>
                  )}
                  <div>
                    <dt className="text-muted-foreground">Type</dt>
                    <dd className="mt-1">
                      {agent.preset ? "Built-in" : "Custom"}
                    </dd>
                  </div>
                </dl>
              </section>
            </div>
          </TabsContent>
          <TabsContent value="tasks" className="pt-3">
            <AgentTasks key={agent.id} agent={agent} />
          </TabsContent>
          <TabsContent value="runs" className="pt-3">
            <AgentRuns key={agent.id} agent={agent} />
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}

function AgentTasks({ agent }: { agent: Agent }) {
  const tasks = useQuery(
    taskPlanningOptions(agent.project_id, {
      scope: "all",
      preferred_agent_id: agent.id,
      limit: 5,
    }),
  );
  const taskSearch = { scope: "all" as const, preferred_agent_id: agent.id };
  const total = tasks.data?.groups.reduce((sum, group) => sum + group.total, 0);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="mr-auto">
          <h2 className="text-sm font-medium">
            Preferred Tasks{total !== undefined ? ` · ${total}` : ""}
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Tasks planned for this Agent. Runs may use another Agent.
          </p>
        </div>
        <div role="group" aria-label="Open Task layout" className="flex gap-1">
          {(
            [
              ["list", "List", List],
              ["board", "Kanban", Columns3],
              ["timeline", "Gantt", ChartGantt],
            ] as const
          ).map(([view, label, Icon]) => (
            <Button asChild key={view} size="sm" variant="outline">
              <Link
                to="/tasks"
                search={{ ...taskSearch, view }}
                state={{ taskPlanningProject: agent.project_id }}
              >
                <Icon aria-hidden="true" />
                {label}
              </Link>
            </Button>
          ))}
        </div>
      </div>
      {tasks.isError ? (
        <ErrorAlert>
          Could not load Tasks.{" "}
          <Button variant="link" onClick={() => void tasks.refetch()}>
            Retry Tasks
          </Button>
        </ErrorAlert>
      ) : tasks.isPending ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading Tasks…
        </p>
      ) : total === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          No Tasks prefer this Agent yet.
        </p>
      ) : (
        tasks.data.groups
          .filter((group) => group.total > 0)
          .map((group) => (
            <section key={group.status} aria-label={stageLabels[group.status]}>
              <div className="flex h-9 items-center gap-2 rounded-md bg-muted/40 px-3">
                <h3 className="text-[13px] font-medium">
                  {stageLabels[group.status]}
                </h3>
                <span className="text-xs tabular-nums text-muted-foreground">
                  {group.total}
                </span>
              </div>
              <ul>
                {group.items.map((item) => (
                  <li key={item.task.id}>
                    <Link
                      to="/tasks"
                      aria-label={item.task.title}
                      search={{
                        ...taskSearch,
                        view: "list",
                        taskId: item.task.id,
                      }}
                      state={{ taskPlanningProject: agent.project_id }}
                      className="flex min-h-11 min-w-0 items-center gap-3 rounded-md px-3 py-2 text-[13px] hover:bg-muted/40 focus-visible:outline-2 focus-visible:outline-ring"
                    >
                      <span className="min-w-0 flex-1 truncate">
                        {item.task.title}
                      </span>
                      <span className="hidden max-w-48 truncate text-xs text-muted-foreground sm:block">
                        {item.repository?.name ?? "No Repository"}
                      </span>
                      <ArrowRight
                        aria-hidden="true"
                        className="size-3.5 shrink-0 text-muted-foreground"
                      />
                    </Link>
                  </li>
                ))}
              </ul>
              {group.total > group.items.length && (
                <Button asChild variant="link" size="sm">
                  <Link
                    to="/tasks"
                    search={{
                      ...taskSearch,
                      status: group.status,
                      view: "list",
                    }}
                    state={{ taskPlanningProject: agent.project_id }}
                  >
                    View all {group.total} {stageLabels[group.status]} Tasks
                  </Link>
                </Button>
              )}
            </section>
          ))
      )}
    </div>
  );
}

function AgentRuns({ agent }: { agent: Agent }) {
  const search = useSearch({ from: "/agents/$agentId" });
  const navigate = useNavigate();
  const [query, setQuery] = useState(search.q);
  const rows = useQuery(
    runQueueOptions(agent.project_id, { ...search, agent_id: agent.id }),
  );
  useEffect(() => setQuery(search.q), [search.q]);
  useEffect(() => {
    if (query === search.q) return;
    const timer = setTimeout(
      () =>
        void navigate({
          to: "/agents/$agentId",
          params: { agentId: agent.id },
          search: { ...search, q: query.trim(), cursor: "" },
          replace: true,
        }),
      250,
    );
    return () => clearTimeout(timer);
  }, [query, search, agent.id, navigate]);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div
          role="group"
          aria-label="Filter Runs"
          className="mr-auto flex gap-1"
        >
          {(
            [
              ["all", "All"],
              ["active", "Active"],
              ["failed", "Failed"],
              ["finished", "Finished"],
            ] as const
          ).map(([group, label]) => (
            <Button
              key={group}
              size="sm"
              className="rounded-full text-xs"
              variant={search.group === group ? "secondary" : "ghost"}
              aria-pressed={search.group === group}
              onClick={() =>
                void navigate({
                  to: "/agents/$agentId",
                  params: { agentId: agent.id },
                  search: { ...search, group, cursor: "" },
                })
              }
            >
              {label}
            </Button>
          ))}
        </div>
        <Input
          aria-label="Search Tasks"
          placeholder="Search Tasks…"
          maxLength={400}
          className="h-8 w-full sm:w-64"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>
      <p className="text-xs text-muted-foreground">
        Executions performed by this Agent.
      </p>
      {rows.isError && (
        <ErrorAlert>
          {rows.data && "Showing the last update. "}Could not load Runs.{" "}
          <Button variant="link" onClick={() => void rows.refetch()}>
            Retry Runs
          </Button>
        </ErrorAlert>
      )}
      {rows.isPending ? (
        <p role="status" className="text-sm text-muted-foreground">
          Loading Runs…
        </p>
      ) : rows.data?.items.length ? (
        <div className="min-w-0 overflow-hidden rounded-lg border">
          <RunQueue items={rows.data.items} />
        </div>
      ) : (
        !rows.isError && (
          <p className="py-8 text-center text-sm text-muted-foreground">
            No Runs match these filters.
          </p>
        )
      )}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground">
          {search.cursor ? "Older Runs" : "Newest Runs · updates automatically"}
        </p>
        <div className="flex gap-2">
          {search.cursor && (
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                void navigate({
                  to: "/agents/$agentId",
                  params: { agentId: agent.id },
                  search: { ...search, cursor: "" },
                })
              }
            >
              Newest Runs
            </Button>
          )}
          {rows.data?.next_cursor && (
            <Button
              size="sm"
              variant="outline"
              onClick={() =>
                void navigate({
                  to: "/agents/$agentId",
                  params: { agentId: agent.id },
                  search: { ...search, cursor: rows.data!.next_cursor },
                })
              }
            >
              Older Runs
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
