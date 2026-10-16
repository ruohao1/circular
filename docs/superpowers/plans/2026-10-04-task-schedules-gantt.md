# Task schedules and Gantt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Edit planned Task dates, inspect the same Tasks in a paginated Timeline, record dependencies and date warnings, then safely drag and resize schedules.

**Architecture:** Extend existing atomic Task planning writes and Project planning revisions. Add bounded Timeline/dependent reads, a shared date/dependency editor, and a pinned ReUI adapter. Calendar dates remain independent of Run execution. Keep one owner for drafts and mutations across List, Board and Timeline.

**Tech Stack:** Go, pgx/PostgreSQL, OpenAPI 3.1, MCP, React 19, TypeScript, Vite, TanStack Query/Router, Tailwind 4, existing Radix Nova primitives. Adopt pinned MIT ReUI Gantt source with only `date-fns@4.1.0` and `@date-fns/tz@1.5.0` as new runtime packages.

**Spec:** [Task schedules and Gantt design](../specs/2026-10-04-task-schedules-gantt-design.md). [Pinned source manifest](../specs/2026-10-04-task-schedules-gantt-sources.json).

**Status:** Task 1 implemented, verified and reviewed. Tasks 2–4 remain planned. The existing choice of **native execution** persists: root implements these tasks sequentially using executing-plans, then requests one fresh final review. Read-only research was delegated during planning.

**Release reconciliation, 2026-10-16:** This document records the original larger
plan. Timeline and gesture scheduling subsequently shipped in the Task Timeline
release and [Gantt interaction plan](2026-10-08-gantt-interactions.md). Task
dependencies remain unimplemented and are excluded from the GitHub release PR.
The proposed dependency migration `0018` below is not reserved: delegation now
uses that revision, so future dependency work must select the next unused revision.

## Global Constraints

- Preserve Task/Run separation, existing launch/retry and partial-launch recovery, provider callbacks, source-managed content locks, Repository identity, workflow/order, and historical snapshots. Schedules and dependencies never trigger or gate execution.
- Calendar dates are nullable, inclusive-finish, real `YYYY-MM-DD` in years 0001–9999. Partial schedules remain Unscheduled. No timestamps or locale-dependent date persistence.
- Every write locks Project before Task, checks Task revision and any supplied planning revision, and commits all edits together. New schedule/dependency fields require both revisions, even for no-ops. Never replay stale user intent automatically.
- Graph validation and warning counts use the full Project, irrespective of chart filters/pages. Limit incoming edges to 20, not graph traversal or outgoing relationships. Keep date conflicts advisory and cycles invalid.
- Timeline keeps bounded current pages, exact totals, consistent revisions and current Run summaries. Do not fetch all Tasks, silently cap totals, or accumulate unvirtualized rows.
- Preserve unrelated dirty workspace files. Original Git metadata may be read-only; use an isolated baseline containing current dirty source and transfer verified deltas only after detecting concurrent edits.
- Preserve MIT attribution and exact upstream provenance. Runtime adoption is conditional on Task 2's concrete gate, not a claim established by source inspection.

## Review Focus

1. **Civil dates survive DST, extreme years, and Today near UTC midnight.** Task 1 `TestTaskSchedulePatchPresenceAndDates` / `civil date round trips`; Task 2 `timeline uses local Today and actual visible ranges`; Task 4 `drag preserves dates across timezones`.
2. **Concurrent edits cannot overwrite schedules or create a hidden cycle.** Tasks 1/3 `TestTaskScheduleNoopAndRevisions`, `TestTaskScheduleAtomicPatch`, `TestTaskDependencyConcurrentOppositeEdges`; Task 4 `stale gesture is discarded without replay`.
3. **Filtered, off-window, or unloaded Tasks retain dependency and pagination context.** Tasks 2/3 `TestTaskTimelineCursorScopeAndRevision`, `TestTaskTimelineSnapshotDuringMutation`, `TestTaskTimelineHiddenDependencyContext`, `TestTaskDependentsPagination`; browser `bounded pages retain all Tasks and hidden dependency context`.
4. **Dirty detail, late requests and view/Project changes retain the correct Task identity.** Tasks 1/2 `shared detail keeps schedule draft across views` and `timeline drops responses from old scope`; Task 4 `cancellation performs no write`; existing launch recovery suites.
5. **Keyboard/touch scheduling remains accessible and cannot alter execution.** Tasks 1/3 `TestTaskScheduleContentLockIndependence`, `TestTaskScheduleRunIndependence`; Task 4 `keyboard date editor matches pointer scheduling`, `touch scroll and long-press scheduling`, and real `planned dates and dependencies do not gate Run launch`.

## Baseline and ownership

