# Task schedules and Gantt design

Status: Planned-date editing, List/Kanban/Gantt views, and Gantt drag/resize with one-step Undo are implemented. Dependencies remain planned; the user approved interactions before dependencies on October 8. Builds on the completed [Task planning and Kanban design](2026-10-03-task-planning-kanban-design.md). The bounded interaction increment is described in the [October 8 implementation plan](../plans/2026-10-08-gantt-interactions.md).

## Outcome and scope

A person can assign planned calendar dates to a Task, see the same Tasks in List, Board, or Timeline, record dependencies, and identify inconsistent dates. Timeline is the Task Gantt requested in the console roadmap. An actual Run execution timeline remains a separate future feature.

Ship four useful increments: date editing; a read-only Timeline; dependencies and warnings; then bar dragging and resizing. Scheduling is manual. Saving dates, moving a bar, or adding a dependency never starts, cancels, blocks, approves, or reschedules a Run. Existing Run launch and retry behavior remains intact.

Automatic scheduling, dependency execution gates, critical paths, capacity planning, milestones, recurrence, baselines, provider date synchronization, and invented progress percentages are outside this increment.

## Calendar dates

Add nullable PostgreSQL `DATE` columns `planned_start_date` and `planned_finish_date` to Tasks. Their wire form is exactly `YYYY-MM-DD`, a real Gregorian date from `0001-01-01` through `9999-12-31`, or null. Reject timestamps, whitespace, year zero, infinity, invalid leap days, and finish before start when both dates are present.

Dates are independently optional. A Task with either date missing belongs to **Unscheduled**; preserve its existing partial date when editing the other field. Equal dates mean a one-day Task. **Clear schedule** changes both draft fields to null and persists only when the person chooses Save changes. Existing Tasks start with both dates null; creation and imports retain that default. Task creation inputs stay unchanged in this increment.

Finish is inclusive throughout storage, API, forms, labels, and MCP. For example, October 4–6 occupies three calendar days. Only the renderer adapter converts finish to the following day's exclusive boundary. Civil-date helpers use calendar components or day ordinals, never local elapsed 24-hour arithmetic. UTC-encoded `Date` objects exist only at the renderer boundary, with `timeZone="UTC"`. Avoid JavaScript's year 0–99 constructor remapping. Internal boundary sentinels, such as exclusive `10000-01-01`, must never become persisted values.

Today is the browser's local calendar day. The Today control and header highlight use that same day encoded at UTC midnight for chart geometry. Midnight may update the highlight, but must not change the selected URL anchor. Windows at the supported date limits are clamped for API reads; out-of-domain chart cells are inert, and navigation cannot select an out-of-domain anchor.

Scheduling is editable for source-managed Tasks and Tasks with active Runs, like priority. Existing content and Repository locks still apply to their own fields. Scheduling is not execution input and does not rewrite historical Run snapshots.

## Dependencies and warnings

An edge `P → T` means Task T is planned to start after predecessor P finishes. Each Task may have at most **20 direct predecessors**; there is no artificial cap on its number of successors or on graph depth. Predecessors are an unordered set. All statuses are allowed, including completed and cancelled Tasks. Status changes retain edges and their warnings.

Reject duplicate IDs, the nil UUID, self-links, missing or cross-Project Tasks, and directed cycles. Cycle validation uses the full Project graph, including Tasks hidden by filters, dates, status, and pagination. Replacing a Task's incoming edges is atomic with its other edits. Its old incoming edges are excluded when validating the replacement.

For each edge, compute the relation from saved dates:

| State | Rule | Presentation |
| --- | --- | --- |
| `satisfied` | Both required endpoints exist and `T.start > P.finish` | Dates respect the dependency |
| `conflict` | Both required endpoints exist and `T.start <= P.finish` | Starts before the predecessor has finished; show both dates |
| `unknown_dates` | P's finish or T's start is absent | Add the missing date to check this dependency |

These checks need only the two relevant endpoints, even if either Task is otherwise Unscheduled. A date conflict is advisory and may be saved. Cycles and invalid date ranges are structural errors and return 422. Nothing moves automatically.

