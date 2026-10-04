import {
  useIsMutating,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowRight,
  Check,
  CheckCircle2,
  Circle,
  Compass,
  FolderOpen,
  GitBranch,
  LoaderCircle,
  Link2,
  Plus,
  Plug,
  Users,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, type Agent, type Project, type Repository } from "@/api";
import { AgentModelSettings } from "@/components/agent-model-settings";
import {
  CodexModelFields,
  resolvedModelSettings,
  useCodexModels,
  type ModelSettings,
} from "@/components/codex-model-fields";
import { EmptyState } from "@/components/empty-state";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { useProject } from "@/use-project";
import { IntegrationsPage, type ConnectionNotice } from "./integrations";
import { MCPSetup } from "./mcp";

export type SetupSection =
  "projects" | "repositories" | "agents" | "integrations" | "mcp";

function DiscoverySetup({
  project,
  agent,
  repositories,
}: {
  project: string;
  agent: Agent;
  repositories: Repository[];
}) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const [repositoryID, setRepositoryID] = useState("");
  const repository =
    repositories.find((item) => item.id === repositoryID) ?? repositories[0];
  const prepare = useMutation({
    mutationKey: ["setup", "discovery"],
    mutationFn: () => api.prepareDiscovery(project, repository!.id),
    onSuccess: async (task) => {
      client.setQueryData(["task", task.id], task);
      await navigate({ to: "/", search: { taskId: task.id } });
    },
  });
  return (
    <div className="space-y-4">
      <p className="text-sm leading-relaxed text-muted-foreground">
        Understand the codebase, its workflow, and what to work on next. This
        agent produces a report and recommends specialist agents suited to your
        repository.
      </p>
      <ResourceSelect
        id="discovery-repository"
        label="Repository to explore"
        value={repository?.id ?? ""}
        onValueChange={setRepositoryID}
        options={repositories.map((item) => ({
          value: item.id,
          label: item.name,
        }))}
        placeholder="Add a repository first"
        disabled={prepare.isPending || !agent.enabled || !repositories.length}
      />
      {!repositories.length && (
        <Button asChild variant="link" className="h-auto p-0">
          <Link to="/setup" search={{ section: "repositories" }}>
            Add a repository <ArrowRight aria-hidden="true" />
          </Link>
        </Button>
      )}
      <Button
        disabled={!repository || !agent.enabled || prepare.isPending}
        onClick={() => prepare.mutate()}
      >
        {prepare.isPending ? (
          <LoaderCircle className="animate-spin" aria-hidden="true" />
        ) : (
          <Compass aria-hidden="true" />
        )}
        Explore repository
      </Button>
      <p className="text-xs text-muted-foreground">
        Opens a discovery task ready for you to start.
      </p>
      {prepare.error && <ErrorAlert>{prepare.error.message}</ErrorAlert>}
      <details className="text-xs text-muted-foreground">
        <summary className="cursor-pointer font-medium text-foreground">
          Agent instructions
        </summary>
        <p className="mt-3 whitespace-pre-wrap leading-relaxed [overflow-wrap:anywhere]">
          {agent.instructions}
        </p>
      </details>
    </div>
  );
}

function Collection({
  title,
  count,
  pending,
  error,
  empty,
  children,
}: {
  title: string;
  count: number;
  pending: boolean;
  error: Error | null;
  empty: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card className="h-fit min-w-0">
      <CardHeader className="border-b">
        <CardTitle className="flex items-center justify-between gap-3">
          <h2>{title}</h2>
          <Badge variant="secondary">{count}</Badge>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {error ? (
          <ErrorAlert>{error.message}</ErrorAlert>
        ) : pending ? (
          <div
            role="status"
            aria-label={`Loading ${title.toLowerCase()}`}
            className="space-y-4"
          >
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-4 w-full" />
          </div>
        ) : count ? (
          <ul className="divide-y">{children}</ul>
        ) : (
          empty
        )}
      </CardContent>
    </Card>
  );
}