Read `CONTEXT.md`, the spec, `docs/development/ui-components.md`, the completed Task planning plan, and current code before starting. Record status, source hashes and the baseline commit in the isolated checkout. The prior implementation checkout `/tmp/circular-task-planning` is evidence, not automatically a current baseline: original files may have changed since transfer. Do not copy secrets, databases, build outputs or dependency directories into a source baseline. Keep test databases disposable.

| Boundary | Responsibility |
| --- | --- |
| `internal/migrate/0017.sql`, `0018.sql` | Dates, constraints/indexes, dependency relation and scoped cascades |
| `internal/postgres/task_planning.go` | Existing lock order, atomic edits, revision accounting |
| `internal/postgres/task_schedules.go`, `task_dependencies.go` | Civil-date validation, full-graph replacement validation |
| `internal/httpapi/task_planning.go`, `task_planning_read.go` | Strict PATCH fields and enriched existing reads |
| `internal/httpapi/task_timeline.go`, `task_dependents.go` | Snapshot reads, set-based counts, scoped cursors |
| `internal/controlmcp/task_planning.go`, `task_schedules.go` | Existing update parity and new read tools |
| `apps/web/src/lib/task-schedules.ts` | Civil dates, planning draft fields, patch diff and shift intent |
| `apps/web/src/lib/task-timeline.ts` | View range adapter, query options, bounded page reconciliation, gesture snapshot |
| `apps/web/src/components/tasks/use-task-planning.ts` | Sole shared draft and write owner, invalidation and guards |
| `apps/web/src/components/tasks/use-task-timeline.ts` | Read-only Timeline state, active pages and cursor histories |
| `apps/web/src/components/tasks/task-timeline.tsx` | Circular toolbar, rows, Unscheduled, announcements and ReUI adapter |
| `apps/web/src/components/tasks/task-schedule-fields.tsx`, `task-dependencies.tsx` | Shared date fields, draft predecessor picker, paginated dependent context |
| `apps/web/src/components/reui/gantt/` | Adapted, attributed upstream rendering/gesture primitive |

HTTP paths below are relative to `/api/v1`. Keep OpenAPI authoritative and regenerate TypeScript after each contract change. Only physical Task columns belong in schemas consumed by `projection()`.

### Task 1: Date editing from storage through shared detail

**Files:**

- Create `internal/migrate/0017.sql`, `internal/migrate/task_schedules_test.go`; modify `internal/migrate/migrate.go`, `migrate_test.go`.
- Create `internal/postgres/task_schedules.go`, `task_schedules_test.go`; modify `internal/postgres/task_planning.go`.
- Modify `internal/httpapi/task_planning.go`, `task_planning_read.go`, `schema.go` if needed for conditional contract validation; create `internal/httpapi/task_schedules_test.go`.
- Modify `contracts/openapi.json`, generated `apps/web/src/generated/api.ts`, `internal/controlmcp/task_planning.go`; create `internal/controlmcp/task_schedules_test.go`.
- Create `apps/web/src/lib/task-schedules.ts`, `task-schedules.test.ts`, `apps/web/src/components/tasks/task-schedule-fields.tsx`.
- Modify `apps/web/src/api.ts`, `components/tasks/use-task-planning.ts`, `task-detail.tsx`, `task-list.tsx`, `task-board.tsx`, and `tests/browser/tasks.spec.ts`.

**Interfaces and behavior:**

Extend `TaskPlanningPatch` without changing `UpdateTaskPlanning(ctx, tx, taskID, expectedRevision, patch)` or its result:

```go
ExpectedBoardRevision *int64
PlannedStartSet, PlannedFinishSet bool
PlannedStartDate, PlannedFinishDate *string
// Task 3 adds PredecessorsSet bool and PredecessorIDs []uuid.UUID.

func ValidateTaskSchedule(start, finish *string) error
```

`Set=false` preserves the saved value; `Set=true` with nil clears it. Validate patch syntax and proposed combined saved+patched dates in both HTTP and persistence. Require a valid planning revision for any date presence, then check it under the existing Project lock. A real combined edit updates Task revision/timestamp and Project revision once; all valid no-ops preserve them. Reuse planning errors and safe-integer bounds.

Migration 0017 adds null defaults and database checks for date bounds and `finish >= start` when complete. Add the two partial indexes specified in the design. Head becomes 0017, with 0016 retained in both allowed predecessor versions and migration order. Upgrade tests must cover older paths, not only 0016→0017.

Add nullable date properties to `TaskRead` and `TaskPlanningSummary`, and optional nullable fields plus optional `expected_board_revision` to the PATCH schema. OpenAPI `dependentRequired` records the revision condition; explicit Go validation enforces it. Existing old-field callers remain compatible. MCP `update_task` preserves raw presence/null and implements the same condition without defaults; keep existing create_task inputs/capability checks unchanged. An older API must produce an actionable unsupported-field/operation error, not discard scheduling fields.

