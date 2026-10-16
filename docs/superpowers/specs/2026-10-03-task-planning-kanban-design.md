# Task planning and Kanban

Status: implemented, verified and reviewed. See the completed implementation plan for checks and recorded rulings.

Parent: [Console and Task planning](2026-10-03-console-and-task-planning-design.md). Plan: [Task planning and Kanban implementation](../plans/2026-10-03-task-planning-kanban.md).

## Intent and scope

Let a person prepare engineering work before starting an Agent, organize it by workflow, and inspect each Task's attempts without losing Project context. The user accepted Save Task, shared List/Board views and Task details, manual stages, priority, preferred Agent, and persisted ordering. This moves Task planning ahead of the Run-review redesign. Gantt follows this foundation in a separate design.

The increment is complete when Tasks can be saved without execution, edited safely, found in either view, moved with pointer or keyboard, and explicitly launched through the existing flow. A successful Run never implies that its Task is Done.

## Global constraints

- Retain React 19, Vite, TanStack Router/Query, Tailwind 4, shadcn/Radix Nova, the current theme, and locally bundled Geist.
- Use Project, Repository, Agent, Task, Run, and Workspace consistently. One card represents one Task; its Runs remain distinct attempts.
- Planning mutations never start, retry, cancel, approve, publish, or reschedule a Run and never synchronize workflow to Linear or GitHub.
- Preserve existing Task/Run API envelopes, Run launch/retry semantics, imported identities, execution snapshots, provider callbacks, and phase 1 navigation/recovery behavior.
- Existing Tasks retain their workflow, content, external references, timestamps, and Run history through a forward-only migration.
- Keep Project scope in every collection query, draft, pagination cursor, and mutation; never display another Project's cached rows or apply a late result to it.
- Add only the three dnd-kit packages required by the selected free Kanban primitive; retain its MIT notice. Do not install another component suite or paid block source.
- Preserve all existing unrelated workspace changes. Establish an isolated baseline containing dirty source before implementation; do not branch from a clean HEAD that omits it.

## User experience

Add **Tasks** between Overview and Runs. `/tasks` has List and Board tabs, one shared toolbar, and a shared Task detail dialog. Default to List; the `view=list|board` URL value determines the view. The Board has fixed columns:

| Display | Existing/additive wire value |
| --- | --- |
| Backlog | `open` |
| Ready | `ready` |
| In progress | `in_progress` |
| Review | `in_review` |
| Done | `completed` |

Cancelled Tasks are hidden by default and available through the workflow filter. Selecting Cancelled shows the cancelled group in either view, with Move to available for restoration. No sixth column appears on the default Board. Stages are manual; completion and cancellation do not depend on or modify Run state. An active Run indicator remains visible even on a Done or cancelled Task.

List is a table grouped by workflow in the same saved order as Board. Both views use the same collection data and per-group Load more controls. Filters are literal title search, workflow, priority, Repository, and preferred Agent. Search is trimmed, case-insensitive, limited to 200 Unicode code points, and debounced by 250 ms with replace navigation. `%` and `_` are literal. Deliberate filter/view changes create history entries. The URL stores filters and optional `taskId`; changing view preserves the selection and filters.

Cards/rows show title, Repository or No Repository, priority, preferred Agent or No preferred Agent, and a separate execution summary. Show the latest attempt's status and actual Agent, plus an active-Run count when nonzero; a newer finished attempt must not hide an older active one. Counts come from the API. Do not invent progress percentages or treat priority as an execution scheduler.

At 390 px, columns/table may scroll inside their own surface, with no page-level overflow. Long names remain accessible. Task titles are real links that work in a new tab. Each card has a separate labeled drag handle; opening a Task never begins dragging.

### Save Task and explicit execution

Extend the existing New Task dialog with **Save Task** alongside **Start Run**. Title remains required (trimmed, 1–500 Unicode code points); description is optional. Repository and Agent may be unset for saving. Priority is `none | low | normal | high | urgent`, default `none`. The chosen Agent is saved as the preferred Agent. New Tasks start in Backlog. Default selections may be populated once; intentionally clearing a selection must survive refetch and reopening.

Save calls only `POST /tasks`, closes on success, invalidates planning queries, and opens the saved Task in List details. Start Run retains the existing create-then-launch behavior and stronger Repository/enabled-Agent validation. Its create step also saves priority/preference. A Task-created/Run-failed result retains exactly the same saved Task and actual selected Agent; Retry starting Run never creates a second Task. Save is unavailable for an already-saved or partial Task.

