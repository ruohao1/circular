import { describe, expect, it } from "vitest";
import { clearReviewIntent, reviewIntent } from "./pr-review-intent";
const source = "10000000-0000-4000-8000-000000000001";
const parameters = {
  mode: "normal" as const,
  expected_input_fingerprint: "a".repeat(64),
};
function storage() {
  const data = new Map<string, string>();
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => {
      data.set(k, v);
    },
    removeItem: (k: string) => {
      data.delete(k);
    },
  };
}
describe("review intent", () => {
  it("reuses the complete intent after double clicks, parameter changes and reload", () => {
    const store = storage();
    const first = reviewIntent(store, source, parameters);
    expect(reviewIntent(store, source, parameters)).toEqual(first);
    expect(
      reviewIntent({ ...store }, source, {
        ...parameters,
        expected_input_fingerprint: "b".repeat(64),
      }),
    ).toEqual(first);
  });
  it("starts an explicit fresh attempt after a known result is cleared", () => {
    const store = storage();
    const first = reviewIntent(store, source, parameters);
    clearReviewIntent(store, source);
    const next = reviewIntent(store, source, {
      ...parameters,
      mode: "again",
      previous_review_id: source,
    });
    expect(next.request_key).not.toBe(first.request_key);
    expect(next.previous_review_id).toBe(source);
  });
  it("recovers corrupted or invalid storage without trusting injected fields", () => {
    for (const invalid of [
      "broken",
      "null",
      "{}",
      JSON.stringify({ ...parameters, request_key: source, automatic: true }),
    ]) {
      const store = storage();
      store.setItem(`circular:pr-review:${source}`, invalid);
      const next = reviewIntent(store, source, parameters);
      expect(next.request_key).toMatch(/^[0-9a-f-]{36}$/);
      expect(next).not.toHaveProperty("automatic");
    }
  });
  it("keeps retries stable if browser storage is unavailable", () => {
    const store = {
      getItem() {
        throw Error("blocked");
      },
      setItem() {
        throw Error("blocked");
      },
      removeItem() {
        throw Error("blocked");
      },
    };
    expect(reviewIntent(store, source, parameters)).toEqual(
      reviewIntent(store, source, parameters),
    );
  });
});