```ts
type TaskPlanningDraft = TaskDraft & {
  planned_start_date: string; // empty form value becomes explicit null on change
  planned_finish_date: string;
  // Task 3 adds predecessor_ids: string[].
};
function parseCivilDate(value: string): { year: number; month: number; day: number } | null;
function addCivilDays(value: string, amount: number): string | null; // null outside domain
function shiftSchedule(start: string, finish: string, days: number):
  { planned_start_date: string; planned_finish_date: string } | null;
```

Keep form state separate from `launch.ts`'s creation draft. Extend existing changed-field patch construction: include only changed dates, null for cleared dates, and the detail snapshot's planning revision if a date changed. Retain text/metadata in the same atomic Save. Add a compact saved date label to List/Board, partial-date labels, and the shared date fields/Clear schedule controls. A validation or 409 failure retains the user's draft. Polling must not overwrite it. Do not loosen existing content locks to enable dates.

- [x] **Step 1: Write focused failing tests.** Add the following assertions using existing disposable database and browser fixture patterns:

  | Test | Assertions |
  | --- | --- |
  | `TestTaskScheduleMigrationPreservesHistory` | Upgrade populated 0016 and an earlier supported version; both dates null, Task/Project revisions, timestamps, content, ordering and Runs unchanged; repeat Up harmless; malformed direct SQL dates/ranges rejected |
  | `TestTaskSchedulePatchPresenceAndDates` | Omission/one-field update/null clear/one-day/partial range work; leap day and 0001/9999 accepted; year zero, timestamps, whitespace, infinity and reversed complete ranges rejected |
  | `TestTaskScheduleNoopAndRevisions` | One effective combined edit bumps both revisions once; no-op bumps neither; stale Task or planning revision returns conflict even for no-op; overflow rolls back |
  | `TestTaskScheduleContentLockIndependence` | Dates editable with source-managed content or each active Run state; supplying a locked text/Repository change rejects the whole combined patch |
  | `TestTaskScheduleStrictValidationWithoutDatabase` | Missing/null/fractional/unsafe revisions, duplicate/unknown keys, invalid UTF-8, trailing JSON and malformed dates fail before a database connection |
  | `TestTaskScheduleAtomicPatch` | Successful text+dates round trip through PATCH/detail/list; validation or stale planning revision rolls back every field |
  | `TestTaskScheduleMCPPresenceAndConditionalRevision` | Omitted dates absent on HTTP body, explicit null retained, revision condition enforced; old-only updates and create_task unchanged |
  | `civil date round trips` / `schedule patch keeps omission distinct from clearing` | Gregorian boundaries, DST dates, year 0099, shift limits, no accidental fields and no local-millisecond arithmetic |
  | `shared detail saves and clears partial schedules` | Save/reload, no-op disabled, invalid range inline, Clear requires Save, source locks retained, dirty/409 draft survives explicit reload decision |

- [x] **Step 2: Observe red.** Run `go test ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/controlmcp -run 'TestTaskSchedule' -count=1 -v`, `corepack pnpm --filter @circular/web exec vitest run src/lib/task-schedules.test.ts`, and the new focused Tasks browser cases. Confirm missing behavior is the cause; database skips are not verification.
- [x] **Step 3: Implement the full date slice.** Apply the migration, strict/persistence validation, contract/MCP parity and shared form changes above. Leave bars and packages for Task 2. Generate types with `corepack pnpm contracts:generate`.
- [x] **Step 4: Verify.** Run the same focused tests, `go test ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/controlmcp ./internal/integrations -count=1`, `corepack pnpm contracts:check`, `corepack pnpm typecheck`, `corepack pnpm test`, and `corepack pnpm exec playwright test --config playwright.tasks.config.ts`. Confirm actual database tests ran.
- [x] **Step 5: Commit the isolated slice.** `feat(tasks): add planned calendar dates`. Record verification evidence; do not commit unrelated baseline files as feature changes.

### Task 2: Bounded read-only Timeline and renderer gate

**Files:**

- Create `internal/httpapi/task_timeline.go`, `task_timeline_test.go`; modify route registration in `internal/httpapi/api.go`, `contracts/openapi.json`, and generated TypeScript.
- Create `internal/controlmcp/task_schedules.go`; modify tool registration in `internal/controlmcp/server.go` and count assertions in `internal/controlmcp/server_test.go`, `http_test.go`, `tests/browser/mcp.spec.ts`. Extend `task_schedules_test.go` and update tool counts in `docs/development/control-mcp.md`.
- Create `apps/web/src/lib/task-timeline.ts`, `task-timeline.test.ts`, `components/tasks/use-task-timeline.ts`, `task-timeline.tsx`.
- Modify `lib/task-planning.ts`, `task-planning.test.ts`, `components/tasks/use-task-planning.ts`, `task-detail.tsx`, `pages/tasks.tsx`, `main.tsx`, and `api.ts`.
- Create `apps/web/src/components/reui/gantt/{gantt,gantt-types,gantt-lib,gantt-i18n,gantt-recurrence,gantt-dnd,gantt-bar,gantt-view}.tsx` and adjacent `LICENSE.md`.
- Create missing `apps/web/src/components/ui/{checkbox,context-menu,scroll-area,tooltip}.tsx` from existing Radix patterns; modify `apps/web/package.json`, `pnpm-lock.yaml`, `docs/development/ui-components.md`.
- Create `tests/browser/timeline.spec.ts`, `playwright.timeline.config.ts` using the existing mocked Tasks config conventions.

