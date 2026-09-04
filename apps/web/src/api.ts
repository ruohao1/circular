import type { components } from "./generated/api";

export type Project = components["schemas"]["ProjectRead"];
export type Repository = components["schemas"]["RepositoryRead"];
export type Agent = components["schemas"]["AgentRead"];
export type Run = components["schemas"]["RunRead"];
export type Execution = components["schemas"]["RunExecutionRead"];
export type RunEvent = components["schemas"]["EventRead"];
export type Artifact = components["schemas"]["ArtifactRead"];
type Task = components["schemas"]["TaskRead"];
type TaskCreate = components["schemas"]["TaskCreate"];
type RunCreate = components["schemas"]["RunCreate"];

export const apiUrl =
  (import.meta.env.VITE_API_URL ?? "http://localhost:8000") + "/api/v1";

async function request<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(
    `${apiUrl}${path}`,
    body === undefined
      ? undefined
      : {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
  );
  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    throw new Error(
      typeof error.detail === "string"
        ? error.detail
        : `Request failed (${response.status})`,
    );
  }
  return response.json() as Promise<T>;
}

export const api = {
  projects: () => request<Project[]>("/projects"),
  repositories: (project: string) =>
    request<Repository[]>(
      `/repositories?project_id=${encodeURIComponent(project)}`,
    ),
  agents: (project: string) =>
    request<Agent[]>(`/agents?project_id=${encodeURIComponent(project)}`),
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
  createRun: (body: RunCreate) => request<Run>("/runs", body),
  cancel: (id: string) =>
    request<Run>(`/runs/${encodeURIComponent(id)}/cancel`, {}),
  artifactUrl: (artifact: Artifact) =>
    `${apiUrl}/runs/${artifact.run_id}/artifacts/${artifact.id}/content`,
};
