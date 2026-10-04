import type { components } from "./generated/api";

export type ExternalRequest = components["schemas"]["ExternalRequest"];
export type ExternalRequestDetail =
  components["schemas"]["ExternalRequestDetail"];
export type LinearRequestRoute = components["schemas"]["LinearRequestRoute"];
export type IntegrationIdentityStatus = components["schemas"]["IdentityStatus"];
export type PRReview = components["schemas"]["PRReviewRead"];
export type PRReviewLaunch = components["schemas"]["PRReviewLaunch"];
export type PRReviewSettings = components["schemas"]["PRReviewSettingsRead"];
export type Project = components["schemas"]["ProjectRead"];
export type Repository = components["schemas"]["RepositoryRead"];
export type Agent = components["schemas"]["AgentRead"];
export type ProjectCreate = components["schemas"]["ProjectCreate"];
export type RepositoryCreate = components["schemas"]["RepositoryCreate"];
export type AgentCreate = components["schemas"]["AgentCreate"];
export type AgentProposal = components["schemas"]["AgentProposalRead"];
export type AgentProposalCreate = components["schemas"]["AgentProposalCreate"];
export type CodexModelCatalog = components["schemas"]["CodexModelCatalog"];
export type Run = components["schemas"]["RunRead"];
export type Execution = components["schemas"]["RunExecutionRead"];
export type RunEvent = components["schemas"]["EventRead"];
export type Artifact = components["schemas"]["ArtifactRead"];
export type Task = components["schemas"]["TaskRead"];
export type Connection = components["schemas"]["ConnectionRead"];
export type Provider = Connection["provider"];
type Schema = components["schemas"];
type TaskCreate = components["schemas"]["TaskCreate"];
type RunCreate = components["schemas"]["RunCreate"];

export const apiUrl =
  (import.meta.env.VITE_API_URL ?? "http://localhost:8000") + "/api/v1";

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}