function FormCard({
  title,
  description,
  children,
  onSubmit,
  pending,
  disabled,
  valid,
  error,
  success,
  submitLabel,
  next,
}: {
  title: string;
  description: string;
  children: ReactNode;
  onSubmit: () => void;
  pending: boolean;
  disabled?: boolean;
  valid: boolean;
  error: Error | null;
  success?: string;
  submitLabel: string;
  next?: ReactNode;
}) {
  return (
    <Card className="h-fit min-w-0 gap-0 py-0">
      <CardHeader className="border-b py-5">
        <CardTitle>
          <h2>{title}</h2>
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <form
        aria-label={title}
        onSubmit={(event) => {
          event.preventDefault();
          if (valid && !pending && !disabled) onSubmit();
        }}
      >
        <CardContent className="space-y-5 py-6">
          <fieldset
            disabled={pending || disabled}
            className="grid min-w-0 gap-5"
          >
            {children}
          </fieldset>
          {error && <ErrorAlert>{error.message}</ErrorAlert>}
          {success && (
            <p
              role="status"
              className="flex items-start gap-2 text-sm text-success"
            >
              <CheckCircle2
                className="mt-0.5 size-4 shrink-0"
                aria-hidden="true"
              />
              <span>{success}</span>
            </p>
          )}
        </CardContent>
        <CardFooter className="flex flex-wrap gap-3 py-4">
          <Button type="submit" disabled={pending || disabled || !valid}>
            {pending ? (
              <LoaderCircle className="animate-spin" aria-hidden="true" />
            ) : (
              <Plus aria-hidden="true" />
            )}
            {pending ? "Saving…" : submitLabel}
          </Button>
          {success && next}
        </CardFooter>
      </form>
    </Card>
  );
}

function ProjectForm({ onContinue }: { onContinue: () => void }) {
  const client = useQueryClient();
  const { selectProject } = useProject();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationKey: ["setup", "projects"],
    mutationFn: api.createProject,
    onSuccess: (project) => {
      client.setQueryData<Project[]>(["projects"], (current = []) => [
        project,
        ...current,
      ]);
      selectProject(project.id);
      void client.invalidateQueries({ queryKey: ["projects"] });
      setName("");
      setDescription("");
    },
  });

  return (
    <FormCard
      title="Create project"
      description="Group repositories and tasks. A Repository discovery agent is included to help you understand the project and plan its team."
      onSubmit={() =>
        create.mutate({
          name: name.trim(),
          description: description.trim() || null,
        })
      }
      pending={create.isPending}
      valid={!!name.trim()}
      error={create.error}
      success={
        create.isSuccess
          ? `“${create.data.name}” created and selected.`
          : undefined
      }
      submitLabel="Create project"
      next={
        <Button type="button" variant="outline" onClick={onContinue}>
          Add a repository <ArrowRight aria-hidden="true" />
        </Button>
      }
    >
      <div className="grid gap-2">
        <Label htmlFor="project-name">Project name</Label>
        <Input
          id="project-name"
          required
          maxLength={200}
          placeholder="My project"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="project-description">
          Description{" "}
          <span className="font-normal text-muted-foreground">(optional)</span>
        </Label>
        <Textarea
          id="project-description"
          rows={4}
          placeholder="What are you building?"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
    </FormCard>
  );
}

