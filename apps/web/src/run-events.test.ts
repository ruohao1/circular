import { describe, expect, it } from "vitest";
import type { RunEvent } from "./api";
import { emptyEvents, mergeEvents, outputFrom } from "./run-events";

function event(
  sequence: number,
  type = "agent.message.delta",
  data = { delta: String(sequence) },
): RunEvent {
  return {
    sequence,
    position: sequence,
    id: `event-${sequence}`,
    run_id: "run",
    type,
    data,
    raw: null,
    source: "fake",
    occurred_at: "2026-01-01T00:00:00Z",
    recorded_at: "2026-01-01T00:00:00Z",
  };
}
describe("replayable Run events", () => {
  it("merges replay and duplicate SSE delivery once", () => {
    const history = mergeEvents(emptyEvents, [event(1), event(2)]);
    const result = mergeEvents(history, [event(2), event(3), event(1)]);
    expect(result.events.map((item) => item.sequence)).toEqual([1, 2, 3]);
    expect(result.cursor).toBe(3);
    expect(outputFrom(result.events)).toBe("123");
  });
  it("keeps the reconnect cursor before a gap until missing events arrive", () => {
    const gap = mergeEvents(emptyEvents, [event(3), event(1)]);
    expect(gap.cursor).toBe(1);
    const recovered = mergeEvents(gap, [event(2), event(3), event(4)]);
    expect(recovered.cursor).toBe(4);
    expect(recovered.events.map((item) => item.sequence)).toEqual([1, 2, 3, 4]);
  });
  it("does not render a completed message twice after its deltas", () => {
    const complete: RunEvent = {
      ...event(3),
      type: "agent.message.completed",
      data: { content: "12" },
    };
    expect(outputFrom([event(1), event(2), complete])).toBe("12\n");
  });
});
