import { useState } from "react";
import { Download, FileText } from "lucide-react";
import { Markdown, CopyText } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import type { RunEvent } from "@/api";

export function RunReport({
  events,
  finished,
  runID,
}: {
  events: RunEvent[];
  finished: boolean;
  runID: string;
}) {
  const [source, setSource] = useState(false);
  const messages: string[] = [];
  let partial = "";
  for (const event of events) {
    if (
      event.type === "agent.message.delta" &&
      typeof event.data.delta === "string"
    )
      partial += event.data.delta;
    if (
      event.type === "agent.message.completed" &&
      typeof event.data.content === "string"
    ) {
      messages.push(event.data.content);
      partial = "";
    }
  }
  const report = partial || messages.at(-1) || "";
  const earlier = partial ? messages : messages.slice(0, -1);
  if (!report)
    return (
      <EmptyState
        icon={<FileText aria-hidden="true" />}
        description={
          finished
            ? "No agent output was recorded."
            : "The agent is working. Its updates will appear here."
        }
      />
    );
  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/20 px-4 py-2 sm:px-6">
        <div className="flex gap-1" role="group" aria-label="Report format">
          <Button
            size="sm"
            variant={!source ? "secondary" : "ghost"}
            aria-pressed={!source}
            onClick={() => setSource(false)}
          >
            Preview
          </Button>
          <Button
            size="sm"
            variant={source ? "secondary" : "ghost"}
            aria-pressed={source}
            onClick={() => setSource(true)}
          >
            Markdown
          </Button>
        </div>
        <div className="flex items-center gap-1">
          <CopyText text={report} label="Copy report" />
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              const url = URL.createObjectURL(
                new Blob([report], { type: "text/markdown;charset=utf-8" }),
              );
              const anchor = document.createElement("a");
              anchor.href = url;
              anchor.download = `circular-report-${runID.slice(0, 8)}.md`;
              anchor.click();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
            }}
          >
            <Download aria-hidden="true" />
            <span className="hidden sm:inline">Download</span>
            <span className="sr-only sm:hidden">Download report</span>
          </Button>
        </div>
      </div>
      <div className="agent-output px-5 py-6 sm:px-7 sm:py-8">
        {source ? (
          <pre className="whitespace-pre-wrap font-mono text-xs leading-6 [overflow-wrap:anywhere]">
            {report}
          </pre>
        ) : (
          <Markdown>{report}</Markdown>
        )}
      </div>
      {earlier.length > 0 && (
        <details className="border-t px-5 py-4 sm:px-7">
          <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
            Earlier updates ({earlier.length})
          </summary>
          <div className="mt-4 space-y-5">
            {earlier.map((message, index) => (
              <Markdown
                key={index}
                className="border-l-2 pl-4 text-muted-foreground"
              >
                {message}
              </Markdown>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}