function RepositoryForm({
  project,
  onContinue,
}: {
  project?: Project;
  onContinue: () => void;
}) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [cloneUrl, setCloneUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const create = useMutation({
    mutationKey: ["setup", "repositories"],
    mutationFn: api.createRepository,
    onSuccess: (repository) => {
      client.setQueryData<Repository[]>(
        ["repositories", repository.project_id],
        (current = []) => [repository, ...current],
      );
      void client.invalidateQueries({
        queryKey: ["repositories", repository.project_id],
      });
      setName("");
      setCloneUrl("");
      setBranch("main");
    },
  });

  return (
    <FormCard
      title="Add repository"
      description={
        project
          ? `Register source code for ${project.name}.`
          : "Select or create a project first."
      }
      onSubmit={() => {
        if (project)
          create.mutate({
            project_id: project.id,
            name: name.trim(),
            clone_url: cloneUrl.trim(),
            default_branch: branch.trim(),
          });
      }}
      pending={create.isPending}
      disabled={!project}
      valid={!!name.trim() && !!cloneUrl.trim() && !!branch.trim()}
      error={create.error}
      success={
        create.isSuccess
          ? `“${create.data.name}” added to this project.`
          : undefined
      }
      submitLabel="Add repository"
      next={
        <Button type="button" variant="outline" onClick={onContinue}>
          View agents <ArrowRight aria-hidden="true" />
        </Button>
      }
    >
      <div className="grid gap-2">
        <Label htmlFor="repository-name">Repository name</Label>
        <Input
          id="repository-name"
          required
          maxLength={200}
          placeholder="Website"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="grid gap-2">
        <Label htmlFor="clone-url">Clone URL or path</Label>
        <Input
          id="clone-url"
          required
          spellCheck={false}
          autoCapitalize="none"
          aria-describedby="clone-url-help"
          placeholder="https://github.com/owner/project.git"
          value={cloneUrl}
          onChange={(e) => setCloneUrl(e.target.value)}
        />
        <p
          id="clone-url-help"
          className="text-xs leading-relaxed text-muted-foreground"
        >
          Use a Git source accessible to Circular. Local paths must be available
          where the worker runs.
        </p>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="default-branch">Default branch</Label>
        <Input
          id="default-branch"
          required
          spellCheck={false}
          autoCapitalize="none"
          aria-describedby="branch-help"
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
        />
        <p
          id="branch-help"
          className="text-xs leading-relaxed text-muted-foreground"
        >
          The branch must already contain a commit. Circular checks access when
          a Run starts.
        </p>
      </div>
    </FormCard>
  );
}

function AgentForm({ project }: { project?: Project }) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [backend, setBackend] = useState("codex");
  const [model, setModel] = useState<ModelSettings>({});
  const catalog = useCodexModels();
  const [instructions, setInstructions] = useState("");
  const create = useMutation({
    mutationKey: ["setup", "agents"],
    mutationFn: api.createAgent,
    onSuccess: (agent) => {
      client.setQueryData<Agent[]>(
        ["agents", agent.project_id],
        (current = []) => [agent, ...current],
      );
      void client.invalidateQueries({ queryKey: ["agents", agent.project_id] });
      setName("");
      setModel({});
      setInstructions("");
    },
  });

  return (
    <FormCard
      title="Create agent"
      description={
        project
          ? `Add a custom specialization for ${project.name}. The built-in discovery agent can help you decide which roles are useful.`
          : "Select or create a project first."
      }
      onSubmit={() => {
        if (project)
          create.mutate({
            project_id: project.id,
            name: name.trim(),
            backend,
            instructions: instructions.trim(),
            backend_config:
              backend === "codex" && catalog.data
                ? resolvedModelSettings(catalog.data, model)
                : {},
          });
      }}
      pending={create.isPending}
      disabled={!project}
      valid={
        !!name.trim() &&
        (backend !== "codex" || (!!catalog.data && model.model !== ""))
      }
      error={create.error}
      success={
        create.isSuccess
          ? `“${create.data.name}” created and available in the launcher.`
          : undefined
      }
      submitLabel="Create agent"
      next={
        <Button variant="outline" asChild>
          <Link to="/">
            Go to Runs <ArrowRight aria-hidden="true" />
          </Link>
        </Button>
      }
    >
      <div className="grid gap-2">
        <Label htmlFor="agent-name">Agent name</Label>
        <Input
          id="agent-name"
          required
          maxLength={200}
          placeholder="Implementation engineer"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <div className="grid gap-2">
        <ResourceSelect
          id="agent-backend"
          label="Backend"
          value={backend}
          onValueChange={setBackend}
          disabled={!project || create.isPending}
          options={[
            { value: "codex", label: "Codex" },
            { value: "fake", label: "Test (fake)" },
          ]}
          placeholder="Select a backend"
          required
        />
        <p className="text-xs leading-relaxed text-muted-foreground">
          {backend === "codex"
            ? "Uses Circular’s configured Codex connection."
            : "Runs a simulated task to verify setup without using a model."}
        </p>
      </div>
      {backend === "codex" &&
        (catalog.data ? (
          <CodexModelFields
            key={create.data?.id ?? "new"}
            id="new-agent"
            catalog={catalog.data}
            value={model}
            onChange={setModel}
            disabled={!project || create.isPending}
          />
        ) : catalog.error ? (
          <ErrorAlert>
            Could not load model choices.{" "}
            <Button
              type="button"
              variant="link"
              onClick={() => void catalog.refetch()}
            >
              Retry
            </Button>
          </ErrorAlert>
        ) : (
          <p className="text-xs text-muted-foreground">
            Loading model choices…
          </p>
        ))}
      <div className="grid gap-2">
        <Label htmlFor="agent-instructions">
          Instructions{" "}
          <span className="font-normal text-muted-foreground">(optional)</span>
        </Label>
        <Textarea
          id="agent-instructions"
          rows={4}
          placeholder="Implement the task and run relevant checks."
          value={instructions}
          onChange={(e) => setInstructions(e.target.value)}
        />
      </div>
    </FormCard>
  );
}