async function request<T>(
  path: string,
  body?: unknown,
  credentials?: RequestCredentials,
  method = "POST",
): Promise<T> {
  const response = await fetch(`${apiUrl}${path}`, {
    credentials,
    ...(body === undefined
      ? {}
      : {
          method,
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        }),
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    throw new ApiError(
      typeof error.detail === "string"
        ? error.detail
        : `Request failed (${response.status})`,
      response.status,
    );
  }
  return response.json() as Promise<T>;
}

const connectionsPath = (project: string) =>
  `/projects/${encodeURIComponent(project)}/integrations`;
const query = (values: Record<string, string>) =>
  new URLSearchParams(values).toString();

export const api = {
  webhookSettings: (provider: Provider) =>
    request<Schema["WebhookSettings"]>(`/integrations/${provider}/webhooks`),
  saveWebhookSettings: (
    provider: Provider,
    value: Schema["WebhookSettingsUpdate"],
  ) =>
    request<Schema["WebhookSettings"]>(
      `/integrations/${provider}/webhooks`,
      value,
    ),
  checkWebhookSettings: (provider: Provider) =>
    request<Schema["WebhookSettings"]>(
      `/integrations/${provider}/webhooks/check`,
      {},
    ),
  integrationIdentity: (project: string, provider: Provider) =>
    request<Schema["IdentityStatus"]>(
      `${connectionsPath(project)}/${provider}/identity`,
    ),
  saveGitHubIdentity: (project: string, privateKey = "") =>
    request<Schema["IdentityStatus"]>(
      `${connectionsPath(project)}/github/identity/key`,
      { private_key: privateKey },
    ),
  requestRoutes: (project: string) =>
    request<LinearRequestRoute[]>(
      `${connectionsPath(project)}/linear/request-routes`,
    ),
  saveRequestRoute: (
    project: string,
    value: Schema["LinearRequestRouteCreate"],
  ) =>
    request<LinearRequestRoute>(
      `${connectionsPath(project)}/linear/request-routes`,
      value,
    ),
  updateRequestRoute: (
    project: string,
    id: string,
    value: Schema["LinearRequestRouteUpdate"],
  ) =>
    request<LinearRequestRoute>(
      `${connectionsPath(project)}/linear/request-routes/${encodeURIComponent(id)}`,
      value,
    ),
  externalRequests: (project: string, unrouted = false, cursor = "") =>
    request<Schema["ExternalRequestPage"]>(
      `/external-requests?${new URLSearchParams({ ...(unrouted ? { unrouted: "true" } : { project_id: project }), cursor, limit: "20" })}`,
    ),
  externalRequest: (id: string) =>
    request<ExternalRequestDetail>(
      `/external-requests/${encodeURIComponent(id)}`,
    ),
  startExternalRequest: (id: string, fingerprint: string) =>
    request<ExternalRequest>(
      `/external-requests/${encodeURIComponent(id)}/start`,
      { expected_input_fingerprint: fingerprint },
    ),
  routeExternalRequest: (id: string, route: string, fingerprint: string) =>
    request<ExternalRequest>(
      `/external-requests/${encodeURIComponent(id)}/route`,
      { route_id: route, expected_input_fingerprint: fingerprint },
    ),
  stopExternalRequest: (id: string) =>
    request<ExternalRequest>(
      `/external-requests/${encodeURIComponent(id)}/stop`,
      {},
    ),
  connectLinearIdentity: (
    project: string,
    purpose: "identity" | "agent" = "identity",
  ) =>
    request<Schema["ConnectionStart"]>(
      `${connectionsPath(project)}/linear/identity/connect`,
      { purpose },
      "include",
    ),
  bindIntegrationIdentity: (
    project: string,
    provider: Provider,
    identityID: string,
  ) =>
    request<Schema["IdentityStatus"]>(
      `${connectionsPath(project)}/${provider}/identity/bind`,
      { identity_id: identityID },
    ),
  detachIntegrationIdentity: (project: string, provider: Provider) =>
    request<Schema["IdentityStatus"]>(
      `${connectionsPath(project)}/${provider}/identity/detach`,
      {},
    ),
  prReviewSettings: (project: string) =>
    request<PRReviewSettings>(`${connectionsPath(project)}/github/pr-reviews`),
  setPRReviewSettings: (
    project: string,
    body: Schema["PRReviewSettingsUpdate"],
  ) =>
    request<PRReviewSettings>(
      `${connectionsPath(project)}/github/pr-reviews`,
      body,
    ),
  preparePRReview: (run: string, reviewer = "") =>
    request<Schema["PRReviewPreparation"]>(
      `/runs/${encodeURIComponent(run)}/pr-reviews/prepare?${query(reviewer ? { reviewer_id: reviewer } : {})}`,
    ),
  launchPRReview: (run: string, body: PRReviewLaunch) =>
    request<PRReview>(`/runs/${encodeURIComponent(run)}/pr-reviews`, body),
  prReviews: (run: string, cursor = "") =>
    request<Schema["PRReviewPage"]>(
      `/runs/${encodeURIComponent(run)}/pr-reviews?${query({ limit: "20", cursor })}`,
    ),
  prReview: (review: string) =>
    request<PRReview>(`/pr-reviews/${encodeURIComponent(review)}`),
  refreshPRReview: (review: string) =>
    request<PRReview>(`/pr-reviews/${encodeURIComponent(review)}/refresh`, {}),
  retryPRReviewPublication: (review: string) =>
    request<PRReview>(
      `/pr-reviews/${encodeURIComponent(review)}/publication/retry`,
      {},
    ),
  health: () => request<{ status: string }>("/health"),
  mcpConnection: () => request<Schema["MCPConnectionRead"]>("/mcp/connection"),
  projects: () => request<Project[]>("/projects"),
  createProject: (body: ProjectCreate) => request<Project>("/projects", body),
  createRepository: (body: RepositoryCreate) =>
    request<Repository>("/repositories", body),
  createAgent: (body: AgentCreate) => request<Agent>("/agents", body),
  updateAgent: (id: string, body: Schema["AgentUpdate"]) =>
    request<Agent>(
      `/agents/${encodeURIComponent(id)}`,
      body,
      undefined,
      "PATCH",
    ),
  codexModels: () => request<CodexModelCatalog>("/backends/codex/models"),
  agentProposals: (run: string) =>
    request<AgentProposal[]>(
      `/runs/${encodeURIComponent(run)}/agent-proposals`,
    ),
  saveAgentProposal: (run: string, body: AgentProposalCreate) =>
    request<AgentProposal>(
      `/runs/${encodeURIComponent(run)}/agent-proposals`,
      body,
    ),
  createProposedAgent: (
    run: string,
    proposal: string,
    body: AgentProposalCreate,
  ) =>
    request<Agent>(
      `/runs/${encodeURIComponent(run)}/agent-proposals/${encodeURIComponent(proposal)}/create`,
      body,
    ),
  dismissAgentProposal: (run: string, proposal: string) =>
    request<AgentProposal>(
      `/runs/${encodeURIComponent(run)}/agent-proposals/${encodeURIComponent(proposal)}/dismiss`,
      {},
    ),
  repositories: (project: string) =>
    request<Repository[]>(
      `/repositories?project_id=${encodeURIComponent(project)}`,
    ),
  agents: (project: string) =>
    request<Agent[]>(`/agents?project_id=${encodeURIComponent(project)}`),
  prepareDiscovery: (project: string, repository_id: string) =>
    request<Task>(`/projects/${encodeURIComponent(project)}/discovery`, {
      repository_id,
    }),
  runs: (project?: string) =>
    request<Run[]>(
      `/runs${project ? `?project_id=${encodeURIComponent(project)}` : ""}`,
    ),
  execution: (id: string) =>
    request<Execution>(`/runs/${encodeURIComponent(id)}/execution`),
  events: (id: string, after = 0) =>
    request<RunEvent[]>(
      `/runs/${encodeURIComponent(id)}/events?after=${after}&limit=200`,
    ),
  createTask: (body: TaskCreate) => request<Task>("/tasks", body),
  task: (id: string) => request<Task>(`/tasks/${encodeURIComponent(id)}`),
  connections: (project: string) =>
    request<Connection[]>(connectionsPath(project)),
  connect: (project: string, provider: Provider) =>
    request<Schema["ConnectionStart"]>(
      `${connectionsPath(project)}/${provider}/connect`,
      {},
      "include",
    ),
  registerGitHub: (project: string, organization: string) =>
    request<Schema["GitHubAppRegistrationRead"]>(
      `${connectionsPath(project)}/github/register`,
      { organization },
      "include",
    ),
  updateGitHubApp: (project: string, app_url: string) =>
    request<Schema["ProviderAppSaved"]>(
      `${connectionsPath(project)}/github/app`,
      { app_url },
    ),
  saveLinearApp: (project: string, client_id: string) =>
    request<Schema["ProviderAppSaved"]>(
      `${connectionsPath(project)}/linear/app`,
      { client_id },
    ),
  disconnect: (project: string, provider: Provider) =>
    request<Schema["ConnectionDisconnect"]>(
      `${connectionsPath(project)}/${provider}/disconnect`,
      {},
    ),
  githubInstallations: (project: string, page: number) =>
    request<Schema["GitHubInstallationPage"]>(
      `${connectionsPath(project)}/github/installations?page=${page}`,
    ),
  githubRepositories: (project: string, installation: string, page: number) =>
    request<Schema["GitHubRepositoryPage"]>(
      `${connectionsPath(project)}/github/repositories?${query({ installation_id: installation, page: String(page) })}`,
    ),
  importGitHub: (project: string, body: Schema["GitHubImport"]) =>
    request<Repository>(`${connectionsPath(project)}/github/import`, body),
  createGitHubRepository: (
    project: string,
    body: Schema["GitHubRepositoryCreate"],
  ) =>
    request<Schema["GitHubRepositoryCreation"]>(
      `${connectionsPath(project)}/github/repositories`,
      body,
    ),
  linearTeams: (project: string, cursor: string) =>
    request<Schema["LinearTeamPage"]>(
      `${connectionsPath(project)}/linear/teams?${query({ cursor })}`,
    ),
  linearProjects: (project: string, cursor: string) =>
    request<Schema["LinearProjectPage"]>(
      `${connectionsPath(project)}/linear/projects?${query({ cursor })}`,
    ),
  linearIssues: (
    project: string,
    team: string,
    linearProject: string,
    cursor: string,
  ) =>
    request<Schema["LinearIssuePage"]>(
      `${connectionsPath(project)}/linear/issues?${query({ team_id: team, linear_project_id: linearProject, cursor })}`,
    ),
  importLinear: (project: string, body: Schema["LinearImport"]) =>
    request<Task>(`${connectionsPath(project)}/linear/import`, body),
  linearRunUpdates: (project: string) =>
    request<Schema["LinearRunUpdatesRead"]>(
      `${connectionsPath(project)}/linear/run-updates`,
    ),
  setLinearRunUpdates: (project: string, enabled: boolean) =>
    request<Schema["LinearRunUpdatesRead"]>(
      `${connectionsPath(project)}/linear/run-updates`,
      { enabled },
    ),
  runLinearDelivery: (run: string) =>
    request<Schema["LinearRunDeliveryRead"]>(
      `/runs/${encodeURIComponent(run)}/linear-delivery`,
    ),
  githubRunDeliverySettings: (project: string) =>
    request<Schema["GitHubRunDeliverySettingsRead"]>(
      `${connectionsPath(project)}/github/run-delivery`,
    ),
  setGitHubRunDeliverySettings: (project: string, enabled: boolean) =>
    request<Schema["GitHubRunDeliverySettingsRead"]>(
      `${connectionsPath(project)}/github/run-delivery`,
      { enabled },
    ),
  runGitHubDelivery: (run: string) =>
    request<Schema["GitHubRunDeliveryRead"]>(
      `/runs/${encodeURIComponent(run)}/github-delivery`,
    ),
  publishRunGitHub: (run: string) =>
    request<Schema["GitHubRunDeliveryRead"]>(
      `/runs/${encodeURIComponent(run)}/github-delivery`,
      {},
    ),
  createRun: (body: RunCreate) => request<Run>("/runs", body),
  cancel: (id: string) =>
    request<Run>(`/runs/${encodeURIComponent(id)}/cancel`, {}),
  artifactUrl: (artifact: Artifact) =>
    `${apiUrl}/runs/${artifact.run_id}/artifacts/${artifact.id}/content`,
};