Task detail offers **Start Run** as a link to the existing `/?taskId=…` launcher. The launcher is the single place where an actual Agent is confirmed and execution starts; it must not stack on an open detail modal. Default its Agent to the discovery suggestion when applicable, otherwise the saved preferred Agent, otherwise the existing default. A disabled/missing saved preference requires an explicit new choice; never silently substitute another Agent. An enabled alternative changes this Run's Agent only unless the person separately saves a new preference. Label ordinary existing records Saved Task, and preserve Imported Task/discovery labels for their source types.

### Shared Task detail and editing

Details show editable Task fields, workflow, source link when available, and paginated Run history using actual Run links. History loads 20 attempts at a time through an additive `task_id` filter on the phase 1 Run queue endpoint; other Run queue callers keep their behavior. Old attempts remain available through Older Runs.

Use explicit Save changes. Preserve draft text on errors and 409 conflicts; do not replace unsaved fields during polling. A conflict displays “This Task changed elsewhere. Reload its latest version before saving.” Reload is an explicit action that discards the stale draft; do not automatically replay an edit. Closing or leaving a dirty detail requires a normal unsaved-changes choice; view switching alone does not discard it. Lock user Project selection while initial detail scope resolves, while editing a dirty draft, and while a save/move is pending. A clean detail follows its loaded Task's Project; changing Project closes that detail and resets Repository/Agent filters and pagination, while retaining view, title search, priority, and workflow filter.

Editing rules reflect existing execution behavior:

- Local title/description may be edited unless any Run is nonterminal (`queued`, `provisioning`, `running`, `waiting_for_approval`, `waiting_for_input`, `finalizing`). Check under the same Task row lock used by Run launch.
- Repository may be changed or cleared only before any Run exists. Project and external references are never editable here.
- Linear-imported, external-request, and repository-discovery source title/description/Repository remain read-only. Detect managed source from existing Linear issue references, Circular discovery kind, or an external-request link, not from the mere presence of arbitrary external references.
- Workflow, priority, and preferred Agent remain locally editable for all Tasks, including Tasks with active Runs. Preferences must reference the same Project; an already-saved preference can remain displayed if the Agent is later disabled. Choosing a new disabled Agent is rejected; clearing a preference is allowed.
- Existing completed attempts retain their Events, Artifacts, and recorded snapshots. The mutable Task description on Run detail is labeled **Current Task context**; this slice does not introduce historical input snapshots for ordinary Runs.

Changing workflow uses the move operation and appends to the target stage by default. Dirty field edits must be saved or discarded before a separate move. Cancellation is a workflow choice, with text explaining that active Runs continue and require their own cancellation action.

## Ordering and concurrent work

Use dense, zero-based integer positions within each `(Project, status)`. The client sends a move intent, never an entire reordered board. A move carries the expected Task revision, expected Project board revision, destination status, and one placement: start, end, before an anchor, after an anchor, or one step up/down in the complete saved order. Up/down require the same destination status as the current Task.

The server locks Project then Task, checks both revisions, reads the complete source/target order, removes the moved Task, inserts it at the requested location, and updates only changed positions atomically. Anchors must be different Tasks in the same Project and target stage. Do not use visible array indexes as persisted positions.

For example, a filtered stage with full order `[A, H, B]` hides H. Moving X before visible B gives `[A, H, X, B]`. A drop after the last loaded card uses that card as an after-anchor; it does not move unloaded cards. Dropping on an empty-looking column uses the actual stage end, including hidden cards. The ordinary Move to menu also appends; Move up/down resolves the real adjacent card on the server. Fixed stage headers are not draggable.

Task revision protects semantic edits. Board revision protects ordering and collection membership. Effective title/description/Repository/priority/preference/status changes increment both; a pure reorder increments board revision only. Neighbors' revisions/timestamps do not change when their positions shift. No-ops change neither revision. A stale revision returns 409, with no partial writes, and requires refreshing before another intent. Creation increments board revision; Run state changes do not.

Use ReUI's commit-only `onMove` mode. Retain the authoritative placement while saving and mark the Task busy, then refetch the affected Project collection. Only one planning mutation is pending per console at a time. On error keep/refetch canonical state and show a retryable error; never restore a snapshot over newer data or replay a failed move automatically. Pause collection polling during a drag or mutation; a changed revision invalidates a gesture's captured intent. Escape/outside drop sends no request.

## Storage and compatibility