Show incoming **Depends on** and outgoing **Needed by** relationships in Task details, with counterpart titles, statuses, dates, and relation states. Incoming relationships fit the 20-item bound. Outgoing relationships use a separately paginated read, so a heavily depended-on Task never silently loses context. All counts include relationships to hidden, off-window, unloaded, and cancelled Tasks. While the draft has unsaved dates or edges, label dependency results as based on saved values; refresh after Save rather than claiming a complete preview of hidden effects.

## Storage, concurrency, and atomic writes

Use the existing Project `task_board_revision` as the planning snapshot token across all three views. Do not introduce a second schedule revision. An effective date or incoming-dependency change increments the edited Task's `revision` and `updated_at`, and the Project planning revision, exactly once per atomic edit. Derived warnings and outgoing context do not bump other Tasks. A no-op changes nothing, but still checks supplied revisions. Preserve the existing JavaScript-safe revision bound and overflow behavior.

Add schedules in migration **0017** and dependencies in **0018**. When updating migration Head and the accepted-version/sequence lists, retain 0016 explicitly, then retain 0017 explicitly. Test upgrades from the prior heads and repeat Up. Do not alter historical content, status, references, timestamps, ordering, or Runs during backfill.

Dependency storage is `task_dependencies(project_id, predecessor_task_id, successor_task_id)`, with a composite primary key, self-link check, and two scoped foreign keys into a new `UNIQUE (project_id, id)` on Tasks, both `ON DELETE CASCADE`. Index the successor direction as well as the primary-key predecessor direction. Test whole-Project deletion with dependencies. Schedule reads use a partial `(project_id, planned_start_date, id)` index for complete ranges and a partial `(project_id, created_at DESC, id DESC)` index for incomplete ranges.

All planning mutations continue to lock Project `FOR NO KEY UPDATE` before Task `FOR UPDATE`, in default READ COMMITTED transactions. Under that Project lock, validate revisions, proposed dates and the full replacement edge set before committing. A recursive cycle check must terminate on pre-existing invalid graphs (`UNION`, not unrestricted `UNION ALL`) and must not limit traversal to 20 hops. Concurrent opposite-edge edits serialize: the second request with an old planning revision returns 409; after reload, a retry creating a cycle returns 422. Preserve the shared Task lock used by Run launch.

Extend the existing `PATCH /api/v1/tasks/{task_id}` instead of introducing another write path:

```json
{
  "expected_revision": 8,
  "expected_board_revision": 31,
  "planned_start_date": "2026-10-04",
  "planned_finish_date": "2026-10-06",
  "predecessor_ids": ["61dfb40f-5cb9-484b-9c4f-7d3d18b91f64"]
}
```

Existing content and metadata fields remain valid in the same body. `expected_revision` remains required. `expected_board_revision` is required when **any** schedule or predecessor field is present, including null, an empty array, or an unchanged value. It is optional for old-only edits, but checked whenever supplied. Omitted fields preserve values; null clears a date; `[]` clears predecessors; null is invalid for predecessors. Reject the entire edit if any supplied field is invalid or locked. Keep strict JSON decoding, unknown/duplicate-field rejection, UTF-8 checks, and the existing 128 KiB request limit. Reuse 200 mutation results, 409 conflicts, and 422 validation responses.

Dates are physical columns and can be added to `TaskRead` and `TaskPlanningSummary`. Dependency projections are computed DTO fields, outside those schemas: the current `projection()` helper turns every schema property into a database column.

## Timeline read contract

Add `GET /api/v1/projects/{project_id}/task-timeline` with required inclusive `from` and `to` dates. Reject a reversed window or more than 366 calendar days. This viewport bound does not limit a Task's stored duration.

Reuse existing normalized `q` (literal case-insensitive title search, at most 200 runes), status, priority, Repository, and preferred Agent filters. With no status filter, include the five ordinary workflow stages; cancelled Tasks require the existing explicit filter. Apply identical filters to all totals and rows.

Default `limit=50`, maximum 100 **per section**. Without `section`, return the first page of both sections. `section=scheduled|unscheduled` returns only that section; a cursor requires section. The response has this shape:

```ts
type TaskDependencyState = "satisfied" | "conflict" | "unknown_dates";
type TaskDependencySummary = {
  predecessor_count: number;
  successor_count: number;
  conflict_count: number;
  unknown_date_count: number;
};
type TaskTimelinePage = {
  board_revision: number;
  from: string;
  to: string;
  outside_window_total: number;
  groups: Array<{
    section: "scheduled" | "unscheduled";
    total: number;
    items: TaskPlanningItem[];
    next_cursor: string; // empty means no next page, matching existing planning
  }>;
  dependencies: Array<{
    predecessor_task_id: string;
    successor_task_id: string;
    state: TaskDependencyState;
  }>;
};
```

`TaskPlanningItem` keeps its existing Task summary, Repository, preferred Agent, latest Run, and Run counts, and gains `dependency_summary`. The two warning counts count incident edges in both directions, using the full same-Project graph. Also add this computed summary to List/Board items and Task planning detail.

Scheduled rows have both dates and overlap the window inclusively; order by `(planned_start_date, id)` ascending. Unscheduled rows have either date absent, ignore the viewport dates, and order by `(created_at, id)` descending. `outside_window_total` counts matching, fully scheduled Tasks outside the window. Keep the full original dates on clipped bars.

`dependencies` contains every incoming edge for every returned row, including Unscheduled rows; its size is bounded by 20 times returned Tasks. Include IDs and computed state even when the predecessor is hidden. Do not return hidden Task titles in this collection endpoint or silently drop an edge. When one section page changes, replace only edges owned by that section's successor rows, retaining the other section's edges at the same revision. Draw an arrow only when both endpoints are visible scheduled rows. Textual counts and detail relationships carry the remaining context.

Read the Project revision, rows, exact totals, Run summaries, dependency edges, and full incident counts in one read-only REPEATABLE READ transaction. Use bounded set-based SQL, not per-row HTTP lookups. Read-only queries must not acquire mutation locks.

Versioned opaque cursors bind Project, normalized filters, inclusive window, section, limit, planning revision, and the section's final sort key. Allow up to 4096 encoded bytes to accommodate the supported Unicode query. Reject malformed or wrong-scope cursors with 422 and stale revisions with 409. Return `Cache-Control: no-store`. Cursor pagination must neither skip nor duplicate Tasks at equal sort dates/timestamps.

Extend `GET /tasks/{task_id}/planning` with `dependency_summary` and bounded `predecessors`. Each predecessor brief is `{id, title, status, planned_start_date, planned_finish_date, relation_state}`. Add `GET /tasks/{task_id}/dependents?limit=20&cursor=...`, maximum 100, returning `{board_revision, total, items, next_cursor}` with successor briefs of the same shape, ordered by UUID ascending. Its cursor binds source Task, Project, limit, revision, and final ID; apply the same snapshot and stale-cursor rules. Reset dependent pagination on Task/Project/revision changes, retaining any dirty Task draft; never present a stale dependent page as current beside refreshed detail counts.

Expose the two new reads as MCP `list_task_timeline` and `list_task_dependents`; extend existing `get_task_planning` output and `update_task` input. MCP defaults to 20 items per section/page and requires explicit Timeline dates. Preserve raw-field presence and nulls, conditionally require the planning revision, and retain the 512 KiB output guard with an actionable reduce-limit error. No silent truncation. Read-only MCP exposes both reads and no mutation. Final tool counts become 45 full / 24 read-only from the current 43 / 22.

## Shared view and detail behavior

Add Timeline as the third Tasks tab. Preserve Project, existing filters/search, selected Task, and any detail draft across view switches. Add URL `scale=day|week|month` (default week) and `date=YYYY-MM-DD` anchor. When entering Timeline without a valid date, write the browser-local Today into the URL once. Normalize invalid values; retain the anchor/scale while using List or Board. Page positions are local view state, not shareable URL state.

| User label | Grid and viewport | Previous / Next |
| --- | --- | --- |
| Day | Daily cells across the anchor month | One calendar month |
| Week | Weekly cells across the anchor quarter, including whole boundary weeks | Three calendar months |
| Month | Monthly cells across the anchor year | One calendar year |

Show the exact visible date range next to Previous, Today, Next, and the scale control. Fetch the renderer's actual visible interval, especially the boundary weeks of a quarter. One row represents one Task; its title button and bar open the shared detail. Show workflow and priority separately from execution state. No Timeline row reordering or drag between resources.

