import { useEffect, useState } from "react";
import type { Run } from "@/api";
import { runDurationSeconds } from "@/lib/run-queue";
export function RunDuration({ run }: { run: Run }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (
      !run.started_at ||
      run.finished_at ||
      ["succeeded", "failed", "cancelled"].includes(run.status)
    )
      return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [run.started_at, run.finished_at, run.status]);
  const seconds = runDurationSeconds(run, now);
  return (
    <span className="whitespace-nowrap tabular-nums">
      {seconds === null
        ? "Not started"
        : seconds < 60
          ? `${seconds}s`
          : seconds < 3600
            ? `${Math.floor(seconds / 60)}m ${seconds % 60}s`
            : `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`}
    </span>
  );
}