**Read and state contracts:**

Implement the Timeline request/response, 366-day viewport bound, filtered totals, order, cursors and snapshot rules from the spec. Initially omit dependency fields; Task 3 adds them to this contract. Reuse established Task summary/Run SQL projections, adding bounded set-based reads rather than new per-Task fetches. MCP `list_task_timeline` defaults to 20, maximum 100, and explicit from/to; full/read-only counts become 44/23.

Extend `TaskPlanningSearch` to `view: 'list' | 'board' | 'timeline'`, `scale: 'day' | 'week' | 'month'`, `date?: string`. Keep existing search/filter/taskId behavior and strip default scale appropriately. Normalize date with the strict civil-date helper. Entering Timeline materializes local Today once through router replace, without creating a navigation loop.

```ts
type TimelineSection = "scheduled" | "unscheduled";
type TimelineScale = "day" | "week" | "month";
type ActivePlanningCollection = {
  isPending: boolean; isFetching: boolean; isError: boolean;
  error: Error | null; refetch: () => Promise<unknown>;
};
type TaskTimelineState = ActivePlanningCollection & {
  boardRevision: number | null;
  groups: TaskTimelinePage["groups"];
  outsideWindowTotal: number;
  nextPage: (section: TimelineSection) => void;
  previousPage: (section: TimelineSection) => void;
  canPrevious: (section: TimelineSection) => boolean;
  resetPages: () => void;
};
function timelineWindow(anchor: string, scale: TimelineScale):
  { from: string; to: string }; // actual visible range, inclusive and clamped
function useTaskTimeline(input: {
  projectId: string; search: TaskPlanningSearch; enabled: boolean; paused: boolean;
}): TaskTimelineState;
```

Use generated DTO types for groups; Task 3 adds `dependencies: TaskTimelinePage['dependencies']` to the state. Query keys include captured Project, normalized filters, window, section, limit and cursor. Keep one active page and cursor stack per section. Continuation replaces its section only at a matching revision; otherwise reset both. Cancel/ignore old-scope responses. Expose `activeCollection` and `timeline` through the existing planning controller so toolbar and detail guards follow the visible view.

Poll active pages and detail every 15 seconds when visible and idle, including unchanged-revision Run summaries. Default queries return both first pages in one snapshot. Subsequent section reads must reconcile revisions before showing both sections. A planning revision change returns to first pages with an explanatory notice. View switches retain detail and drafts; Project/filter/window changes reset cursor histories. Existing List/Board polling remains intact.

**Pinned adoption steps:**

1. Fetch each pinned Radix Nova `gantt*.json` entry named in the source manifest, verify its registry hash, extract its matching file content, and verify the extracted-content hash before adaptations. Preserve the pinned MIT license. Do not run an unpinned registry installer.
2. Copy exactly the eight required modules. Rewrite aliases and icon placeholders to existing utils/Lucide; add four missing local wrappers using installed `radix-ui`. Keep recurrence internals imported but expose no recurrence features. Omit upstream navigation/demos and Calendar/day-picker dependencies.
3. Install only the two pinned date packages in the isolated checkout. Add source commit, hash-manifest link, license and local-patch notes to `docs/development/ui-components.md`.
4. Map Circular scales to upstream month/quarter/year. Use `getGanttDateRange(scale, utcAnchor, {timeZone: 'UTC', weekStartsOn: 1})` and its `visibleRange`, converting exclusive end to inclusive to. Use `stepGanttDate` for period navigation. Do not duplicate approximate quarter math. Disable out-of-domain navigation/cells at year bounds.
5. Add `todayDate?: Date` to vendored `GanttViewConfig`/recognized view keys; use it in both Today key and start calculations, including memo dependencies. Supply a stable UTC-encoded local date that refreshes on local midnight/focus. Hide the now line; custom Today sets the controlled anchor. Keep `initialCenter='anchor'`.
6. Configure controlled events/resources/date/scale; `timeZone='UTC'`, `weekStartsOn=1`, `scheduleMode='single'`, `infiniteScroll=false`, and all drag/resize/select/create/reorder interactions disabled. Convert inclusive dates only in the adapter. Use one Task per resource and the actual full dates for clipped bars. The native bar/title buttons open shared detail.
7. Render the toolbar, exact period label, counts and independent Previous/Next section controls, Unscheduled Schedule actions, partial dates and clear empty/error/loading states. Update detail's two-view navigation affordance to handle all three views and add Show on Timeline.