Use **bounded pagination**, not accumulated infinite rows: initially 50 scheduled and 50 Unscheduled Tasks, with independent Previous/Next controls and “Showing x–y of total.” Keep only the active page for each section and a scoped cursor history. Every Task remains reachable; no hidden total cap. A selected Task can remain open outside the current page or window. Show the outside-window count and distinguish loading, no matching Tasks, all Tasks Unscheduled, off-window schedules, and recoverable failure states.

Unscheduled is a labelled section with partial dates and a Schedule action that focuses the shared date editor. Detail provides Show on Timeline for complete schedules, changing the anchor to their start while preserving filters and selection. If a filter or page still hides the Task, keep detail open and explain the hidden state. After a bar moves outside the current window, announce the move and retain an Open Task / Show on Timeline route to it.

Add dates and **Depends on** to the existing Save changes form. Use `TaskPlanningDraft` extending the launch module's existing `TaskDraft`; do not put scheduling state into creation or launch drafts. Date fields are labelled, errors are inline, and saved warnings use text and icons as well as color. Earlier by one day / Later by one day changes both draft dates for a complete schedule, preserves duration, obeys date limits, and requires Save changes. This supplies a keyboard alternative to dragging.

The dependency picker uses existing Project-scoped Task planning search, at most 10 per workflow group with normal pagination, excluding self. Its query and status choice are independent of Timeline filters; allow selecting cancelled Tasks explicitly. Selected predecessors stay visible as a bounded draft set even when search results change. Save text, dates, and predecessors in one request.

Keep `useTaskPlanning` as the sole owner of Task drafts and writes, using the existing global pending key `['task-planning-write']`. A new `useTaskTimeline` owns only Timeline reads and bounded pages. Enable the grouped collection query only in List/Board, and Timeline queries only in Timeline. Share an active-collection status interface for loading, failure, refresh, and detail move guards. Invalidate detail, grouped collection, Timeline, dependents, and existing Task/Run-related caches using the captured Project scope after effective writes.

Poll visible active pages and detail every 15 seconds while idle. Refresh Run summaries even when planning revision is unchanged. Pause polling during an active gesture or write. Any planning revision change resets both Timeline page histories and refetches a consistent pair; never combine pages of different revisions. Stale 409s reset pages and require fresh intent, preserve dirty detail drafts, and offer explicit reload. Do not automatically replay a drag or overwrite a draft. Preserve existing route/Project dirty guards, source-content locks, and partial-launch recovery.

## Renderer choice and adoption limits

