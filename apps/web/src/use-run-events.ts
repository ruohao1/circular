import { useEffect, useState } from "react";
import { api, apiUrl, type RunEvent } from "./api";
import { emptyEvents, mergeEvents, terminalEvents } from "./run-events";

const eventNames = [
  "run.started",
  ...terminalEvents,
  "workspace.provisioning",
  "workspace.ready",
  "workspace.released",
  "workspace.failed",
  "agent.message.delta",
  "agent.message.completed",
  "usage.updated",
  "git.diff.updated",
  "artifact.created",
  "tool.execution.output",
  "file.changed",
];

export function useRunEvents(runId: string, lastSequence = 0) {
  const [state, setState] = useState(emptyEvents);
  const [connection, setConnection] = useState("Connecting");
  useEffect(() => {
    let cancelled = false;
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let current = emptyEvents;
    setState(current);
    setConnection("Connecting");
    const merge = (events: RunEvent[]) => {
      current = mergeEvents(current, events);
      if (!cancelled) setState(current);
    };
    async function connect() {
      try {
        let page: RunEvent[];
        do {
          page = await api.events(runId, current.cursor);
          if (cancelled) return;
          merge(page);
        } while (page.length === 200);
        if (current.events.some((event) => terminalEvents.has(event.type))) {
          setConnection("Complete");
          return;
        }
        source = new EventSource(
          `${apiUrl}/runs/${encodeURIComponent(runId)}/events/stream?after=${current.cursor}`,
        );
        source.onopen = () => {
          if (!cancelled) setConnection("Live");
        };
        source.onerror = () => {
          source?.close();
          if (!cancelled) {
            setConnection("Reconnecting");
            timer = setTimeout(connect, 1000);
          }
        };
        for (const name of eventNames)
          source.addEventListener(name, (message) => {
            if (cancelled) return;
            try {
              const event = JSON.parse(
                (message as MessageEvent).data,
              ) as RunEvent;
              if (event.run_id !== runId) throw new Error("foreign event");
              merge([event]);
              if (terminalEvents.has(event.type)) {
                source?.close();
                setConnection("Complete");
              }
            } catch {
              source?.close();
              setConnection("Reconnecting");
              timer = setTimeout(connect, 1000);
            }
          });
      } catch {
        if (!cancelled) {
          setConnection("Reconnecting");
          timer = setTimeout(connect, 1000);
        }
      }
    }
    void connect();
    return () => {
      cancelled = true;
      source?.close();
      if (timer) clearTimeout(timer);
    };
  }, [runId, lastSequence]);
  return { ...state, connection };
}