- [x] **Step 1: Write tests that define the read and rendering gate.**

  | Test | Assertions |
  | --- | --- |
  | `TestTaskTimelineWindowAndUnscheduled` | Inclusive overlap boundaries, one-day and multi-year Tasks, both partial-date forms, exact outside/section totals, cancelled default exclusion, literal Unicode search, complete stable ordering |
  | `TestTaskTimelineCursorScopeAndRevision` | Traverse >100 rows per section without gaps/duplicates; wrong Project/filter/window/section/limit and malformed/oversized cursor 422; changed board revision 409; valid 200-rune Unicode query fits |
  | `TestTaskTimelineSnapshotDuringMutation` | Synchronize reads and concurrent schedule/create edits; each response's rows/totals/revision belong to one snapshot; no mixed membership and no write locks on reads |
  | `TestTaskTimelineMCPPagination` | Explicit from/to, default/max limits, sections/cursors preserved, response-size errors actionable rather than truncated, tool visible in read-only mode |
  | `timeline civil adapter round trips` | Inclusive↔exclusive dates round trip, leap/DST/0099/extreme boundaries, one-day width never becomes duration, long/clipped range retains true endpoints |
  | `timeline uses local Today and actual visible ranges` | Day/Week/Month map correctly, Monday quarter boundary weeks match query, local Today/highlight agree near UTC midnight, explicit anchor survives midnight/reload/back |
  | `bounded pages retain all Tasks` | 1,000-Task fixture, at most page-limit scheduled rows mounted, no full-project/per-row fetches, totals correct, each section pageable and independently retained at same revision |
  | `shared detail keeps schedule draft across views` | List→Board→Timeline retains draft/selection/filters; selected off-page Task remains open; dirty Project/navigation guards and Show on Timeline work |
  | `timeline drops responses from old scope` | Delayed old Project/filter/window/section response cannot replace active rows, notices or drafts; revision changes reset both cursor stacks; unchanged-revision polling refreshes Run badges |
  | `read-only timeline works with keyboard and narrow screens` | Enter/Space opens detail; pointer/touch cannot change dates; long titles, clipping, light/dark themes, horizontal scrolling and focus remain usable |

- [x] **Step 2: Observe red.** Run `go test ./internal/httpapi ./internal/controlmcp -run 'TaskTimeline' -count=1 -v`, `corepack pnpm --filter @circular/web exec vitest run src/lib/task-timeline.test.ts src/lib/task-planning.test.ts`, and `corepack pnpm exec playwright test --config playwright.timeline.config.ts`. Missing route/renderer behavior must explain failures.
- [x] **Step 3: Implement reads, adoption and read-only UI.** Complete the contracts and adoption steps, regenerate types. Exercise browser contexts `America/Los_Angeles`, `Europe/Paris` and `Pacific/Kiritimati`, DST boundaries, year bounds and clock-controlled UTC-midnight cases. If the renderer fundamentally fails these requirements, record the exact limitation and revise the choice before enabling mutation gestures; do not silently substitute a different library.
- [x] **Step 4: Verify the gate.** Run focused tests, contracts check, typecheck, all frontend unit tests, production build, mocked Timeline and Tasks suites, and `go test ./internal/httpapi ./internal/controlmcp -count=1`. Inspect actual bars, labels, focus and themes in browser evidence. Record bounded DOM/request counts, not unmeasured performance claims.
- [x] **Step 5: Commit.** `feat(tasks): add paginated task timeline`. Keep bars read-only at this checkpoint.

### Task 3: Atomic dependencies and complete warning context

**Files:**

- Create `internal/migrate/0018.sql`, `internal/migrate/task_dependencies_test.go`; modify migrator lists/head and related tests.
- Create `internal/postgres/task_dependencies.go`, `task_dependencies_test.go`; modify `task_planning.go` and schedule tests.
- Modify `internal/httpapi/task_planning.go`, `task_planning_read.go`, `task_timeline.go`; create `task_dependents.go`, `task_dependencies_test.go`.
- Modify `contracts/openapi.json`, generated TypeScript, `internal/controlmcp/task_planning.go`, `task_schedules.go`, `task_schedules_test.go`, `server.go`, `server_test.go`, `http_test.go`, `tests/browser/mcp.spec.ts`, `docs/development/control-mcp.md` and HTTP route registration in `internal/httpapi/api.go`.
- Create `apps/web/src/components/tasks/task-dependencies.tsx`; modify `task-detail.tsx`, `task-timeline.tsx`, `use-task-planning.ts`, `use-task-timeline.ts`, `lib/task-schedules.ts`, its tests, `api.ts`, and mocked Tasks/Timeline browser cases.

**Interfaces and transaction:**