Select the free MIT **ReUI Radix Gantt**, pinned to commit `ef0fe1252d9b24d69bdedee48a69ec9b29d1f217`, behind a Circular adapter. It offers controlled events, exclusive end dates, dependency arrows, and touch gestures. Source inspection supports this choice; React 19/Vite runtime behavior still requires the implementation gate. The necessary source closure is roughly 9,400 lines, a material maintenance cost. [ReUI documentation](https://reui.io/docs/components/radix/gantt), [pinned source](https://github.com/keenthemes/reui/commit/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217), [MIT license](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/LICENSE.md).

| Alternative | Tradeoff |
| --- | --- |
| ReUI, selected | Fits controlled dates, weekly cells, arrows and touch; larger vendored surface and a small Today adaptation |
| Kibo at `3d63cdb15b79d972e3dc38a10997987672f9b263` | Smaller source, but lacks weekly cells and dependency arrows, uses mouse sensors, and needs more work for controlled rejection/cancellation plus additional packages |
| Custom bounded CSS/SVG view | Could reuse existing dependencies and keep a smaller initial display, but makes Circular responsible for all drag, resize, geometry and accessibility behavior |

Kibo findings come from its [pinned Gantt implementation](https://github.com/shadcnblocks/kibo/blob/3d63cdb15b79d972e3dc38a10997987672f9b263/packages/gantt/index.tsx) and [package manifest](https://github.com/shadcnblocks/kibo/blob/3d63cdb15b79d972e3dc38a10997987672f9b263/packages/gantt/package.json). If the ReUI integration gate exposes a fundamental mismatch, record evidence and revise the choice before broad adoption.

Copy the eight extracted Radix Nova registry files listed in the adjacent [source manifest](2026-10-04-task-schedules-gantt-sources.json), retaining MIT attribution. Omit navigation and demos; the recurrence module remains an internal import even with recurrence disabled. Add only `date-fns@4.1.0` and `@date-fns/tz@1.5.0`. Reuse existing Radix/Lucide/utils, supply missing local Checkbox/ContextMenu/ScrollArea/Tooltip wrappers, and replace registry-specific icon placeholders and aliases. Use a Circular toolbar and native date inputs. [Pinned Radix Nova registry entry](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/public/r/styles/radix-nova/gantt.json).

Map Circular Day/Week/Month to ReUI `month`/`quarter`/`year`; these retain calendar-day snapping. Disable infinite scrolling, slot selection, Task creation, summaries, recurrence, baselines and resource reordering; use one Task per resource and `scheduleMode="single"`. Rows are not virtualized, which is why pages stay bounded. Minimum visual bar width must never determine saved duration. Use the upstream range helper so quarterly boundary weeks match the query. [Range math](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/registry-reui/bases/radix/reui/gantt/gantt-lib.tsx), [rendering](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/registry-reui/bases/radix/reui/gantt/gantt-view.tsx).

Locally add `todayDate?: Date` to `GanttViewConfig` and its recognized keys, using it for both `todayDayKey` and `todayStartMs` in `gantt-view.tsx`. Supply the UTC-encoded local day from Circular. Set `nowIndicator={false}` and `initialCenter="anchor"`; the custom Today action sets the controlled anchor instead of calling upstream `api.today()`. Hiding the line alone does not correct upstream header highlights. Record this patch in component provenance.

## Drag, resize, and accessibility

First integrate with drag and resize disabled. Once date editing, reads and conflicts pass, enable permitted bar gestures. Movement preserves the complete stored duration, including clipped portions. Resizing changes only the exposed endpoint and never produces less than one day. Do not show resize handles on clipped edges. Keep keyboard opening and the date editor available at every scale and screen size. [Bar controls](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/registry-reui/bases/radix/reui/gantt/gantt-bar.tsx).

ReUI's `onEventUpdate` is synchronous, so start one date-only PATCH and immediately **return false**. Keep events controlled by canonical server reads; do not return a Promise, persist the component's event array, save on hover, or keep an optimistic date after failure. Include immutable Task/Project IDs, Task and planning revisions, original dates, and view scope in each event's `data`; validate that gesture snapshot against current state before sending the patch. [Update contract](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/registry-reui/bases/radix/reui/gantt/gantt.tsx).

There are no upstream `onDragStart` / `onDragEnd` props. Observe `useGanttSelector(state => state.drag)` or the instance subscription for gesture state, and use exported `cancelActiveGanttGestures()` when scope, revision, permission, or canonical data changes. A dirty detail, pending write, stale collection, or unconfirmed snapshot disables schedule gestures. Escape, blur, pointer cancellation, unmount, geometry/view changes, and release outside the chart must produce no write. Verify outside-drop behavior and add an adapter boundary guard if needed. [Gesture lifecycle](https://github.com/keenthemes/reui/blob/ef0fe1252d9b24d69bdedee48a69ec9b29d1f217/registry-reui/bases/radix/reui/gantt/gantt-dnd.tsx).

After canonical refresh, restore focus by stable Task ID because a moved event may remount. If it leaves the page/window, focus the relevant fallback control and announce the result. Announce start, proposed dates, cancellation, saving, success, and failures without duplicate messages. Verify real touch scrolling, long-press move, exposed-edge resize, long titles, dark/light themes, reduced motion and narrow-screen detail access. Visible arrows are decorative assistance; relationship text remains accessible and authoritative.

## Acceptance and handoff

The implementation must prove date round trips across DST and opposing timezones; atomic stale-write rejection; full-graph cycle protection under concurrent edits; complete hidden dependency context; bounded pagination with exact totals; and no Run or provider side effects. The implementation plan assigns named tests to these cases and retains the existing planning, console, launch, integration, MCP and delivery regression suites.

Stage 1 was implemented in an isolated baseline preserving existing dirty work and received a fresh final review with no actionable findings. Migration 0017, HTTP/MCP date editing, shared date fields, and List/Board labels are verified. Database verification used disposable PostgreSQL only; apply `go run ./cmd/circular-migrate` before running the updated API against a development database. No new frontend packages were needed. ReUI runtime compatibility and gesture integration were subsequently verified in October 8 browser checks; dependencies remain unimplemented. Native execution remains the chosen method.
