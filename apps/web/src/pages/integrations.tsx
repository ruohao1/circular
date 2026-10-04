import { useEffect, useState, type ReactNode } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowRight,
  Check,
  ExternalLink,
  GitBranch,
  Link2,
  LoaderCircle,
  LockKeyhole,
  RefreshCw,
} from "lucide-react";
import { api, ApiError, type Connection, type Provider } from "@/api";
import { EmptyState } from "@/components/empty-state";
import { ErrorAlert } from "@/components/error-alert";
import { GitHubCreateRepository } from "@/components/github-create-repository";
import { PRReviewSettings } from "@/components/pr-review-settings";
import { GitHubRunPublishing } from "@/components/github-run-publishing";
import { LinearRunUpdates } from "@/components/linear-run-updates";
import { IntegrationIdentity } from "@/components/integration-identity";
import { ResourceSelect } from "@/components/resource-select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

function submitGitHubManifest(registration: {
  registration_url: string;
  manifest: string;
}) {
  const form = document.createElement("form");
  form.method = "POST";
  form.action = registration.registration_url;
  const manifest = document.createElement("input");
  manifest.type = "hidden";
  manifest.name = "manifest";
  manifest.value = registration.manifest;
  form.append(manifest);
  document.body.append(form);
  form.submit();
  form.remove();
}

export type ConnectionNotice = {
  provider: Provider;
  result: "connected" | "cancelled" | "failed";
};

function ProviderError({
  project,
  error,
}: {
  project: string;
  error: Error | null;
}) {
  const client = useQueryClient();
  useEffect(() => {
    if (error instanceof ApiError && error.status === 409)
      void client.invalidateQueries({ queryKey: ["connections", project] });
  }, [client, error, project]);
  return error ? <ErrorAlert>{error.message}</ErrorAlert> : null;
}

