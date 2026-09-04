import { describe, expect, it, vi } from "vitest";
import { api, type Agent, type Project, type Repository } from "./api";
import { LaunchError, launchTask } from "./launch";

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
        });
        return { ...body, ...dates, id: "task", status: "open" };
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