Migration `0016.sql` adds `tasks.priority` (default none), nullable `preferred_agent_id`, `revision` (default 1), and private `board_position`; add private `projects.task_board_revision` (default 1). Use checks for valid workflow/priority, positive revisions, nonnegative position, and same-Project preferred Agent membership. A composite foreign key to `(agents.project_id, agents.id)` enforces scope; use deferred NO ACTION so whole-Project cascades remain valid, and never clear only half the pair. No Agent deletion API is added. Disabled Agents are retained.

Backfill positions per Project/status using `created_at DESC, id DESC`, without modifying existing timestamps/content/status. Add `(project_id,status,board_position,id)` index. Positions need not have a uniqueness constraint that conflicts with transactional renumbering; serialization and tests establish the invariant.

A BEFORE INSERT Task trigger locks the Project `FOR NO KEY UPDATE` and assigns the complete stage's next position. AFTER INSERT increments board revision. This covers ordinary create, discovery, Linear import, and external-request insertion without rewriting their launch workflows. Existing no-op import upserts must preserve position/revisions. Do not use a general UPDATE trigger that would reverse Project→Task lock order. Planning writes take Project `FOR NO KEY UPDATE` then Task `FOR UPDATE`; Agent/Repository checks use ordinary reads, not competing update locks.

Add only physical columns `priority`, `preferred_agent_id`, and `revision` to `TaskRead`, since the generic HTTP projection maps contract fields directly to table columns. Preserve old Task GET/list envelopes and created-time ordering. Add optional priority/preference to TaskCreate, retaining Backlog as its default workflow and existing unknown-field compatibility. Validate supplied new fields explicitly; the generic decoder currently does not enforce enum/integer constraints.

## HTTP read and mutation contracts

New operations use focused files under `internal/httpapi` and `internal/postgres`, reusing existing error/transaction conventions. No provider or worker protocol change is required.

| Operation | Request and result |
| --- | --- |
| `GET /projects/{project_id}/task-planning` | Shared grouped List/Board collection; parameters below; returns `TaskPlanningPage` |
| `GET /tasks/{task_id}/planning` | Task details and authoritative editing capabilities; returns `TaskPlanningDetail` |
| `PATCH /tasks/{task_id}` | Required `expected_revision`; optional title, description, nullable repository_id, priority, nullable preferred_agent_id; returns `{task: TaskRead, board_revision}` |
| `POST /tasks/{task_id}/move` | Required expected_revision, expected_board_revision, status, placement; anchor_task_id required only for before/after; returns `{task: TaskRead, board_revision}` |

Collection filters: `q` (200 code points), optional status/priority enums, optional Repository/preferred-Agent UUIDs, `limit` 1–100 (default 50 per group), and `cursor` up to 2048 bytes. Omitted status returns the five default stages; explicit status returns that one group, including cancelled. A cursor requires explicit status. Filters never change the underlying saved order. Unknown/foreign filter resources fail validation instead of silently using another Project.

`TaskPlanningPage` is `{board_revision, groups: TaskPlanningGroup[]}`. Each group is `{status, total, items, next_cursor}`; total is the exact filtered count in that snapshot. Empty groups remain present. `TaskPlanningItem` contains a lean Task summary (`id,project_id,repository_id,title,status,priority,preferred_agent_id,revision,created_at,updated_at`), nullable Repository `{id,name}`, nullable preferred Agent `{id,name,backend,enabled}`, nullable latest Run `{id,status,attempt,agent_id,agent_name,kind,created_at,started_at,finished_at}`, `run_count`, and `active_run_count`. Do not repeat descriptions or external references across collection rows. Use joins/lateral aggregates in bounded SQL, not per-card HTTP calls.

Read revision, filtered counts, and all initial groups in one read-only REPEATABLE READ transaction. One request provides a coherent initial Board; later requests load one group's next page. Cursor v1 binds Project, normalized filters/status, board revision, last position, and Task ID, encoded as URL-safe base64 JSON. Validate syntax before database access; wrong scope is 422 and changed revision is 409. Never combine pages from different revisions. On a stale page reset all groups to a new initial snapshot, preserve filters/detail, and announce that the list changed. There is no hidden total-card cap.

`TaskPlanningDetail` is `{task: TaskRead, repository, preferred_agent, board_revision, content_editable, repository_editable, content_lock_reason, repository_lock_reason}`. Lock reasons are null or `source_managed`, `active_run`, `has_runs` as applicable. The server rechecks capabilities on every write; client flags are explanatory. Query the existing Run queue with the detail Task's Project and `task_id` for attempt history. Bind this optional Task filter into Run cursors and query keys; old cursors without it remain valid for unfiltered callers.