Add `PredecessorsSet bool` and `PredecessorIDs []uuid.UUID` to `TaskPlanningPatch`. Presence requires the planning revision; null is invalid, omission preserves, and an empty list clears. Canonically sort UUIDs for set equality and dirty checking. Do not bump revisions merely for a different input order. Explicit duplicates remain invalid.

Migration 0018 adds the Tasks composite unique key, dependency table/check/scoped cascading foreign keys, and reverse index from the spec. Head becomes 0018, retaining 0016 and 0017 in accepted versions/order. Do not add a broad Task update trigger that double-counts existing revisions.

```go
func validateTaskPredecessors(ctx context.Context, tx pgx.Tx,
    projectID, taskID uuid.UUID, predecessorIDs []uuid.UUID) error
// Requires the Project lock already held by UpdateTaskPlanning.
```

Validate every proposed predecessor in the same Project, maximum 20, unique/non-nil/non-self. Traverse outgoing paths from T: adding P→T is invalid if T reaches P, excluding T's replaced incoming set. The traversal uses the complete graph and terminating set semantics. Validate all new fields before writing; replace incoming edges and update content/dates in the same transaction. Only the edited Task and Project revisions change, once. A predecessor's date edit recomputes outgoing warning context on read, without writing successor Tasks.

Add generated `TaskDependencyState`, `TaskDependencySummary`, `TaskDependencyBrief`, and `TaskDependentsPage` schemas. Enrich List/Board items, Timeline items/edge array, and detail with computed fields outside physical-column projections. Compute all incident conflict/unknown counts without multiplying Run counts through joins. Incoming detail returns all <=20 briefs; dependent GET is bounded, ordered and snapshot/cursor scoped as in the spec. Add `list_task_dependents` MCP, bringing full/read-only counts to 45/24. Retain output-size guards. Timeline edge reconciliation replaces only the changed section's successor-owned edges, keeping the other section at the same revision. Dependent pages/cursor history reset on source Task, Project or detail planning-revision change without discarding a dirty draft.

Add `predecessor_ids: string[]` to `TaskPlanningDraft`. Populate it from detail briefs and diff it as a set in the shared Save. The picker uses Project-scoped existing planning search, independent query/status controls and per-group limit 10; reuse cursors for more results, with no fetch-all. Exclude self and enforce 20 selections visibly. Keep selected items visible while searching. Edits persist only via shared Save changes; clear/remove is a draft action.

Render saved relation states with counterpart names/dates in Depends on and paginated Needed by. Show aggregate warnings for hidden dependencies. Wire visible predecessor IDs to ReUI dependencies only if both scheduled rows are present; explain that more relationships are available in detail. Render Unscheduled incoming edges as textual context. Relationship warnings after an unsaved date/edge change must explicitly reflect saved data.

- [ ] **Step 1: Write failing tests.**

  | Test | Assertions |
  | --- | --- |
  | `TestTaskDependencyScopeAndProjectCascade` | Composite FKs reject foreign-Project edges; self links rejected; deleting Task or whole Project cleans edges without FK deadlocks; dates/history preserved across upgrade |
  | `TestTaskDependencyReplacement` | Omission/[]/replacement/canonical reordering semantics, null/duplicates/nil/max21 invalid; same set no-op; combined content/dates/edges rollback and exactly-once revision/timestamp changes |
  | `TestTaskDependencyCycleAndScope` | Self, two-node and >20-hop cycles rejected, including hidden/cancelled/off-window nodes; valid diamonds accepted; replacement can remove old incoming edges; no bounded traversal shortcut |
  | `TestTaskDependencyConcurrentOppositeEdges` | Barrier-controlled opposite writes with same planning revision produce one success and one 409; reloaded retry returns 422; no deadlock/cycle or partial content write |
  | `TestTaskScheduleHiddenSuccessorConflicts` | Editing predecessor dates changes hidden successors' relation states/counts, including partial endpoints, without changing successor revisions or Runs |
  | `TestTaskTimelineHiddenDependencyContext` | All incoming edges for returned successors present up to 20, full incoming/outgoing conflict/unknown counts include hidden/cancelled Tasks; no Run-count multiplication or extra hidden titles |
  | `TestTaskDependentsPagination` | >100 successors all reachable with exact total and relation states; scoped cursor, stale 409, no duplicates; detail incoming list remains complete |
  | `TestTaskScheduleMCPReadOnly` | Both new reads registered; counts45/24; write absent in read-only; update null/[]/revision parity and response-size guard preserved |
  | `TestTaskScheduleRunIndependence` | Editing dates/dependencies causes no Run or provider writes; explicit Run launch remains allowed with future/conflicting dates and incomplete predecessors |
  | `dependency picker and saved warnings retain hidden context` | Independent search/cancelled filter, >10 results paged, selected set retained, max 20, remove/cancel/save, all relation states, outgoing Next/Previous and stale-page reset, section pagination retains the other section's edges, off-page arrows omitted but context explained |