function More({
  visible,
  pending,
  onClick,
  children,
}: {
  visible: boolean;
  pending: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return visible ? (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={pending}
      onClick={onClick}
    >
      {pending && <LoaderCircle className="animate-spin" aria-hidden="true" />}
      {children}
    </Button>
  ) : null;
}

function GitHubBrowser({
  project,
  installationURL,
}: {
  project: string;
  installationURL: string;
}) {
  const client = useQueryClient();
  const [installationID, setInstallationID] = useState("");
  const [page, setPage] = useState(1);
  const installations = useInfiniteQuery({
    queryKey: ["integration", project, "github", "installations"],
    initialPageParam: 1,
    queryFn: ({ pageParam }) => api.githubInstallations(project, pageParam),
    getNextPageParam: (last) => last.next_page || undefined,
    retry: false,
  });
  const accounts =
    installations.data?.pages.flatMap((part) => part.items) ?? [];
  const installation =
    accounts.find((item) => item.id === installationID) ?? accounts[0];
  const managementURL = installation?.management_url || installationURL;
  const repositories = useQuery({
    queryKey: [
      "integration",
      project,
      "github",
      "repositories",
      installation?.id,
      page,
    ],
    queryFn: () => api.githubRepositories(project, installation!.id, page),
    enabled: !!installation,
    retry: false,
  });
  const registered = useQuery({
    queryKey: ["repositories", project],
    queryFn: () => api.repositories(project),
  });
  const add = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (id: string) =>
      api.importGitHub(project, {
        installation_id: installation!.id,
        repository_id: id,
        page: String(page),
      }),
    onSuccess: () =>
      client.invalidateQueries({ queryKey: ["repositories", project] }),
  });

  return (
    <div className="space-y-5 border-t pt-5">
      <div className="flex flex-wrap items-end gap-3">
        <ResourceSelect
          id="github-account"
          label="GitHub account"
          className="min-w-0 basis-full sm:basis-48 sm:flex-1"
          value={installation?.id ?? ""}
          onValueChange={(value) => {
            setInstallationID(value);
            setPage(1);
            add.reset();
          }}
          options={accounts.map((item) => ({
            value: item.id,
            label: item.account,
          }))}
          placeholder={
            installations.isPending
              ? "Loading accounts…"
              : "No installations available"
          }
          disabled={add.isPending}
        />
        <Button
          variant="outline"
          size="icon"
          aria-label="Refresh GitHub repositories"
          onClick={() =>
            void client.invalidateQueries({
              queryKey: ["integration", project, "github"],
            })
          }
        >
          <RefreshCw aria-hidden="true" />
        </Button>
        <GitHubCreateRepository
          key={project}
          project={project}
          installation={installation}
          appURL={installationURL.replace(/\/installations\/new$/, "")}
        />
      </div>
      <More
        visible={installations.hasNextPage}
        pending={installations.isFetchingNextPage}
        onClick={() => void installations.fetchNextPage()}
      >
        Load more accounts
      </More>
      <ProviderError
        project={project}
        error={
          installations.error ||
          repositories.error ||
          registered.error ||
          add.error
        }
      />
      {installations.isSuccess && !accounts.length && (
        <EmptyState
          title="Install the GitHub App"
          description="Choose which repositories Circular can access, then refresh this list."
        />
      )}
      {installation && repositories.isPending && (
        <Skeleton className="h-20 w-full" />
      )}
      {repositories.isSuccess && !repositories.data.items.length && (
        <EmptyState
          title="No repositories on this page"
          description="Create a repository, or allow access to an existing one in your GitHub App installation."
        />
      )}
      <ul className="divide-y">
        {repositories.data?.items.map((item) => {
          const added = registered.data?.some((current) => {
            const reference = current.external_refs?.github;
            return (
              reference &&
              typeof reference === "object" &&
              "repository_id" in reference &&
              reference.repository_id === item.id
            );
          });
          return (
            <li
              key={item.id}
              className="flex flex-wrap items-center justify-between gap-3 py-4 first:pt-0 last:pb-0"
            >
              <div className="min-w-0 flex-1 space-y-2">
                <p className="break-words text-sm font-medium">{item.name}</p>
                <div className="flex flex-wrap gap-2">
                  <Badge variant="outline">
                    <GitBranch aria-hidden="true" />
                    {item.default_branch}
                  </Badge>
                  <Badge variant="secondary">
                    {item.private && <LockKeyhole aria-hidden="true" />}
                    {item.private ? "Private" : "Public"}
                  </Badge>
                </div>
              </div>
              <Button
                variant={added ? "secondary" : "outline"}
                size="sm"
                disabled={add.isPending || added}
                onClick={() => add.mutate(item.id)}
                aria-label={`${added ? "Added" : "Add"} ${item.name}`}
              >
                {added ? (
                  <Check aria-hidden="true" />
                ) : add.isPending && add.variables === item.id ? (
                  <LoaderCircle className="animate-spin" aria-hidden="true" />
                ) : null}
                {added ? "Added" : "Add repository"}
              </Button>
            </li>
          );
        })}
      </ul>
      {(page > 1 || !!repositories.data?.next_page) && (
        <div className="flex items-center justify-between gap-3">
          <Button
            variant="outline"
            size="sm"
            disabled={page === 1 || add.isPending}
            onClick={() => setPage(page - 1)}
          >
            Previous
          </Button>
          <span className="text-xs text-muted-foreground">Page {page}</span>
          <Button
            variant="outline"
            size="sm"
            disabled={!repositories.data?.next_page || add.isPending}
            onClick={() => setPage(repositories.data!.next_page)}
          >
            Next
          </Button>
        </div>
      )}
      {add.isSuccess && (
        <p role="status" className="text-sm text-success">
          Repository added to this project.{" "}
          <Link
            to="/setup"
            search={{ section: "repositories" }}
            className="underline underline-offset-4"
          >
            View repositories
          </Link>
        </p>
      )}
      {managementURL && (
        <Button asChild variant="link" className="h-auto p-0">
          <a href={managementURL} target="_blank" rel="noreferrer">
            Manage GitHub access <ExternalLink aria-hidden="true" />
          </a>
        </Button>
      )}
    </div>
  );
}

