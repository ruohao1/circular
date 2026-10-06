import { queryOptions } from "@tanstack/react-query";
import { api, type Run, type RunQueueGroup, type RunQueueQuery } from "@/api";

export interface RunQueueSearch {
  group: RunQueueGroup;
  q: string;
  cursor: string;
}
export function parseRunQueueSearch(
  search: Record<string, unknown>,
): RunQueueSearch {
  const group =
    search.group === "active" ||
    search.group === "failed" ||
    search.group === "finished"
      ? search.group
      : "all";
  return {
    group,
    q:
      typeof search.q === "string"
        ? Array.from(search.q.trim()).slice(0, 200).join("")
        : "",
    cursor:
      typeof search.cursor === "string" && search.cursor.length <= 2048
        ? search.cursor
        : "",
  };
}
export function runQueueOptions(project: string, query: RunQueueQuery = {}) {
  const normalized = {
    ...parseRunQueueSearch({ ...query }),
    limit: query.limit ?? 50,
  };
  return queryOptions({
    queryKey: ["run-queue", project, normalized],
    queryFn: () => api.runQueue(project, normalized),
    enabled: !!project,
    retry: false,
    refetchInterval: normalized.cursor
      ? false
      : normalized.group === "all" || normalized.group === "active"
        ? 2_000
        : 15_000,
    refetchIntervalInBackground: false,
  });
}
export function runDurationSeconds(
  run: Pick<Run, "started_at" | "finished_at">,
  now: number,
): number | null {
  if (!run.started_at) return null;
  return Math.max(
    0,
    Math.floor(
      ((run.finished_at ? Date.parse(run.finished_at) : now) -
        Date.parse(run.started_at)) /
        1000,
    ),
  );
}