- [ ] **Step 2: Observe red.** Run `go test ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/controlmcp -run 'TaskDependency|TaskDependents|TaskScheduleHidden|TaskScheduleRun|TaskScheduleMCP|TaskTimelineHidden' -count=1 -v`, focused frontend unit cases and dependency browser cases. Concurrency tests must exercise two real transactions.
- [ ] **Step 3: Implement the whole dependency slice.** Wire validation, storage, computed reads, contracts/MCP and shared form/Timeline context. Regenerate types. Keep date warnings advisory; do not block Run launch or auto-shift successors.
- [ ] **Step 4: Verify.** Repeat focused tests, `go test -race ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/controlmcp ./internal/integrations -count=1`, contracts/typecheck/frontend tests, and mocked Tasks/Timeline browser suites. Review SQL bounds and full-graph scope explicitly.
- [ ] **Step 5: Commit.** `feat(tasks): add dependencies and schedule warnings`.

### Task 4: Controlled gestures, accessible alternatives and full acceptance

**Files:**

- Modify `apps/web/src/lib/task-timeline.ts`, `task-timeline.test.ts`, `components/tasks/task-timeline.tsx`, `use-task-planning.ts`, `use-task-timeline.ts`, `task-schedule-fields.tsx`, `pages/tasks.tsx` and ReUI adapter files only where demonstrated necessary.
- Extend `tests/browser/timeline.spec.ts`; create `tests/browser/task-schedules.spec.ts` using the real default E2E stack. Extend existing fixtures only when necessary for deterministic data.
- Create `docs/user-guide/task-planning.md`; register it in `docs/user-guide/meta.json` and link it from `quickstart.md`. Update `docs/development/ui-components.md`, `docs/development/control-mcp.md`, and both roadmap/focused design implementation status after verification.

**Gesture contract:**

```ts
type TaskScheduleSnapshot = {
  projectId: string; taskId: string;
  revision: number; boardRevision: number;
  plannedStartDate: string; plannedFinishDate: string;
  scope: string; // normalized Project/filter/window/page identity
};
type TaskScheduleIntent = TaskScheduleSnapshot & {
  nextStartDate: string; nextFinishDate: string;
};
function scheduleIntent(snapshot: TaskScheduleSnapshot, current: TaskScheduleSnapshot,
  nextStartDate: string, nextFinishDate: string): TaskScheduleIntent | null;
```

Implement the pure `scheduleIntent` guard in `lib/task-timeline.ts`; mismatches, invalid ranges and no-ops return null. Expose `reschedule(intent: TaskScheduleIntent): void` from `useTaskPlanning`; it rechecks current scope/permissions/pending state and starts the shared mutation. The chart never writes directly.

Carry the immutable snapshot in `GanttEvent.data`. The synchronous update callback validates the proposal against that snapshot and the latest canonical controller scope, starts exactly one existing planning mutation with only the two dates and expected revisions, and immediately returns false. It must not be `async`. No-op intent sends no write. A body move preserves full duration; resize changes only its exposed endpoint. Do not use visual min-width or clipped endpoints as authoritative dates.

Observe ReUI drag state through its selector/subscription to pause reads and activate the existing Project selection lock; release it on every exit path. Include currently pending fetches in cancellation/response guards. Use `cancelActiveGanttGestures()` on revision/canonical change, stale data, permission loss, scope/window/scale/page change, or unmount. Dirty detail, global pending write, loading/stale collection or invalid source snapshot disables gestures. If a refetch is already in flight, either prevent gesture start until settled or cancel the gesture when its result arrives; never freeze an unconfirmed version silently.

Use the existing global write key and shared controller, not a second mutation owner in the chart. Keep authoritative bars while saving and refetch canonical data after success. On 409, clear pages and request fresh intent with no replay; retain unrelated dirty drafts. A failure leaves original dates and focus usable. Update only captured Project caches and never install a late response into a different view/Task draft.

Support Escape, blur, pointercancel, view/geometry changes and outside-chart release with zero writes. Verify upstream cancellation behavior and add a small boundary guard if outside release still commits. Restore focus using stable Task ID after bar remount; fall back to Task/detail or toolbar controls with a useful announcement when it moves off-page/window. Exposed resize handles only, one-day minimum, one calendar-day snapping at every scale.

Add Earlier by one day / Later by one day buttons to the shared form; they change complete draft dates without saving, preserve duration, obey date bounds and use the same Save/validation path. Native labelled date fields remain available for any move/resize. Keep ordinary touch scroll separate from long-press scheduling, and preserve keyboard bar opening. Announce gesture, dates, save/cancel/error results once.

