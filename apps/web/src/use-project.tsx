import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
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
  const [locks, setLocks] = useState(0);
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.projects });
  const project =
    projects.data?.find((item) => item.id === selectedId) ?? projects.data?.[0];
  const selectProject = useCallback((id: string) => {
    setSelectedId(id);
    try {
      localStorage.setItem(storageKey, id);
    } catch {
      /* Selection also works without storage. */
    }
  }, []);
  const acquireSelectionLock = useCallback(() => {
    let released = false;
    setLocks((n) => n + 1);
    return () => {
      if (!released) {
        released = true;
        setLocks((n) => n - 1);
      }
    };
  }, []);
  return {
    projects,
    project,
    selectedProject: project?.id ?? "",
    selectProject,
    selectionLocked: locks > 0,
    acquireSelectionLock,
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
export function useProjectSelectionLock(locked: boolean) {
  const { acquireSelectionLock } = useProject();
  useEffect(() => {
    if (locked) return acquireSelectionLock();
  }, [locked, acquireSelectionLock]);
}
