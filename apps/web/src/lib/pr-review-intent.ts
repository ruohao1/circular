import type { components } from "../generated/api";
type Launch = components["schemas"]["PRReviewLaunch"];
type Store = Pick<Storage, "getItem" | "setItem" | "removeItem">;
const memory = new WeakMap<Store, Map<string, Launch>>();
const key = (source: string) => `circular:pr-review:${source}`;
const uuid = (value: unknown) =>
  typeof value === "string" &&
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    value,
  ) &&
  value !== "00000000-0000-0000-0000-000000000000";
function valid(value: unknown): value is Launch {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return (
    Object.keys(v).every((k) =>
      [
        "request_key",
        "reviewer_id",
        "expected_input_fingerprint",
        "mode",
        "previous_review_id",
      ].includes(k),
    ) &&
    uuid(v.request_key) &&
    typeof v.expected_input_fingerprint === "string" &&
    /^[0-9a-f]{64}$/.test(v.expected_input_fingerprint) &&
    (v.reviewer_id === undefined || uuid(v.reviewer_id)) &&
    ((v.mode === "normal" && v.previous_review_id === undefined) ||
      (v.mode === "again" && uuid(v.previous_review_id)))
  );
}
export function pendingReviewIntent(
  storage: Store,
  source: string,
): Launch | undefined {
  try {
    const raw = storage.getItem(key(source));
    if (raw && raw.length < 2048) {
      const value: unknown = JSON.parse(raw);
      if (valid(value)) return value;
    }
  } catch {
    /* Browsers may block session storage. */
  }
  return memory.get(storage)?.get(source);
}
export function reviewIntent(
  storage: Store,
  source: string,
  parameters: Omit<Launch, "request_key">,
): Launch {
  const existing = pendingReviewIntent(storage, source);
  if (existing) return existing;
  const intent = { ...parameters, request_key: crypto.randomUUID() };
  if (!valid(intent))
    throw new Error("Refresh the PR and reviewer before starting a review.");
  let entries = memory.get(storage);
  if (!entries) {
    entries = new Map();
    memory.set(storage, entries);
  }
  entries.set(source, intent);
  try {
    storage.setItem(key(source), JSON.stringify(intent));
  } catch {
    /* Keep this tab's stable retry identity in memory. */
  }
  return intent;
}
export function clearReviewIntent(storage: Store, source: string): void {
  memory.get(storage)?.delete(source);
  try {
    storage.removeItem(key(source));
  } catch {
    /* Memory fallback is already cleared. */
  }
}
const fallback: Store = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
};
export function reviewStorage(): Store {
  try {
    return window.sessionStorage;
  } catch {
    return fallback;
  }
}