New PATCH/move requests use a strict, presence-aware decoder with a 128 KiB body limit and duplicate/unknown-field rejection. Omission preserves a value; null explicitly clears nullable fields. The move anchor is a nonnull UUID required for before/after and forbidden for other placements. Revisions are positive safe integers (at most 9007199254740991); reject fractional, negative, oversized, or coerced values. Title is trimmed and nonblank; changed descriptions are limited to 20000 code points. Do not reject or truncate a longer legacy description when it was omitted. New creation retains its historical description contract. Invalid input is 422, missing scoped resources 404, stale edits/locked content 409, and oversized bodies 413. Set planning reads `Cache-Control: no-store`.

Add MCP `list_task_planning`, `get_task_planning`, `update_task`, and `move_task` through the HTTP client. Preserve existing `list_tasks` and `get_task`; extend create_task with optional planning fields. Preserve explicit null clearing in tool serialization. When new fields are supplied, verify advertised API planning support before creation, because older APIs silently ignore unknown fields. Read-only MCP connections expose no new mutations. Tools clearly state that workflow changes do not execute or cancel Runs.

## Frontend boundaries and states

Put Task-specific components/query helpers under `apps/web/src/components/tasks/` and `apps/web/src/lib/task-planning.ts`; compose them in `pages/tasks.tsx`. Keep routing in main.tsx and extend the existing shell/launcher rather than introduce another Project provider. Build Task detail from the existing Radix Dialog and controls.

The initial grouped query key includes Project and normalized filters. Loaded pages additionally include group, cursor, and snapshot revision. List/Board share a cache and local loaded-page state; view is presentation only. Task detail uses its own identity key. Mutation callbacks capture Task/Project/revisions, update only that scope, and invalidate planning/detail/Task/affected Run queue keys. Query failure must remain distinguishable from empty results. Retain last data only for its exact scope with a visible stale notice; disable moves until current ordering is confirmed.

Poll planning snapshots every 15 seconds while visible and idle, and visible newest Task attempt history every 2 seconds using the existing Run query helper. Preserve selected Task and field drafts across same-route view/filter changes; keep a selected Task visible in details if it no longer matches filters. Browser Back/Forward and Project changes must not revive foreign pages, details, or queued mutation results.

## Component decision

Adopt the free ReUI Radix Kanban source, with current registry-local imports adapted to Circular. Its documented `onMove` mode gives one drop intent without live cross-column mutation; use separate handles and fixed columns. The ordinary Move to/up/down controls remain first-class. See [ReUI persistence API](https://reui.io/docs/components/radix/kanban) and [source](https://raw.githubusercontent.com/keenthemes/reui/main/registry-reui/bases/radix/reui/kanban.tsx).

The dependency boundary is `@dnd-kit/core` 6.3.1, `@dnd-kit/sortable` 10.0.0, and `@dnd-kit/utilities` 3.2.2; retain the source's [MIT notice](https://raw.githubusercontent.com/keenthemes/reui/main/LICENSE.md). Existing radix-ui and cn utilities suffice. Versions were checked against the [ReUI manifest](https://raw.githubusercontent.com/keenthemes/reui/main/package.json). Source inspection supports Vite compatibility; keyboard/touch/React 19 behavior still requires implementation-time browser verification.

Alternatives considered: Kibo's [Kanban source](https://raw.githubusercontent.com/shadcnblocks/kibo/main/packages/kanban/index.tsx) needs additional tunnel-rat/ScrollArea integration and more reconciliation around hover updates. A custom native-drag board would require implementing keyboard/touch behavior already supplied by dnd-kit. Neither is preferred for this slice.

## Acceptance and deliberate exclusions

Verify saved-only creation with no Run request; existing launch and partial retry; all five stages and cancelled restoration; source/active-Run editing rules; explicit null clears; reload-persistent order; hidden/unloaded neighbors; >50 cards in a stage; concurrent create/edit/move and stale versions; independent Project caches; dirty drafts; disabled preferred Agents; Back/Forward; touch scrolling, keyboard handles, Escape cancellation, ordinary move controls, and 390/1440 px layouts. Re-run contract, migration, HTTP, persistence, MCP, import/discovery/request, and relevant console/execution browser regressions using disposable PostgreSQL and fake workers/providers.

Gantt dates/dependencies, automatic scheduling or workflow advancement, custom stages, assignees/teams, bulk editing, provider workflow synchronization, Run-review redesign, and stronger uncertain-create idempotency remain outside this increment.