function LinearBrowser({ project }: { project: string }) {
  const navigate = useNavigate();
  const client = useQueryClient();
  const [teamID, setTeamID] = useState("");
  const [linearProject, setLinearProject] = useState("all");
  const [repositoryID, setRepositoryID] = useState("");
  const teams = useInfiniteQuery({
    queryKey: ["integration", project, "linear", "teams"],
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.linearTeams(project, pageParam),
    getNextPageParam: (last) => last.next_cursor || undefined,
    retry: false,
  });
  const projects = useInfiniteQuery({
    queryKey: ["integration", project, "linear", "projects"],
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.linearProjects(project, pageParam),
    getNextPageParam: (last) => last.next_cursor || undefined,
    retry: false,
  });
  const teamOptions = teams.data?.pages.flatMap((part) => part.items) ?? [];
  const team = teamOptions.find((item) => item.id === teamID) ?? teamOptions[0];
  const issues = useInfiniteQuery({
    queryKey: [
      "integration",
      project,
      "linear",
      "issues",
      team?.id,
      linearProject,
    ],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api.linearIssues(
        project,
        team!.id,
        linearProject === "all" ? "" : linearProject,
        pageParam,
      ),
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !!team,
    retry: false,
  });
  const repositories = useQuery({
    queryKey: ["repositories", project],
    queryFn: () => api.repositories(project),
  });
  const repository =
    repositories.data?.find((item) => item.id === repositoryID) ??
    repositories.data?.[0];
  const add = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (id: string) =>
      api.importLinear(project, {
        repository_id: repository!.id,
        issue_id: id,
      }),
    onSuccess: (task) => {
      client.setQueryData(["task", task.id], task);
      void navigate({ to: "/", search: { taskId: task.id } });
    },
  });
  const items = issues.data?.pages.flatMap((part) => part.items) ?? [];

  return (
    <div className="space-y-5 border-t pt-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <ResourceSelect
            id="linear-team"
            label="Linear team"
            value={team?.id ?? ""}
            onValueChange={setTeamID}
            options={teamOptions.map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            placeholder={
              teams.isPending ? "Loading teams…" : "No teams available"
            }
            disabled={add.isPending}
          />
          <More
            visible={teams.hasNextPage}
            pending={teams.isFetchingNextPage}
            onClick={() => void teams.fetchNextPage()}
          >
            Load more teams
          </More>
        </div>
        <div className="space-y-2">
          <ResourceSelect
            id="linear-project"
            label="Linear project"
            value={linearProject}
            onValueChange={setLinearProject}
            options={[
              { value: "all", label: "All projects" },
              ...(projects.data?.pages
                .flatMap((part) => part.items)
                .map((item) => ({ value: item.id, label: item.name })) ?? []),
            ]}
            placeholder="All projects"
            disabled={add.isPending}
          />
          <More
            visible={projects.hasNextPage}
            pending={projects.isFetchingNextPage}
            onClick={() => void projects.fetchNextPage()}
          >
            Load more projects
          </More>
        </div>
      </div>
      <ResourceSelect
        id="linear-repository"
        label="Circular repository"
        value={repository?.id ?? ""}
        onValueChange={setRepositoryID}
        options={(repositories.data ?? []).map((item) => ({
          value: item.id,
          label: item.name,
        }))}
        placeholder={
          repositories.isPending
            ? "Loading repositories…"
            : "Add a repository first"
        }
        disabled={add.isPending}
      />
      <p className="text-xs leading-relaxed text-muted-foreground">
        Choose the source code for the imported Task. An issue already imported
        into this project opens its existing Task.
      </p>
      {repositories.isSuccess && !repository && (
        <Button asChild variant="outline" size="sm">
          <Link to="/setup" search={{ section: "repositories" }}>
            Add a repository <ArrowRight aria-hidden="true" />
          </Link>
        </Button>
      )}
      <ProviderError
        project={project}
        error={
          teams.error ||
          projects.error ||
          issues.error ||
          repositories.error ||
          add.error
        }
      />
      {teams.isSuccess && !team && (
        <EmptyState
          title="No teams available"
          description="Check which teams your connected Linear account can access."
        />
      )}
      {team && issues.isPending && <Skeleton className="h-20 w-full" />}
      {issues.isSuccess && !items.length && (
        <EmptyState
          title="No issues found"
          description="Choose another team or project to find work to import."
        />
      )}
      <ul className="divide-y">
        {items.map((item) => (
          <li
            key={item.id}
            className="flex flex-wrap items-center justify-between gap-3 py-4 first:pt-0 last:pb-0"
          >
            <div className="min-w-0 flex-1 space-y-2">
              <p className="text-xs text-muted-foreground">
                {item.identifier} <span className="mx-1">·</span>{" "}
                {item.state.name}
              </p>
              <p className="break-words text-sm font-medium">{item.title}</p>
            </div>
            <Button
              variant="outline"
              size="sm"
              disabled={!repository || add.isPending}
              onClick={() => add.mutate(item.id)}
              aria-label={`Import ${item.identifier}`}
            >
              {add.isPending && add.variables === item.id && (
                <LoaderCircle className="animate-spin" aria-hidden="true" />
              )}
              Import issue <ArrowRight aria-hidden="true" />
            </Button>
          </li>
        ))}
      </ul>
      <More
        visible={issues.hasNextPage}
        pending={issues.isFetchingNextPage}
        onClick={() => void issues.fetchNextPage()}
      >
        Load more issues
      </More>
    </div>
  );
}

