import { Link } from "@tanstack/react-router";
import type { RunQueueItem } from "@/api";
import { RunStatus } from "@/components/run-status";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { RunDuration } from "@/components/console/run-duration";

export function RunQueue({ items }: { items: RunQueueItem[] }) {
  return (
    <Table className="min-w-[640px] table-fixed">
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-[40%] px-5">Task</TableHead>
          <TableHead className="w-[20%]">Agent</TableHead>
          <TableHead className="w-[23%]">Status</TableHead>
          <TableHead className="w-[17%] pr-5 text-right">Duration</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map(({ run, task_title, agent_name, repository }) => (
          <TableRow key={run.id}>
            <TableCell className="px-5 py-4 whitespace-normal align-top">
              <Link
                to="/runs/$runId"
                params={{ runId: run.id }}
                className="block rounded-sm font-medium leading-relaxed break-words text-foreground hover:text-primary focus-visible:ring-2 focus-visible:ring-ring"
              >
                {task_title}
              </Link>
              <p className="mt-1 text-xs leading-relaxed break-words text-muted-foreground">
                {repository?.name ?? "No Repository"}
              </p>
              <p className="mt-2 text-[11px] text-muted-foreground">
                <span className="font-mono">{run.id.slice(0, 8)}</span> ·
                Attempt {run.attempt}
              </p>
            </TableCell>
            <TableCell className="py-4 whitespace-normal align-top">
              <p className="break-words text-sm">{agent_name}</p>
              <p className="mt-1 text-xs text-muted-foreground">
                {run.backend}
              </p>
              {run.kind === "pr_review" && (
                <Badge className="mt-2" variant="secondary">
                  PR review
                </Badge>
              )}
            </TableCell>
            <TableCell className="py-4 whitespace-normal align-top">
              <RunStatus value={run.status} />
              <time
                dateTime={run.created_at}
                title={new Date(run.created_at).toLocaleString()}
                className="mt-2 block text-[11px] text-muted-foreground"
              >
                {new Date(run.created_at).toLocaleString(undefined, {
                  month: "short",
                  day: "numeric",
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </time>
            </TableCell>
            <TableCell className="py-4 pr-5 text-right align-top text-xs text-muted-foreground">
              <RunDuration run={run} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
