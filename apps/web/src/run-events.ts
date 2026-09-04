import type { RunEvent } from "./api";

export interface EventState {
  events: RunEvent[];
  cursor: number;
}
export const emptyEvents: EventState = { events: [], cursor: 0 };
export const terminalEvents = new Set([
  "run.completed",
  "run.failed",
  "run.cancelled",
]);

export function mergeEvents(
  state: EventState,
  incoming: RunEvent[],
): EventState {
  const events = new Map(state.events.map((event) => [event.sequence, event]));
  for (const event of incoming) {
    if (
      Number.isInteger(event.sequence) &&
      event.sequence > 0 &&
      !events.has(event.sequence)
    )
      events.set(event.sequence, event);
  }
  let cursor = state.cursor;
  while (events.has(cursor + 1)) cursor++;
  return {
    events: [...events.values()].sort((a, b) => a.sequence - b.sequence),
    cursor,
  };
}

export function outputFrom(events: RunEvent[]): string {
  let output = "";
  let message = "";
  for (const event of events) {
    if (
      event.type === "agent.message.delta" &&
      typeof event.data.delta === "string"
    )
      message += event.data.delta;
    if (
      event.type === "agent.message.completed" &&
      typeof event.data.content === "string"
    ) {
      output += `${event.data.content}\n`;
      message = "";
    }
  }
  return output + message;
}