- [ ] **Step 1: Write failing acceptance tests.**

  | Test | Assertions |
  | --- | --- |
  | `schedule intent rejects stale or changed scope` | Snapshot Task/Project/revisions/original dates/filter/window/page must match; no-op and invalid/out-of-domain dates produce no mutation; canonical conversion preserves full duration |
  | `drag preserves dates across timezones` | Move and both resize edges at all three scales, one-day/leap/DST/year transitions, UTC/local-Today distinction, partial clipping, same-day minimum; exactly one PATCH on accepted drop |
  | `stale gesture is discarded without replay` | Another console/import changes planning revision before/during drag; delayed fetch, 409 and server failure retain canonical dates, reset pages as needed, show actionable message and no automatic retry |
  | `cancellation performs no write` | Escape, blur, pointercancel, release outside, unmount, filter/scale/page/Project changes and geometry changes cancel; dirty draft/pending write blocks start; all locks released |
  | `keyboard date editor matches pointer scheduling` | Enter/Space open detail; Earlier/Later changes draft only; Save yields equivalent dates; fields/errors/relationship labels accessible; focus recovered after canonical remount or off-window move |
  | `touch scroll and long-press scheduling` | Touch scroll causes no PATCH; intentional long-press move and visible handle resize work; cancelled touch produces no write; narrow detail remains usable |
  | real `schedule changes survive reload and share one Task across views` | Save dates, dependency, drag and resize against database; reload List/Board/Timeline/detail agree, exact revision increments and same Task identity |
  | real `planned dates and dependencies do not gate Run launch` | Future/conflicting/unknown predecessor dates allow explicit fake-worker Run; schedule/dependency changes leave workflow and historical execution inputs intact |
  | real `second console cannot overwrite a schedule` | Two contexts share a Project; stale save/move returns409, preserves draft and requires explicit reload; cancellation/invalid edge does not partially save |

- [ ] **Step 2: Observe red.** Run focused intent unit tests, mocked gesture cases through `playwright.timeline.config.ts`, and the new real `task-schedules.spec.ts` using disposable PostgreSQL. Verify the failures are missing behavior, not unavailable services.
- [ ] **Step 3: Implement gestures and accessible equivalents.** Keep the shared write owner and canonical server reconciliation. Capture desktop/touch/light/dark/narrow evidence. Document local upstream adaptations and any measured limitations without weakening date or concurrency guarantees.
- [ ] **Step 4: Run final verification and review.** With `TEST_DATABASE_URL` set to the disposable test database, run:

  ```sh
  corepack pnpm contracts:check
  corepack pnpm typecheck
  corepack pnpm test
  corepack pnpm build
  go test -race ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/integrations ./internal/controlmcp
  go vet ./...
  go build ./...
  corepack pnpm exec playwright test --config playwright.timeline.config.ts
  corepack pnpm exec playwright test --config playwright.tasks.config.ts
  corepack pnpm exec playwright test --config playwright.console.config.ts
  corepack pnpm exec playwright test tests/browser/task-schedules.spec.ts tests/browser/task-planning.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts tests/browser/integrations.spec.ts tests/browser/linear-agent.spec.ts tests/browser/linear-updates.spec.ts tests/browser/pr-review-execution.spec.ts tests/browser/mcp.spec.ts
  corepack pnpm exec playwright test --config playwright.docs.config.ts
  corepack pnpm exec playwright test --config playwright.github-delivery.config.ts
  corepack pnpm exec playwright test --config playwright.pr-reviews.config.ts
  ```

  Record actual commands, executed/skipped counts, screenshots/traces and limitations. Use one fresh final reviewer against this spec and Review Focus. Resolve material findings and rerun the affected checks; do not equate a skip or unavailable database with passing coverage. Update documentation status only after the corresponding behavior is verified.
- [ ] **Step 5: Commit and hand off.** `feat(tasks): enable safe timeline scheduling`. Transfer only verified feature deltas to the original workspace if implementation used an isolated copy; compare pre-transfer hashes and preserve concurrent edits. Verify transferred source hashes and run appropriate contract/type/build checks there. Report the implementation, test evidence, and any residual limitation. Do not deploy or discard other work as part of this plan.

## Planning self-review

- Four sequential slices provide usable checkpoints; date storage/editing precede the renderer, dependencies precede gesture acceptance.
- Every new wire field has explicit omission/null semantics, validation, revision ownership and computed-vs-column placement.
- Read bounds do not limit graph correctness or hide reachable Tasks. Outgoing dependencies have their own pagination.
- The selected renderer's actual APIs, scale mapping, end convention, Today adaptation and nonvirtualized rows are accounted for. Runtime compatibility remains unclaimed.
- Migration order retains prior heads; MCP schema/counts and generated contracts advance with their slice.
- Task 1 is complete: dates, atomic HTTP/MCP updates, shared editor and List/Board labels. Evidence: 41 frontend units; 27 Tasks + 19 console + 3 real API/fake-worker browser cases; affected Go suites and focused race checks; contracts/typecheck/build/vet. Fresh review found no actionable findings. Tasks 2–4 have not been executed.