export function SetupPage({
  section,
  onSectionChange,
  notice,
}: {
  section: SetupSection;
  onSectionChange: (section: SetupSection) => void;
  notice?: ConnectionNotice;
}) {
  const { projects, project, selectedProject, selectProject } = useProject();
  const saving = useIsMutating({ mutationKey: ["setup"] }) > 0;
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
  const hasRepository = !!repositories.data?.length;
  const hasAgent = !!agents.data?.some((agent) => agent.enabled);
  const ready = !!project && hasRepository && hasAgent;
  const missingProject = (
    <EmptyState
      icon={<FolderOpen aria-hidden="true" />}
      title="Choose a project first"
      description={
        <>
          <span>
            Repositories, agents, and connections belong to a project.
          </span>
          <Button
            variant="link"
            className="mt-2"
            onClick={() => onSectionChange("projects")}
          >
            Create or select a project <ArrowRight aria-hidden="true" />
          </Button>
        </>
      }
    />
  );

  return (
    <>
      <header className="flex h-14 items-center justify-between gap-4 border-b bg-sidebar/50 px-4 text-xs sm:px-6 lg:px-8">
        <span>
          Workspace <span className="text-muted-foreground">/ Setup</span>
        </span>
        <span className="hidden text-muted-foreground sm:inline">
          Your project and its connections
        </span>
      </header>
      <div className="mx-auto max-w-[1450px] space-y-7 px-4 py-7 sm:px-6 lg:px-8 lg:py-9">
        <div className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
          <div className="space-y-2">
            <p className="text-[10px] font-medium tracking-[0.18em] text-muted-foreground">
              WORKSPACE SETUP
            </p>
            <h1 className="text-2xl font-semibold tracking-tight">
              A home for your next task.
            </h1>
            <p className="text-sm text-muted-foreground">
              Connect your source code and choose who does the work.
            </p>
          </div>
          <ResourceSelect
            id="setup-project"
            label="Project"
            className="w-full md:w-64"
            value={selectedProject}
            onValueChange={selectProject}
            options={(projects.data ?? []).map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            disabled={saving}
            placeholder={
              projects.isPending ? "Loading projects…" : "No projects yet"
            }
          />
        </div>
        {projects.error && (
          <ErrorAlert>
            Could not load projects: {projects.error.message}
          </ErrorAlert>
        )}
        <Card className="gap-4 py-5">
          <CardContent className="flex flex-col justify-between gap-5 lg:flex-row lg:items-center">
            <div className="space-y-3 min-w-0">
              <h2 className="font-medium">
                {ready ? "Ready to launch" : "Get ready for your first Run"}
              </h2>
              <div className="flex flex-wrap gap-x-5 gap-y-3">
                {(
                  [
                    { section: "projects", label: "Project", done: !!project },
                    {
                      section: "repositories",
                      label: "Repository",
                      done: hasRepository,
                    },
                    { section: "agents", label: "Agent", done: hasAgent },
                  ] as const
                ).map((step) => (
                  <Button
                    key={step.section}
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => onSectionChange(step.section)}
                    className="h-auto justify-start p-0 text-muted-foreground hover:bg-transparent hover:text-foreground"
                  >
                    {step.done ? (
                      <CheckCircle2
                        className="size-4 text-success"
                        aria-hidden="true"
                      />
                    ) : (
                      <Circle className="size-4" aria-hidden="true" />
                    )}
                    <span>
                      {step.label}
                      <span className="sr-only">
                        {step.done ? " complete" : " needed"}
                      </span>
                    </span>
                  </Button>
                ))}
              </div>
            </div>
            {ready ? (
              <Button asChild className="w-fit">
                <Link to="/">
                  Go to Runs <ArrowRight aria-hidden="true" />
                </Link>
              </Button>
            ) : (
              <Button
                className="w-fit"
                onClick={() =>
                  onSectionChange(
                    !project
                      ? "projects"
                      : !hasRepository
                        ? "repositories"
                        : "agents",
                  )
                }
              >
                Continue setup <ArrowRight aria-hidden="true" />
              </Button>
            )}
          </CardContent>
        </Card>
        <Tabs
          value={section}
          onValueChange={(value) => {
            if (
              value === "projects" ||
              value === "repositories" ||
              value === "agents" ||
              value === "integrations" ||
              value === "mcp"
            )
              onSectionChange(value);
          }}
          className="gap-5"
        >
          <TabsList className="grid w-full grid-cols-2 group-data-horizontal/tabs:h-auto sm:w-fit sm:grid-cols-5">
            <TabsTrigger value="projects" className="h-auto py-2">
              <FolderOpen aria-hidden="true" />
              Projects
            </TabsTrigger>
            <TabsTrigger value="repositories" className="h-auto py-2">
              <GitBranch aria-hidden="true" />
              Repositories
            </TabsTrigger>
            <TabsTrigger value="agents" className="h-auto py-2">
              <Users aria-hidden="true" />
              Agents
            </TabsTrigger>
            <TabsTrigger value="integrations" className="h-auto py-2">
              <Link2 aria-hidden="true" />
              Integrations
            </TabsTrigger>
            <TabsTrigger value="mcp" className="h-auto py-2">
              <Plug aria-hidden="true" />
              MCP
            </TabsTrigger>
          </TabsList>
          <TabsContent value="mcp">
            <MCPSetup />
          </TabsContent>
          <TabsContent value="integrations">
            {project ? (
              <IntegrationsPage
                key={selectedProject}
                project={selectedProject}
                notice={notice}
              />
            ) : (
              <Card>{missingProject}</Card>
            )}
          </TabsContent>
          <TabsContent
            value="projects"
            className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(340px,0.9fr)]"
          >
            <Collection
              title="Your projects"
              count={projects.data?.length ?? 0}
              pending={projects.isPending}
              error={projects.error}
              empty={
                <EmptyState
                  icon={<FolderOpen aria-hidden="true" />}
                  title="No projects yet"
                  description="Create a project to start organizing your work."
                />
              }
            >
              {projects.data?.map((item) => (
                <li
                  key={item.id}
                  className="flex items-start justify-between gap-4 py-4 first:pt-0 last:pb-0"
                >
                  <div className="min-w-0 space-y-1">
                    <h3 className="break-words text-sm font-medium">
                      {item.name}
                    </h3>
                    <p className="break-words text-xs leading-relaxed text-muted-foreground">
                      {item.description || "No description"}
                    </p>
                  </div>
                  {item.id === selectedProject ? (
                    <Badge className="shrink-0" variant="secondary">
                      <Check aria-hidden="true" />
                      Selected
                    </Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={saving}
                      onClick={() => selectProject(item.id)}
                      aria-label={`Select ${item.name}`}
                    >
                      Select
                    </Button>
                  )}
                </li>
              ))}
            </Collection>
            <ProjectForm onContinue={() => onSectionChange("repositories")} />
          </TabsContent>
          <TabsContent
            value="repositories"
            className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(340px,0.9fr)]"
          >
            <Collection
              title="Project repositories"
              count={repositories.data?.length ?? 0}
              pending={!!project && repositories.isPending}
              error={repositories.error}
              empty={
                !project ? (
                  missingProject
                ) : (
                  <EmptyState
                    icon={<GitBranch aria-hidden="true" />}
                    title="No repositories yet"
                    description="Add a Git repository for agents to work on."
                  />
                )
              }
            >
              {repositories.data?.map((item) => (
                <li
                  key={item.id}
                  className="space-y-2 py-4 first:pt-0 last:pb-0"
                >
                  <h3 className="break-words text-sm font-medium">
                    {item.name}
                  </h3>
                  <p className="text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
                    {item.clone_url}
                  </p>
                  <div className="flex flex-wrap items-center gap-3">
                    <Badge variant="outline" className="max-w-full">
                      <GitBranch aria-hidden="true" />
                      <span className="truncate">{item.default_branch}</span>
                    </Badge>
                    <Button
                      variant="link"
                      size="sm"
                      className="h-auto p-0"
                      onClick={() => onSectionChange("agents")}
                    >
                      View discovery agent <ArrowRight aria-hidden="true" />
                    </Button>
                  </div>
                </li>
              ))}
            </Collection>
            <RepositoryForm
              key={selectedProject}
              project={project}
              onContinue={() => onSectionChange("agents")}
            />
          </TabsContent>
          <TabsContent
            value="agents"
            className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(340px,0.9fr)]"
          >
            <Collection
              title="Project agents"
              count={agents.data?.length ?? 0}
              pending={!!project && agents.isPending}
              error={agents.error}
              empty={
                !project ? (
                  missingProject
                ) : (
                  <EmptyState
                    icon={<Users aria-hidden="true" />}
                    title="No agents yet"
                    description="Create an agent and choose its backend to run tasks."
                  />
                )
              }
            >
              {agents.data?.map((item) => (
                <li
                  key={item.id}
                  className="space-y-2 py-4 first:pt-0 last:pb-0"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <h3 className="break-words text-sm font-medium">
                      {item.name}
                    </h3>
                    <Badge variant="outline">
                      {item.enabled ? "Enabled" : "Disabled"}
                    </Badge>
                  </div>
                  <div className="flex min-w-0 flex-wrap gap-2">
                    {item.preset === "repository-discovery" && (
                      <Badge variant="outline">Built-in</Badge>
                    )}
                    <Badge variant="secondary">
                      {item.backend === "codex"
                        ? "Codex"
                        : item.backend === "fake"
                          ? "Test (fake)"
                          : item.backend}
                    </Badge>
                  </div>
                  {item.backend === "codex" && (
                    <AgentModelSettings agent={item} />
                  )}
                  {item.preset === "repository-discovery" ? (
                    <DiscoverySetup
                      key={item.id}
                      project={selectedProject}
                      agent={item}
                      repositories={repositories.data ?? []}
                    />
                  ) : (
                    <p className="whitespace-pre-wrap text-xs leading-relaxed text-muted-foreground [overflow-wrap:anywhere]">
                      {item.instructions || "No additional instructions"}
                    </p>
                  )}
                </li>
              ))}
            </Collection>
            <AgentForm key={selectedProject} project={project} />
          </TabsContent>
        </Tabs>
      </div>
    </>
  );
}
