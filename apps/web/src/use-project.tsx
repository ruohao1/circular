import { useQuery } from "@tanstack/react-query";
import { createContext, useContext, useState, type ReactNode } from "react";
import { api } from "./api";

const storageKey = "circular.selected-project";

function savedProject() {
  try {
    return localStorage.getItem(storageKey) ?? "";
  } catch {
    return "";
  }
}

function useProjectSelection() {
  const [selectedId, setSelectedId] = useState(savedProject);
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects });
  const project =
    projects.data?.find((item) => item.id === selectedId) ?? projects.data?.[0];

  function selectProject(id: string) {
    setSelectedId(id);
    try {
      localStorage.setItem(storageKey, id);
    } catch {
      // Selection still works when browser storage is unavailable.
    }
  }

  return {
    projects,
    project,
    selectedProject: project?.id ?? "",
    selectProject,
  };
}

const ProjectContext = createContext<ReturnType<
  typeof useProjectSelection
> | null>(null);

export function ProjectProvider({ children }: { children: ReactNode }) {
  const selection = useProjectSelection();
  return (
    <ProjectContext.Provider value={selection}>
      {children}
    </ProjectContext.Provider>
  );
}

export function useProject() {
  const selection = useContext(ProjectContext);
  if (!selection) throw new Error("ProjectProvider is required");
  return selection;
}
