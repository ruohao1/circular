import {
  api,
  type Agent,
  type Project,
  type Repository,
  type TaskCreate,
  type TaskPriority,
} from "./api";

export interface TaskDraft {
  title: string;
  description: string;
  repository_id: string | null;
  preferred_agent_id: string | null;
  priority: TaskPriority;
}
export function validateTaskDraft(
  draft: TaskDraft,
  project: Project,
  repositories: readonly Repository[],
  agents: readonly Agent[],
): TaskCreate {
  const title = draft.title.trim();
  if (!title || Array.from(title).length > 500)
    throw new Error("Enter a Task title of 1–500 characters.");
  if (
    draft.repository_id &&
    !repositories.some(
      (r) => r.id === draft.repository_id && r.project_id === project.id,
    )
  )
    throw new Error("Choose a Repository in this Project.");
  if (
    draft.preferred_agent_id &&
    !agents.some(
      (a) =>
        a.id === draft.preferred_agent_id &&
        a.project_id === project.id &&
        a.enabled,
    )
  )
    throw new Error("Choose an enabled preferred Agent in this Project.");
  return {
    project_id: project.id,
    title,
    description: draft.description,
    priority: draft.priority,
    ...(draft.repository_id ? { repository_id: draft.repository_id } : {}),
    ...(draft.preferred_agent_id
      ? { preferred_agent_id: draft.preferred_agent_id }
      : {}),
  };
}

export interface LaunchSelection {
  project: Project;
  repository: Repository;
  agent: Agent;
  title: string;
  description: string;
  priority?: TaskPriority;
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
    !description.trim() ||
    Array.from(title.trim()).length > 500 ||
    !agent.enabled ||
    repository.project_id !== project.id ||
    agent.project_id !== project.id
  ) {
    throw new Error(
      "Choose a repository and an enabled agent in this project, and enter a task title and description.",
    );
  }
  const task = await client.createTask(
    validateTaskDraft(
      {
        title,
        description,
        repository_id: repository.id,
        preferred_agent_id: agent.id,
        priority: selection.priority ?? "none",
      },
      project,
      [repository],
      [agent],
    ),
  );
  try {
    return await client.createRun({ task_id: task.id, agent_id: agent.id });
  } catch (error) {
    throw new LaunchError(
      `Task created, but its Run could not start: ${error instanceof Error ? error.message : "request failed"}`,
      task.id,
    );
  }
}
