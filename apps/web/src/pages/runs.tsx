import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { ArrowDown, ArrowUp, Search } from "lucide-react";
import { useProject } from "@/use-project";
import { parseRunQueueSearch, runQueueOptions } from "@/lib/run-queue";
import { RunQueue } from "@/components/console/run-queue";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";

declare module "@tanstack/react-router" {
  interface HistoryState {
    runQueueProject?: string;
  }
}

export function RunsPage() {
  const { selectedProject } = useProject();
  const location = useRouterState({ select: (state) => state.location });
  // Read URL inputs and their history scope from the same location snapshot.
  const search = useMemo(
    () => parseRunQueueSearch(location.search),
    [location.search],
  );
  const navigate = useNavigate();
  const [query, setQuery] = useState(search.q);
  const [cursorProject, setCursorProject] = useState(selectedProject);
  const historyProject = location.state.runQueueProject;
  // Pagination entries retain their scope across Back/Forward and route remounts.
  const cursorOwner = historyProject ?? cursorProject;
  const changedProject = !!cursorOwner && cursorOwner !== selectedProject;
  const cursor = changedProject ? "" : search.cursor;
  const rows = useQuery(
    runQueueOptions(selectedProject, { ...search, cursor }),
  );
  useEffect(() => {
    setQuery(search.q);
  }, [search.q]);
  useEffect(() => {
    if (!selectedProject) return;
    if (changedProject && search.cursor) {
      void navigate({
        to: "/runs",
        search: { ...search, cursor: "" },
        state: { runQueueProject: selectedProject },
        replace: true,
      });
      return;
    }
    setCursorProject(selectedProject);
  }, [selectedProject, changedProject, navigate, search]);
  useEffect(() => {
    if (query === search.q) return;
    const timer = setTimeout(
      () =>
        void navigate({
          to: "/runs",
          search: { group: search.group, q: query.trim(), cursor: "" },
          replace: true,
        }),
      250,
    );
    return () => clearTimeout(timer);
  }, [query, search.q, search.group, navigate]);
  return (
    <div className="mx-auto max-w-[1450px] space-y-6 px-4 py-7 sm:px-6 lg:px-8 lg:py-9">
      <div>
        <p className="mb-2 text-[10px] font-medium tracking-[0.16em] text-muted-foreground">
          EXECUTION HISTORY
        </p>
        <h1 className="text-2xl font-semibold tracking-tight">Runs</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Every attempt, with the Task and Agent behind it.
        </p>
      </div>
      <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
        <div
          role="group"
          aria-label="Filter Runs"
          className="flex flex-wrap gap-1 rounded-lg border bg-muted/20 p-1"
        >
          {(
            [
              ["all", "All"],
              ["active", "Active"],
              ["failed", "Failed"],
              ["finished", "Finished"],
            ] as const
          ).map(([group, label]) => (
            <Button
              key={group}
              variant={search.group === group ? "secondary" : "ghost"}
              aria-pressed={search.group === group}
              onClick={() =>
                void navigate({
                  to: "/runs",
                  search: { group, q: search.q, cursor: "" },
                })
              }
            >
              {label}
            </Button>
          ))}
        </div>
        <div className="relative w-full md:max-w-80">
          <Search
            className="pointer-events-none absolute top-2 left-3 size-4 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            aria-label="Search Tasks"
            className="pl-9"
            placeholder="Search Tasks…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            maxLength={400}
          />
        </div>
      </div>
      <Card className="min-w-0 gap-0 overflow-hidden py-0">
        {rows.error && (
          <div className="p-5">
            <ErrorAlert>
              {rows.data && <span>Showing the last update. </span>}
              {rows.error.message}{" "}
              <Button variant="link" onClick={() => void rows.refetch()}>
                Retry
              </Button>
            </ErrorAlert>
          </div>
        )}
        {!selectedProject ? (
          <p className="p-8 text-center text-sm text-muted-foreground">
            Choose a Project to see its Runs.
          </p>
        ) : rows.isPending ? (
          <div
            className="space-y-4 p-5"
            role="status"
            aria-label="Loading Runs"
          >
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-3/4" />
          </div>
        ) : rows.data?.items.length ? (
          <RunQueue items={rows.data.items} />
        ) : (
          !rows.error && (
            <div className="space-y-2 p-10 text-center">
              <h2 className="font-medium">No Runs match these filters.</h2>
              <p className="text-sm text-muted-foreground">
                Try another search or start a new Task.
              </p>
            </div>
          )
        )}
      </Card>
      <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground">
        <p>
          {cursor
            ? "Older Runs · refresh from the newest page to follow live updates."
            : "Newest Runs · updates automatically"}
        </p>
        <div className="flex gap-2">
          {cursor && (
            <Button
              variant="outline"
              onClick={() =>
                void navigate({
                  to: "/runs",
                  search: { ...search, cursor: "" },
                })
              }
            >
              <ArrowUp aria-hidden="true" />
              Newest Runs
            </Button>
          )}
          {rows.data?.next_cursor && (
            <Button
              variant="outline"
              onClick={() =>
                void navigate({
                  to: "/runs",
                  search: { ...search, cursor: rows.data!.next_cursor },
                  state: { runQueueProject: selectedProject },
                })
              }
            >
              Older Runs
              <ArrowDown aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
