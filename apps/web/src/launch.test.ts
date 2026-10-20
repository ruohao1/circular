import { describe, expect, it, vi } from "vitest";
import { api, type Agent, type Project, type Repository } from "./api";
import { LaunchError, launchTask, validateTaskDraft } from "./launch";

const dates = {
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};
const project: Project = { id: "project", name: "Circular", ...dates };
const repository: Repository = {
  id: "repository",
  project_id: project.id,
  name: "fixture",
  clone_url: "/fixture",
  default_branch: "main",
  ...dates,
};
const agent: Agent = {
  id: "agent",
  project_id: project.id,
  name: "Engineer",
  enabled: true,
  backend: "fake",
  instructions: "",
  preset: null,
  ...dates,
};
const selection = {
  project,
  repository,
  agent,
  title: "  Implement slice  ",
  description: "Test it.",
};

describe("Task to Run launcher", () => {
  it("creates the Task before starting its Run with generated payloads", async () => {
    const order: string[] = [];
    const createTask = vi
      .spyOn(api, "createTask")
      .mockImplementation(async (body) => {
        order.push("task");
        expect(body).toEqual({
          project_id: "project",
          repository_id: "repository",
          title: "Implement slice",
          description: "Test it.",
          priority: "none",
          preferred_agent_id: "agent",
        });
        return {
          ...body,
          ...dates,
          id: "task",
          status: "open",
          priority: body.priority ?? "none",
          preferred_agent_id: body.preferred_agent_id ?? null,
          parent_task_id: body.parent_task_id ?? null,
          revision: 1,
          planned_start_date: null,
          planned_finish_date: null,
        };
      });
    const createRun = vi
      .spyOn(api, "createRun")
      .mockImplementation(async (body) => {
        order.push("run");
        expect(body).toEqual({ task_id: "task", agent_id: "agent" });
        throw new Error("offline");
      });
    await expect(launchTask(selection)).rejects.toMatchObject({
      taskId: "task",
      name: "Error",
    });
    expect(order).toEqual(["task", "run"]);
    createTask.mockRestore();
    createRun.mockRestore();
  });
  it("rejects invalid selections before making a request", async () => {
    const request = vi.spyOn(api, "createTask");
    await expect(
      launchTask({ ...selection, agent: { ...agent, enabled: false } }),
    ).rejects.toThrow();
    await expect(
      launchTask({
        ...selection,
        repository: { ...repository, project_id: "other" },
      }),
    ).rejects.toThrow();
    await expect(launchTask({ ...selection, title: " " })).rejects.toThrow();
    expect(request).not.toHaveBeenCalled();
    request.mockRestore();
  });
  it("retains the saved Task identity for a retry when Run creation fails", () => {
    expect(new LaunchError("unavailable", "task").taskId).toBe("task");
  });
});

describe("saved Task validation", () => {
  it("allows title-only planning without execution resources", () => {
    expect(
      validateTaskDraft(
        {
          title: "  Plan it  ",
          description: "",
          repository_id: null,
          preferred_agent_id: null,
          priority: "none",
        },
        project,
        [],
        [],
      ),
    ).toEqual({
      project_id: project.id,
      title: "Plan it",
      description: "",
      priority: "none",
    });
  });
  it("accepts 500 Unicode code points and rejects wrong scope or disabled preferences", () => {
    const draft = {
      title: "🌍".repeat(500),
      description: "",
      repository_id: null,
      preferred_agent_id: null,
      priority: "high" as const,
    };
    expect(validateTaskDraft(draft, project, [], []).title).toHaveLength(1000);
    expect(() =>
      validateTaskDraft({ ...draft, title: "🌍".repeat(501) }, project, [], []),
    ).toThrow();
    expect(() =>
      validateTaskDraft(
        { ...draft, preferred_agent_id: agent.id },
        project,
        [],
        [{ ...agent, enabled: false }],
      ),
    ).toThrow();
    expect(() =>
      validateTaskDraft(
        { ...draft, repository_id: repository.id },
        project,
        [{ ...repository, project_id: "other" }],
        [],
      ),
    ).toThrow();
  });
});