function ConnectionCard({
  project,
  connection,
}: {
  project: string;
  connection: Connection;
}) {
  const client = useQueryClient();
  const [appActive, setAppActive] = useState(false);
  const provider = connection.provider;
  const name = provider === "github" ? "GitHub" : "Linear";
  const [setupOpen, setSetupOpen] = useState(false);
  const [githubSettingsOpen, setGithubSettingsOpen] = useState(false);
  const [githubAppURL, setGithubAppURL] = useState("");
  const [organization, setOrganization] = useState("");
  const [linearClientID, setLinearClientID] = useState(
    connection.app_client_id ?? "",
  );
  const connect = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: () => api.connect(project, provider),
    onSuccess: (data) => window.location.assign(data.authorization_url),
  });
  const disconnect = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: () => api.disconnect(project, provider),
    onSuccess: async () => {
      await client.cancelQueries({
        queryKey: ["integration", project, provider],
      });
      client.removeQueries({ queryKey: ["integration", project, provider] });
      await client.invalidateQueries({ queryKey: ["connections", project] });
    },
  });
  const registration = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: () => api.registerGitHub(project, organization.trim()),
    onSuccess: submitGitHubManifest,
  });
  const saveApp = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: async () => {
      await api.saveLinearApp(project, linearClientID.trim());
      await client.invalidateQueries({ queryKey: ["connections"] });
      return api.connect(project, "linear");
    },
    onSuccess: (data) => window.location.assign(data.authorization_url),
  });
  const updateGitHubApp = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: () => api.updateGitHubApp(project, githubAppURL.trim()),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["connections"] });
      setGithubSettingsOpen(false);
    },
  });
  const status = {
    not_configured: "Setup required",
    disconnected: "Disconnected",
    connected: "Connected",
    reconnect_required: "Reconnect required",
  }[connection.status];
  const linearBotPaused =
    provider === "linear" &&
    connection.identity_mode === "app" &&
    connection.status === "disconnected";
  const connectionStatus = linearBotPaused ? "Bot paused" : status;
  const pending =
    connect.isPending ||
    disconnect.isPending ||
    registration.isPending ||
    saveApp.isPending ||
    updateGitHubApp.isPending;
  return (
    <Card className="h-fit min-w-0" aria-label={`${name} connection`}>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="flex items-center gap-2">
            <Link2
              className="size-4 text-muted-foreground"
              aria-hidden="true"
            />
            {name}
          </h2>
          <Badge
            variant={
              connection.status === "connected" ? "secondary" : "outline"
            }
          >
            {connectionStatus}
          </Badge>
        </CardTitle>
        <CardDescription>
          {provider === "github"
            ? "Connect your repositories and choose who publishes PRs and reviews."
            : "Connect your issues, choose who posts updates and give Circular work."}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <dl className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">
            {provider === "github" ? "Repository access" : "Issue access"}
          </dt>
          <dd className="min-w-0 break-words font-medium">
            {connection.status === "connected"
              ? connection.account_name || "Connected"
              : connection.status === "reconnect_required"
                ? "Authorization needed"
                : linearBotPaused
                  ? "Resume bot to restore access"
                  : "Not connected"}
          </dd>
        </dl>
        {(connection.status === "connected" ||
          connection.identity_mode === "app") && (
          <IntegrationIdentity
            key={`${project}-${provider}`}
            project={project}
            connection={connection}
            onActive={setAppActive}
          />
        )}
        {connection.status === "reconnect_required" && (
          <p className="text-sm text-muted-foreground">
            {provider === "linear" && connection.identity_mode === "app"
              ? "Reconnect the bot above to restore access."
              : "Open Connection settings to reconnect your account."}
          </p>
        )}
        {!connection.configured && !connection.setup_available && (
          <div className="space-y-3 rounded-lg border bg-muted/20 p-4">
            <p className="text-sm text-muted-foreground">
              Register{" "}
              {provider === "github" ? "a GitHub App" : "a Linear OAuth app"}{" "}
              and configure it on the Circular server to enable this connection.
            </p>
            <details className="text-xs text-muted-foreground">
              <summary className="cursor-pointer font-medium text-foreground">
                App registration details
              </summary>
              <div className="mt-3 space-y-3">
                <p>Callback URL</p>
                <code className="block select-all break-all rounded-md border bg-background p-3">
                  {connection.callback_url}
                </code>
                <a
                  className="inline-flex items-center gap-1 underline underline-offset-4"
                  href={
                    provider === "github"
                      ? "https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app"
                      : "https://linear.app/developers/oauth-2-0-authentication"
                  }
                  target="_blank"
                  rel="noreferrer"
                >
                  {name} app setup guide{" "}
                  <ExternalLink className="size-3" aria-hidden="true" />
                </a>
              </div>
            </details>
          </div>
        )}
        {!connection.configured && connection.setup_available && !setupOpen && (
          <p className="text-sm leading-relaxed text-muted-foreground">
            Connect starts the one-time setup for this Circular server. You’ll
            sign in and approve access on {name}.
          </p>
        )}
        {setupOpen && connection.setup_available && (
          <div className="space-y-4 rounded-lg border bg-muted/20 p-4">
            {provider === "github" ? (
              <>
                <h3 className="text-sm font-medium">Create your GitHub App</h3>
                <p className="text-sm leading-relaxed text-muted-foreground">
                  GitHub will ask you to name the app and select repositories.
                  Circular fills in the settings and saves the app credentials
                  automatically.
                </p>
                <div className="grid gap-2">
                  <Label htmlFor="github-organization">
                    Organization{" "}
                    <span className="font-normal text-muted-foreground">
                      (optional)
                    </span>
                  </Label>
                  <Input
                    id="github-organization"
                    value={organization}
                    onChange={(event) => setOrganization(event.target.value)}
                    maxLength={39}
                    placeholder="Leave blank for your personal account"
                    disabled={pending}
                    autoCapitalize="none"
                    spellCheck={false}
                  />
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  Access: read source code in the repositories you select.
                </p>
                <Button
                  disabled={pending}
                  onClick={() => registration.mutate()}
                >
                  {registration.isPending && (
                    <LoaderCircle className="animate-spin" aria-hidden="true" />
                  )}
                  Continue on GitHub <ExternalLink aria-hidden="true" />
                </Button>
              </>
            ) : (
              <form
                aria-label="Set up Linear"
                className="space-y-4"
                onSubmit={(event) => {
                  event.preventDefault();
                  if (linearClientID.trim() && !pending) saveApp.mutate();
                }}
              >
                <h3 className="text-sm font-medium">Set up Linear once</h3>
                <p className="text-sm leading-relaxed text-muted-foreground">
                  Open the prefilled app form, create the app in your workspace,
                  then copy its Client ID here. Circular handles the sign-in and
                  return to this console.
                </p>
                <Button asChild variant="outline">
                  <a
                    href={connection.registration_url}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Create Linear app <ExternalLink aria-hidden="true" />
                  </a>
                </Button>
                <div className="grid gap-2">
                  <Label htmlFor="linear-client-id">Linear Client ID</Label>
                  <Input
                    id="linear-client-id"
                    value={linearClientID}
                    onChange={(event) => setLinearClientID(event.target.value)}
                    maxLength={200}
                    required
                    disabled={pending}
                    autoCapitalize="none"
                    autoComplete="off"
                    spellCheck={false}
                  />
                </div>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  Use the public Client ID. No client secret or personal API key
                  is needed.
                </p>
                <Button
                  type="submit"
                  disabled={pending || !linearClientID.trim()}
                >
                  {saveApp.isPending && (
                    <LoaderCircle className="animate-spin" aria-hidden="true" />
                  )}
                  Save and connect Linear <ArrowRight aria-hidden="true" />
                </Button>
              </form>
            )}
            <Button
              variant="ghost"
              size="sm"
              disabled={pending}
              onClick={() => setSetupOpen(false)}
            >
              Cancel setup
            </Button>
          </div>
        )}
        {(provider === "github" || connection.identity_mode !== "app") && (
          <details
            className="rounded-lg border p-3"
            open={connection.status !== "connected" || undefined}
          >
            <summary className="cursor-pointer text-sm font-medium">
              Connection settings
            </summary>
            <div className="space-y-4 pt-4">
              {githubSettingsOpen && (
                <form
                  aria-label="Update GitHub app URL"
                  className="space-y-4 rounded-lg border bg-muted/20 p-4"
                  onSubmit={(event) => {
                    event.preventDefault();
                    if (githubAppURL.trim() && !pending)
                      updateGitHubApp.mutate();
                  }}
                >
                  <div className="grid gap-2">
                    <Label htmlFor="github-app-url">GitHub app URL</Label>
                    <Input
                      id="github-app-url"
                      value={githubAppURL}
                      onChange={(event) => setGithubAppURL(event.target.value)}
                      placeholder="https://github.com/apps/your-app"
                      maxLength={512}
                      required
                      disabled={pending}
                      autoCapitalize="none"
                      autoComplete="off"
                      spellCheck={false}
                      aria-describedby="github-app-url-help"
                    />
                    <p
                      id="github-app-url-help"
                      className="text-sm text-muted-foreground"
                    >
                      If you renamed the app on GitHub, paste its new URL here.
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-3">
                    <Button
                      type="submit"
                      disabled={pending || !githubAppURL.trim()}
                    >
                      {updateGitHubApp.isPending && (
                        <LoaderCircle
                          className="animate-spin"
                          aria-hidden="true"
                        />
                      )}
                      Save GitHub app URL
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      disabled={pending}
                      onClick={() => {
                        setGithubSettingsOpen(false);
                        updateGitHubApp.reset();
                      }}
                    >
                      Cancel
                    </Button>
                  </div>
                </form>
              )}
              <div className="flex flex-wrap gap-3">
                {!setupOpen &&
                  !githubSettingsOpen &&
                  !(
                    provider === "linear" && connection.identity_mode === "app"
                  ) && (
                    <Button
                      disabled={
                        (!connection.configured &&
                          !connection.setup_available) ||
                        pending
                      }
                      onClick={() =>
                        connection.configured
                          ? connect.mutate()
                          : setSetupOpen(true)
                      }
                    >
                      {connect.isPending && (
                        <LoaderCircle
                          className="animate-spin"
                          aria-hidden="true"
                        />
                      )}
                      {connection.status === "connected" ||
                      connection.status === "reconnect_required"
                        ? `Reconnect ${name}`
                        : `Connect ${name}`}
                    </Button>
                  )}
                {!githubSettingsOpen &&
                  provider === "github" &&
                  connection.configured &&
                  connection.app_editable && (
                    <Button
                      variant="outline"
                      aria-label="GitHub app settings"
                      disabled={pending}
                      onClick={() => {
                        setGithubAppURL(
                          connection.installation_url.replace(
                            /\/installations\/new$/,
                            "",
                          ),
                        );
                        updateGitHubApp.reset();
                        setGithubSettingsOpen(true);
                      }}
                    >
                      App settings
                    </Button>
                  )}
                {!setupOpen &&
                  provider === "linear" &&
                  connection.configured &&
                  connection.app_editable && (
                    <Button
                      variant="outline"
                      disabled={pending}
                      onClick={() => {
                        setLinearClientID(connection.app_client_id);
                        setSetupOpen(true);
                      }}
                    >
                      App settings
                    </Button>
                  )}
                {!(
                  provider === "linear" && connection.identity_mode === "app"
                ) &&
                  (connection.status === "connected" ||
                    connection.status === "reconnect_required") && (
                    <Button
                      variant="outline"
                      disabled={pending}
                      onClick={() => disconnect.mutate()}
                    >
                      {disconnect.isPending && (
                        <LoaderCircle
                          className="animate-spin"
                          aria-hidden="true"
                        />
                      )}
                      Disconnect {name}
                    </Button>
                  )}
              </div>
              <ProviderError
                project={project}
                error={
                  connect.error ||
                  disconnect.error ||
                  registration.error ||
                  saveApp.error ||
                  updateGitHubApp.error
                }
              />
              {updateGitHubApp.isSuccess && (
                <p role="status" className="text-sm text-success">
                  GitHub app URL updated.
                </p>
              )}
              {provider === "github" &&
                connection.configured &&
                connection.status !== "connected" &&
                connection.installation_url && (
                  <Button asChild variant="link" className="h-auto p-0">
                    <a
                      href={connection.installation_url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      Install GitHub App <ExternalLink aria-hidden="true" />
                    </a>
                  </Button>
                )}
              {disconnect.isSuccess && (
                <p role="status" className="text-sm text-muted-foreground">
                  Disconnected from Circular.
                  {!disconnect.data.revocation_confirmed &&
                    " Provider revocation could not be confirmed. Remove access in your provider’s app settings as well."}
                </p>
              )}
            </div>
          </details>
        )}
        {(connection.status === "connected" || appActive) &&
          !disconnect.isPending &&
          (provider === "github" ? (
            <>
              <GitHubRunPublishing
                project={project}
                installationURL={connection.installation_url}
              />
              <PRReviewSettings projectID={project} />
              <GitHubBrowser
                project={project}
                installationURL={connection.installation_url}
              />
            </>
          ) : (
            <>
              <LinearRunUpdates
                project={project}
                reconnect={() => connect.mutate()}
                connecting={pending}
              />
              <LinearBrowser project={project} />
            </>
          ))}
      </CardContent>
    </Card>
  );
}

