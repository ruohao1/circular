import { useState, type ReactNode } from "react";
import { useIsMutating, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  BookOpen,
  CircleDot,
  ListTodo,
  LayoutDashboard,
  Inbox,
  Menu,
  Plus,
  SlidersHorizontal,
} from "lucide-react";
import type { ExternalRequestDetail } from "@/api";
import { useProject } from "@/use-project";
import { useTaskLauncher } from "./task-launcher-context";
import { ResourceSelect } from "@/components/resource-select";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

function Orbit() {
  return (
    <svg
      viewBox="0 0 64 64"
      fill="none"
      className="size-8 shrink-0 text-primary"
      aria-hidden="true"
    >
      <path
        d="M46.3 17.7a20.2 20.2 0 1 0 0 28.6"
        stroke="currentColor"
        strokeWidth="5.5"
        strokeLinecap="round"
      />
      <circle cx="32" cy="32" r="5" fill="currentColor" />
    </svg>
  );
}
function WorkspaceNavigation({ onNavigate }: { onNavigate?(): void }) {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const items = [
    {
      to: "/" as const,
      label: "Overview",
      icon: LayoutDashboard,
      active: pathname === "/",
    },
    {
      to: "/tasks" as const,
      label: "Tasks",
      icon: ListTodo,
      active: pathname === "/tasks",
    },
    {
      to: "/runs" as const,
      label: "Runs",
      icon: CircleDot,
      active: pathname.startsWith("/runs"),
    },
    {
      to: "/requests" as const,
      label: "Requests",
      icon: Inbox,
      active: pathname.startsWith("/requests"),
    },
    {
      to: "/setup" as const,
      label: "Setup",
      icon: SlidersHorizontal,
      active: pathname === "/setup",
    },
    { to: "/docs" as const, label: "Docs", icon: BookOpen, active: false },
  ];
  return (
    <nav aria-label="Workspace" className="grid gap-1">
      {items.map((item) => (
        <Button
          key={item.to}
          asChild
          variant={item.active ? "secondary" : "ghost"}
          className="justify-start gap-3"
        >
          <Link
            to={item.to}
            search={item.to === "/setup" ? { section: "projects" } : undefined}
            onClick={onNavigate}
            aria-current={item.active ? "page" : undefined}
          >
            <item.icon aria-hidden="true" />
            {item.label}
          </Link>
        </Button>
      ))}
    </nav>
  );
}
export function ConsoleShell({ children }: { children: ReactNode }) {
  const { projects, selectedProject, selectProject, selectionLocked } =
    useProject();
  const saving = useIsMutating({ mutationKey: ["setup"] }) > 0;
  const { openNewTask } = useTaskLauncher();
  const [menuOpen, setMenuOpen] = useState(false);
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const navigate = useNavigate();
  const client = useQueryClient();
  const tasksPage = pathname === "/tasks";
  function chooseProject(id: string) {
    if (selectionLocked || saving || id === selectedProject) return;
    const request = pathname.startsWith("/requests/")
      ? client.getQueryData<ExternalRequestDetail>([
          "external-request",
          pathname.split("/")[2],
        ])
      : undefined;
    if (
      pathname.startsWith("/runs/") ||
      (pathname.startsWith("/requests/") && request?.project_id)
    )
      void navigate({ to: "/" });
    selectProject(id);
  }
  return (
    <div className="min-h-svh sm:grid sm:grid-cols-[190px_minmax(0,1fr)] lg:grid-cols-[220px_minmax(0,1fr)]">
      <aside className="hidden border-r border-sidebar-border bg-sidebar text-sidebar-foreground sm:flex sm:flex-col sm:px-4 sm:py-5">
        <Link
          to="/"
          className="mb-12 flex items-center gap-2 rounded-lg px-2 text-lg font-semibold tracking-tight focus-visible:outline-ring"
        >
          <Orbit />
          Circular
        </Link>
        <p className="mb-3 px-3 text-[10px] font-medium tracking-[0.16em] text-muted-foreground">
          WORKSPACE
        </p>
        <WorkspaceNavigation />
        <p className="mt-auto px-3 pt-12 text-xs leading-relaxed text-muted-foreground">
          Your agents.
          <br />
          One clear workflow.
        </p>
      </aside>
      <div className="min-w-0">
        <header
          className={
            tasksPage
              ? "flex h-11 items-center gap-2 border-b bg-sidebar/40 px-4 sm:px-6 lg:px-8"
              : "flex min-h-20 items-center gap-3 border-b bg-sidebar/40 px-4 py-3 sm:px-6 lg:px-8"
          }
        >
          <Button
            variant="ghost"
            size="icon"
            className="shrink-0 sm:hidden"
            aria-label="Open navigation"
            onClick={() => setMenuOpen(true)}
          >
            <Menu aria-hidden="true" />
          </Button>
          {tasksPage ? (
            <nav aria-label="Breadcrumb" className="min-w-0">
              <ol className="flex min-w-0 items-center gap-2 text-[13px]">
                <li className="min-w-0">
                  <Select
                    name="project"
                    value={selectedProject}
                    onValueChange={chooseProject}
                    disabled={
                      selectionLocked ||
                      saving ||
                      projects.isPending ||
                      !projects.data?.length
                    }
                  >
                    <SelectTrigger
                      id="project"
                      aria-label="Project"
                      size="sm"
                      className="max-w-64 min-w-0 border-transparent px-1.5 text-[13px] dark:bg-transparent"
                    >
                      <SelectValue
                        placeholder={
                          projects.isPending
                            ? "Loading Projects…"
                            : "No Projects yet"
                        }
                      />
                    </SelectTrigger>
                    <SelectContent
                      position="popper"
                      align="start"
                      className="max-w-[calc(100vw-2rem)]"
                    >
                      <SelectGroup>
                        {(projects.data ?? []).map((project) => (
                          <SelectItem
                            key={project.id}
                            value={project.id}
                            textValue={project.name}
                          >
                            <span className="truncate">{project.name}</span>
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </li>
                <li aria-hidden="true" className="text-muted-foreground/60">
                  /
                </li>
                <li className="shrink-0">
                  <h1 aria-current="page" className="font-medium">
                    Tasks
                  </h1>
                </li>
              </ol>
            </nav>
          ) : (
            <>
              <ResourceSelect
                id="project"
                label="Project"
                className="w-full max-w-64"
                value={selectedProject}
                onValueChange={chooseProject}
                options={(projects.data ?? []).map((p) => ({
                  value: p.id,
                  label: p.name,
                }))}
                disabled={selectionLocked || saving || projects.isPending}
                placeholder={
                  projects.isPending ? "Loading Projects…" : "No Projects yet"
                }
              />
              <Button
                id="new-task"
                className="ml-auto"
                disabled={
                  !selectedProject ||
                  projects.isPending ||
                  selectionLocked ||
                  saving
                }
                onClick={() => openNewTask()}
              >
                <Plus aria-hidden="true" />
                New Task
              </Button>
            </>
          )}
        </header>
        {projects.error && (
          <div className="px-4 pt-4 sm:px-6">
            <ErrorAlert>
              Could not load Projects: {projects.error.message}{" "}
              <Button variant="link" onClick={() => void projects.refetch()}>
                Retry
              </Button>
            </ErrorAlert>
          </div>
        )}
        {projects.isSuccess && !projects.data.length && (
          <div className="mx-4 mt-5 flex flex-wrap items-center justify-between gap-3 rounded-xl border p-5 sm:mx-6">
            <div>
              <h2 className="font-medium">No Projects yet</h2>
              <p className="mt-1 text-sm text-muted-foreground">
                Create a Project to connect a Repository and start your first
                Run.
              </p>
            </div>
            <Button asChild variant="outline">
              <Link to="/setup" search={{ section: "projects" }}>
                Open setup
              </Link>
            </Button>
          </div>
        )}
        <main className="min-w-0">{children}</main>
      </div>
      <Dialog open={menuOpen} onOpenChange={setMenuOpen}>
        <DialogContent
          className="max-w-sm"
          onCloseAutoFocus={(e) => {
            e.preventDefault();
            document
              .querySelector<HTMLButtonElement>(
                '[aria-label="Open navigation"]',
              )
              ?.focus();
          }}
        >
          <DialogTitle className="mb-2 flex items-center gap-2 text-lg font-semibold">
            <Orbit />
            Circular
          </DialogTitle>
          <DialogDescription className="mb-6 text-sm text-muted-foreground">
            Your workspace
          </DialogDescription>
          <WorkspaceNavigation onNavigate={() => setMenuOpen(false)} />
        </DialogContent>
      </Dialog>
    </div>
  );
}
