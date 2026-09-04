import { api, type Agent, type Project, type Repository } from "./api";

export interface LaunchSelection {
  project: Project;
  repository: Repository;
  agent: Agent;
  title: string;
  description: string;
}

export class LaunchError extends Error {
  constructor(
    message: string,
    readonly taskId: string,
  ) {
    super(message);
  }
}

export async function launchTask(selection: LaunchSelection, client = api) {
  const { project, repository, agent, title, description } = selection;
  if (
    !title.trim() ||
    title.trim().length > 500 ||
    !agent.enabled ||
    repository.project_id !== project.id ||
    agent.project_id !== project.id
  ) {
    throw new Error(
      "Choose a repository and an enabled agent in this project, and enter a task title.",
    );
  }
  const task = await client.createTask({
    project_id: project.id,
    repository_id: repository.id,
    title: title.trim(),
    description,
  });
  try {
    return await client.createRun({ task_id: task.id, agent_id: agent.id });
  } catch (error) {
    throw new LaunchError(
      `Task created, but its Run could not start: ${error instanceof Error ? error.message : "request failed"}`,
      task.id,
    );
  }
}