export function IntegrationsPage({
  project,
  notice,
}: {
  project: string;
  notice?: ConnectionNotice;
}) {
  const connections = useQuery({
    queryKey: ["connections", project],
    queryFn: () => api.connections(project),
    retry: false,
  });
  return (
    <div className="space-y-5">
      <p className="text-sm text-muted-foreground">
        Connections belong to this Circular project. Codex uses its own login.
      </p>
      {notice &&
        (notice.result === "connected" ? (
          <p role="status" className="text-sm text-success">
            {notice.provider === "github" ? "GitHub" : "Linear"} connected.
          </p>
        ) : (
          <ErrorAlert>
            {notice.result === "cancelled"
              ? "Connection cancelled. You can connect again when ready."
              : "The connection could not be verified. Try connecting again."}
          </ErrorAlert>
        ))}
      {connections.error && (
        <ErrorAlert>{connections.error.message}</ErrorAlert>
      )}
      {connections.isPending && (
        <div
          role="status"
          aria-label="Loading connections"
          className="grid gap-6 xl:grid-cols-2"
        >
          <Skeleton className="h-60 w-full" />
          <Skeleton className="h-60 w-full" />
        </div>
      )}
      <div className="grid items-start gap-6 xl:grid-cols-2">
        {connections.data?.map((connection) => (
          <ConnectionCard
            key={`${project}:${connection.provider}`}
            project={project}
            connection={connection}
          />
        ))}
      </div>
    </div>
  );
}
