# Task Planning and Kanban Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let people save, edit, organize, and explicitly launch Tasks from shared List and Kanban views without coupling workflow to execution.

**Architecture:** Add planning columns and serialized placement operations to the existing Task model. Expose a bounded grouped read model and revision-checked mutations through HTTP/MCP, then reuse the phase 1 shell, launcher, and Run queue in a Task feature. One canonical collection powers List and Board.

**Tech Stack:** Go, PostgreSQL/pgx, OpenAPI/generated TypeScript, React 19, TanStack Router/Query, Tailwind 4, Radix Nova, ReUI Kanban/dnd-kit, Vitest, Playwright.

**Spec:** [Task planning and Kanban design](../specs/2026-10-03-task-planning-kanban-design.md).

## Global Constraints

- Retain React 19, Vite, TanStack Router/Query, Tailwind 4, shadcn/Radix Nova, the current theme, and locally bundled Geist.
- Use Project, Repository, Agent, Task, Run, and Workspace consistently. One card represents one Task; its Runs remain distinct attempts.
- Planning mutations never start, retry, cancel, approve, publish, or reschedule a Run and never synchronize workflow to Linear or GitHub.
- Preserve existing Task/Run API envelopes, Run launch/retry semantics, imported identities, execution snapshots, provider callbacks, and phase 1 navigation/recovery behavior.
- Existing Tasks retain their workflow, content, external references, timestamps, and Run history through a forward-only migration.
- Keep Project scope in every collection query, draft, pagination cursor, and mutation; never display another Project's cached rows or apply a late result to it.
- Add only the three dnd-kit packages required by the selected free Kanban primitive; retain its MIT notice. Do not install another component suite or paid block source.
- Preserve all existing unrelated workspace changes. Establish an isolated baseline containing dirty source before implementation; do not branch from a clean HEAD that omits it.

## Review Focus

- An old Task draft or move must not overwrite another console's changes, including simultaneous creation or import; pin revisions, no-ops, rollback, and lock order in Tasks 1–2.
- Hidden or unloaded neighbors must keep their relative order when a filtered card moves; pin full-stage anchor semantics and versioned pagination in Tasks 1, 2, and 4.
- Editing instructions must not race a queued Run into executing unintended content; preserve source-managed inputs, Repository identity, and Task/Run independence in Tasks 1–3.
- Delayed responses, browser Back/Forward, or a dirty detail must not switch Task/Project identity or discard text; pin URL scope, conflict recovery, and Save/launch recovery in Task 3.
- Keyboard/touch users must be able to move cards, cancel gestures, and read long names without accidental launches or duplicate writes; pin cancellation, ordinary controls, disabled preferences, and narrow screens in Tasks 3–4.

## Working baseline and ownership

The workspace contains substantial prior tracked and untracked changes, including phase 1. Record status and make an isolated baseline containing them before implementation. If protected Git metadata prevents a linked worktree, retain a source snapshot and transfer only verified deltas after checking original files for concurrent edits. No product edits, installs, migrations, or test execution were performed while writing this plan.

| Boundary | Responsibility |
| --- | --- |
| `internal/migrate/0016.sql` | Neutral planning defaults, deterministic backfill, creation ordering/revision triggers |
| `internal/postgres/task_planning.go` | Task capabilities, revision checks, content updates, canonical move transaction |
| `internal/httpapi/task_planning.go` | Focused write validation, route handlers, error mapping |
| `internal/httpapi/task_planning_read.go` | Grouped collection/detail SQL and scoped cursor encoding |
| `internal/controlmcp/task_planning.go` | Planning tool schemas and HTTP delegation |
| `apps/web/src/lib/task-planning.ts` | URL/filter normalization, query keys, group-page reconciliation, drop intent |
| `apps/web/src/components/tasks/use-task-planning.ts` | Scoped collection/detail state and mutation ownership |
| `apps/web/src/components/tasks/{task-list,task-detail,task-board}.tsx` | List, shared detail, Board rendering and accessible controls |
| `apps/web/src/components/reui/kanban.tsx` | Adapted upstream interaction primitive and provenance |
| `apps/web/src/pages/tasks.tsx` | Shared toolbar, view switch, collection/detail composition |

Read `CONTEXT.md`, the spec, `docs/development/ui-components.md`, and the existing launch/import tests first. Keep existing Run detail in main.tsx; only its current-context label changes. Planning HTTP paths below are relative to `/api/v1`.

### Task 1: Establish planning storage and safe mutations

**Files:**

- Create `internal/migrate/0016.sql`, `internal/migrate/task_planning_test.go`.
- Modify `internal/migrate/migrate.go`, `internal/migrate/migrate_test.go` for head 0016 and accepted prior head 0015.
- Create `internal/postgres/task_planning.go`, `internal/postgres/task_planning_test.go`.
- Exercise existing `internal/postgres/run_launch.go`, `internal/integrations/imports.go`, `internal/integrations/request_launch.go`, `internal/httpapi/discovery.go`; preserve their public behavior.

**Interfaces produced in postgres:**

```go
type TaskPlanningPatch struct {
    Title, Description, Priority *string
    RepositorySet, PreferredAgentSet bool
    RepositoryID, PreferredAgentID *uuid.UUID
}
type TaskMoveInput struct {
    ExpectedRevision, ExpectedBoardRevision int64
    Status, Placement string // start, end, before, after, up, down
    AnchorID *uuid.UUID
}
type TaskPlanningResult struct {
    TaskID uuid.UUID
    Revision, BoardRevision int64
    Changed bool
}
type TaskEditCapabilities struct {
    ContentEditable, RepositoryEditable bool
    ContentLockReason, RepositoryLockReason *string
}
func UpdateTaskPlanning(ctx context.Context, tx pgx.Tx, taskID uuid.UUID,
    expectedRevision int64, patch TaskPlanningPatch) (TaskPlanningResult, error)
func MoveTask(ctx context.Context, tx pgx.Tx, taskID uuid.UUID,
    input TaskMoveInput) (TaskPlanningResult, error)
func TaskPlanningCapabilities(ctx context.Context, tx pgx.Tx,
    taskID uuid.UUID) (TaskEditCapabilities, error)
```

`RepositorySet`/`PreferredAgentSet` distinguish omission from an explicit nil clear. Export `ErrTaskPlanningInput` and `ErrTaskPlanningConflict` for 422/409 mapping; missing records remain `pgx.ErrNoRows`. Functions use a caller-owned transaction and acquire Project `FOR NO KEY UPDATE` before Task `FOR UPDATE`. Task ID/Project lookup does not lock Task first. Capability reads share the same source and Run predicates as updates; updates recheck under lock.

- [x] **Step 1: Write failing migration and persistence tests.** Use disposable database helpers already in these packages. Pin these named cases and exact outcomes:

  | Test | Assertions |
  | --- | --- |
  | `TestTaskPlanningMigrationPreservesHistory` | Upgrade 0015 with open/in_progress/completed/cancelled Tasks and Runs; status/content/refs/timestamps unchanged, priority none, preference null, revision 1; positions created_at DESC then id DESC, starting 0; repeat Up is harmless |
  | `TestTaskPlanningCreationAndReplay` | Concurrent ordinary/import/discovery insert paths append distinct positions; effective insert bumps board revision; duplicate Linear upsert preserves all Task content, position, Task revision and board revision |
  | `TestTaskPlanningPatchPresenceAndScope` | Omitted fields retained, explicit nil clears, same-Project Agent/Repository enforced, disabled existing preference retained, new disabled choice rejected; no-op leaves revisions unchanged |
  | `TestTaskPlanningContentLocks` | Managed Linear/discovery/external-request content immutable; all six active Run statuses block title/description; any historical Run blocks Repository; metadata remains editable |
  | `TestTaskPlanningLaunchRace` | Synchronize edit and CreateRun in two transactions, both lock orders; the second sees committed inputs/active Run, with no deadlock or intermediate write |
  | `TestTaskPlanningCanonicalMoves` | Same/cross-stage, start/end, before/after, up/down and cancelled restoration; `[A,H,B] + X before B => [A,H,X,B]`; empty filtered stage appends after hidden Tasks; all positions dense afterward |
  | `TestTaskPlanningMoveConflictAndNoop` | Stale Task/board, foreign/self/wrong-stage anchor rejected without writes; pure reorder only bumps board revision; status change bumps both; neighbor timestamps/revisions unchanged |
  | `TestTaskPlanningConcurrentIntents` | Competing moves/edit/create serialize; stale intent cannot silently replay; transaction rollback restores positions/revisions; moving Task to Done/cancelled creates no Run/Event/provider effect and leaves existing Run unchanged |

- [x] **Step 2: Observe the intended red result.** Run `go test ./internal/migrate ./internal/postgres -run 'TestTaskPlanning' -count=1 -v` with `TEST_DATABASE_URL`. Missing functions/columns should fail; a skipped or unreachable database is not red evidence.
- [x] **Step 3: Implement migration and transaction functions.** Add the spec's columns/checks/index, composite Agent scope FK with deferred NO ACTION and required unique `(project_id,id)` target, and deterministic backfill. Creation triggers assign `max(board_position)+1` under the Project lock and bump board revision only after an actual insert. Do not introduce an UPDATE trigger. Validate supported enums, safe revision bounds, source-managed rules, and Agent choices in persistence as well as HTTP. Read full source/target stage IDs, splice relative to the one anchor, and renumber changed positions. Increment revisions exactly as specified; update moved Task `updated_at` only for semantic changes. Validate Agent/Repository with ordinary reads to preserve existing request-launch lock ordering.
- [x] **Step 4: Verify the persistence slice.** Run `go test ./internal/migrate ./internal/postgres ./internal/integrations -count=1`, then `go test -race ./internal/postgres -run 'TestTaskPlanning' -count=1`. Confirm import replay, normal/keyed Run launch, external-request and PR-review snapshots still pass with real database coverage.
- [x] **Step 5: Commit only this slice** as `feat: add task planning state and revisioned moves`.

### Task 2: Expose bounded planning reads and HTTP/MCP writes

**Files:**

- Create `internal/httpapi/task_planning.go`, `task_planning_read.go`, `task_planning_test.go`.
- Modify `internal/httpapi/api.go`, `run_queue.go`, `run_queue_test.go`; extend existing create behavior without changing old envelopes.
- Modify `contracts/openapi.json`, regenerate `apps/web/src/generated/api.ts`, extend `apps/web/src/api.ts`.
- Create `internal/controlmcp/task_planning.go`, `task_planning_test.go`; modify `server.go`, `mutations.go`, `client.go` only for registration/create capability handling.

**Interfaces:**

- Consume Task 1 transaction functions and `projection`, `record`, `identifier`, `integerQuery`, `rollback` from the HTTP package.
- Add handlers `(*api).taskPlanning`, `taskPlanningDetail`, `updateTaskPlanning`, `moveTask` with ordinary `(http.ResponseWriter,*http.Request)` signatures; register the four spec routes.
- Private decoder `decodeTaskPlanningUpdate(w,r) (int64, postgres.TaskPlanningPatch, bool)` and `decodeTaskMove(w,r) (postgres.TaskMoveInput,bool)`: strict 128 KiB JSON-object limit, unique/known fields, presence-aware nullable values; do not reuse `body()` for PATCH. The 128 KiB limit accommodates 20000 four-byte Unicode description characters. Preserve legacy body behavior for old create fields.
- OpenAPI schemas: `TaskPriority`, updated `TaskStatus`/`TaskCreate`/`TaskRead`, `TaskPlanningSummary`, `TaskPlanningRepository`, `TaskPlanningAgent`, `TaskPlanningRun`, `TaskPlanningItem`, `TaskPlanningGroup`, `TaskPlanningPage`, `TaskPlanningDetail`, `TaskPlanningUpdate`, `TaskMove`, `TaskPlanningMutation`. Fields and nulls match the spec; joined data never enters generic `TaskRead`.
- `TaskPlanningMutation` is `{task: TaskRead, board_revision:number}`. PATCH accepts no workflow/order fields; move uses required revisions/status/placement and nonnull UUID `anchor_task_id` required for before/after and forbidden for other placements.
- Frontend aliases use generated schemas. Add `TaskPlanningQuery {q?:string;status?:Task['status'];priority?:TaskPriority;repository_id?:string;preferred_agent_id?:string;limit?:number;cursor?:string}` and methods `api.taskPlanning(projectId,query?)`, `api.taskPlanningDetail(taskId)`, `api.updateTaskPlanning(taskId,input)`, `api.moveTask(taskId,input)`. Export `TaskCreate` for shared form validation.
- Extend `RunQueueQuery` with optional `task_id:string`, HTTP query and Run cursor scope correspondingly; all pre-existing unfiltered cursors still decode with empty Task scope. Validate that the Task belongs to the Project and return 404 otherwise.
- MCP registration `(*client).addTaskPlanningTools(server *mcp.Server)` and capability check `requireTaskPlanning(ctx context.Context) error`. Tools use the same filter/update/move fields with project_id/task_id. `list_task_planning`/`get_task_planning` return the planning envelope; mutations return the mutation envelope, and get includes `/tasks?taskId=…` console URL. Existing list/get tool results remain unchanged.

- [x] **Step 1: Add failing HTTP and MCP contract cases.**

  | Test | Assertions |
  | --- | --- |
  | `TestTaskPlanningGroupedRead` | One initial response contains five default groups, 50 items/group maximum, exact filtered totals including >50 Tasks, nullable joined resources, real Run counts/latest attempt/older active count, and no foreign Project Tasks |
  | `TestTaskPlanningFiltersAndCursor` | Literal `%`/`_`, mixed case, Unicode length; each enum/resource filter; explicit cancelled group; limit+1 cursors traverse tied records once; wrong filter/Project/group cursor 422; revision change 409; empty known Project vs unknown 404 |
  | `TestTaskPlanningValidationWithoutDatabase` | Unreachable pool still rejects unknown status/priority, limit 0/101/1.5, cursor without status, >2048-byte cursor, invalid UUID, revision 0/1.5/9007199254740992, duplicate/unknown fields, invalid null, blank/overlong title, >20000 changed description and body >128 KiB |
  | `TestTaskPlanningUpdateAndMoveHTTP` | Clear versus omit, protected fields/capabilities, exact 404/409/422 behavior, canonical result/revisions, no-op handling and 409 with no writes; old GET /tasks remains an array, old POST defaults Backlog |
  | `TestRunQueueTaskFilterCompatibility` | Only target Task's attempts, newest/older pagination, correct scope errors, task filter encoded in cursor; existing Project-only cursors and responses unchanged |
  | `TestTaskPlanningMCP` | New reads/mutations round-trip revisions; explicit null clears rather than disappears; old list/get shapes; read-only registration excludes writes; errors remain bounded; no Run created by planning tools |
  | `TestTaskPlanningMCPOlderAPI` | New create options fail clearly if OpenAPI lacks planning support, without sending POST; old create without new options still works |

  Seed an imported Task and verify re-import preserves metadata/order. Assert collection reads are no-store and coherent if an insert commits between revision and row reads.

- [x] **Step 2: Observe red.** Run `go test ./internal/httpapi ./internal/controlmcp -run 'TaskPlanning|RunQueueTaskFilter' -count=1 -v`; the missing operations/contracts must fail rather than skip.
- [x] **Step 3: Implement handlers/read model/contracts and MCP.** Use a read-only REPEATABLE READ transaction for initial groups, totals and revision; select only limit+1 items/group with windowing/bounded joins. Emit all requested empty groups. Cursor v1 contains Project, canonical filters/status, board revision, position, Task UUID; verify syntax before database access and reject changed snapshots. Fetch and return mutation TaskRead within the successful transaction. Extend TaskCreate validation for priority/preference while keeping optional omitted fields neutral and historical unknown-field behavior.

  Register planning MCP tools with explicit input schemas and `mcp.AddTool[map[string]json.RawMessage,object]` where nullable PATCH presence must survive; the installed SDK supports overriding `Tool.InputSchema`. Validate path UUIDs and retain the existing 512 KiB result bound and HTTP client protections. Do not loosen the shared `add()` helper's UUID validation for other tools. Exclude Task ID from forwarded body; forward present nulls exactly. `requireTaskPlanning` checks advertised TaskCreate priority/preference and planning paths before a create containing either new option. Reads can use normal typed arguments. Add supported planning defaults to existing `create_task` without changing execution annotations.
- [x] **Step 4: Generate and verify.** Run `corepack pnpm contracts:generate`, `corepack pnpm contracts:check`, `corepack pnpm typecheck`, and `go test ./internal/httpapi ./internal/controlmcp ./internal/integrations -count=1`. Expect current contracts and passing database-backed tests, including existing discovery, keyed launch, private request and provider callback cases.
- [x] **Step 5: Commit only this slice** as `feat: expose task planning APIs and MCP tools`.

### Task 3: Ship Save Task, grouped List, and shared details

**Files:**

Frontend paths in Tasks 3–4 are relative to `apps/web/src/` unless they begin with `apps/`, `tests/`, `docs/`, or name a root config/lock file.

- Create `apps/web/src/lib/task-planning.ts`, `task-planning.test.ts`; `components/tasks/use-task-planning.ts`, `task-list.tsx`, `task-detail.tsx`; `pages/tasks.tsx`.
- Modify `main.tsx`, `components/console/shell.tsx`, `task-launcher.tsx`, `task-launcher-context.tsx`, `launch.ts`, `launch.test.ts`, `lib/run-queue.ts`/test, as required by the new flow.
- Create `tests/browser/tasks.spec.ts`, `playwright.tasks.config.ts`, and `tests/browser/task-planning.spec.ts` for real API acceptance. Keep existing console tests meaningful and update only changed public labels/choices.

**Interfaces:**

- `TaskPlanningSearch {view:'list'|'board';q:string;status:Task['status']|'';priority:TaskPriority|'';repository_id:string;preferred_agent_id:string;taskId:string}`; `parseTaskPlanningSearch(Record<string,unknown>):TaskPlanningSearch` normalizes invalid URL values to defaults and caps q. Group page cursors stay in collection state, scoped by Project/filter/revision, rather than adding six independent cursors to the URL.
- `taskPlanningOptions(projectId:string,query:TaskPlanningQuery)` and `taskPlanningDetailOptions(taskId:string)` return scoped TanStack queryOptions. The former polls every 15000 ms while visible; caller suppresses polling during interaction. No cross-key placeholder data.
- `mergeTaskPlanningPage(current:TaskPlanningPage,next:TaskPlanningPage):TaskPlanningPage|null` appends one group only when revisions match, deduplicates Task IDs, preserves totals/order, and returns null on mismatched revision so the caller resets to an initial snapshot. The hook calls it only for pages with the same captured Project/filter query key; revision equality alone does not establish matching scope.
- `useTaskPlanning(projectId:string,search:TaskPlanningSearch)` owns initial/group-page/detail queries, exact captured mutation scope, errors, dirty draft and pending state; returns typed collections, selected detail, setters, reload/save/move/loadMore actions. Do not hide navigation inside rendering-only components.
- `TaskList({page,onLoadMore,onOpenTask,loadingGroups})`, `TaskDetail({detail,draft,onDraftChange,onSave,onMove,onReload,onClose,pending,error})` use generated data and typed callbacks; detail handles accessible discard confirmation before close. `TasksPage()` composes them; Task 4 adds Board without another data owner.
- Export `TaskDraft {title:string;description:string;repository_id:string|null;preferred_agent_id:string|null;priority:TaskPriority}` and `validateTaskDraft(draft:TaskDraft,project:Project,repositories:readonly Repository[],agents:readonly Agent[]):TaskCreate` from `launch.ts`. Validation trims title, checks supplied resources belong to the Project and a newly chosen Agent is enabled, and returns optional Repository/preference plus priority without requiring execution resources. Extend `LaunchSelection` with `priority?:TaskPriority`; `launchTask` consumes the same normalization plus its existing strict launch prerequisites. Preserve `LaunchError.taskId`.
- Extend `runQueueOptions` normalization to retain optional task_id in both query key and request; do not discard it through the URL-only Run queue search parser.

- [x] **Step 1: Write unit/browser cases before UI implementation.** In a standalone mocked-HTTP config on port 15178, test:

  - `Save Task never starts execution`: title-only save sends one POST /tasks and zero POST /runs, even with no Repository/Agent; success opens its detail. Intentional unset selections survive async option loads/reopen.
  - `Saved Task launch keeps identity and Agent choice`: start through `/?taskId`, use saved preference or explicit replacement for a disabled preference; no second Task POST; discovery suggestion wins; failed Run creation retains chosen identity/Agent on retry.
  - `Task List edits and conflict recovery`: metadata, content permissions, null clears, dirty close/route guard, and stale 409; typed text remains until explicit Reload. Imported content readonly but metadata editable.
  - `Task views preserve URL and Project scope`: List filter/history and selected Task survive presentation changes; deep-link aligns Project, clean Project switch clears Task/resource filters, dirty/pending selection locks; late A response/save never appears in B.
  - `Task history retains every attempt`: task_id reaches Run queue; 20-row paging, multiple/active attempts and links, no fabricated acceptance state. Phase 1 queue keys stay compatible.

  Unit cases pin parsing defaults, trimmed 200-code-point search, nullable draft values, Agent defaults and `mergeTaskPlanningPage` same/different revisions. Test `validateTaskDraft` separately from actual launch requirements, including 500 astral Unicode code points.

- [x] **Step 2: Observe red.** Run the focused Vitest files and `corepack pnpm exec playwright test --config playwright.tasks.config.ts`. Missing Save/Tasks/detail controls and query handling are the expected failures.
- [x] **Step 3: Implement the flow.** Add `/tasks` with the validated search contract and navigation entry. Until Task 4, keep Board switch visibly unavailable with no pretend Board implementation. Build grouped List and shared Dialog details with empty/loading/error/stale states, per-group count and Load more. Use one collection state owner across views. Preserve exact task/Project identity and dirty drafts; combine `useProjectSelectionLock` with the router's dirty-navigation blocker and before-unload protection. Browser navigation and modal close use the same save/discard policy.

  Add Save Task to the global launcher, split save/launch validation, store one-time default initialization separately from intentionally empty selection, and preserve partial launch recovery. Success invalidates planning queries and navigates to `/tasks?view=list&taskId=<saved>`. Imported/partial sessions show no Save action. Existing Task detail Start Run links to Overview's existing taskId launcher; honor preferred Agent precedence without silent disabled substitution. Use **Current Task context** on Run detail. Consume Task 2's capability flags and enforce the same rules on the server; no frontend-only permission assumptions.
- [x] **Step 4: Verify with mocks and the real API.** Run `corepack pnpm test`, `corepack pnpm typecheck`, the tasks mock config, and the default Playwright config with `tests/browser/task-planning.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts tests/browser/integrations.spec.ts`. The real planning case creates a Project/Task, edits metadata, reloads, starts the saved Task using a fake worker, and verifies the same Task ID and independent workflow. A second case proves active instructions cannot be edited and imported source content remains fixed.
- [x] **Step 5: Commit only this slice** as `feat: add saved tasks and planning details`.

### Task 4: Add Kanban moves and complete acceptance

**Files:**

- Add only `@dnd-kit/core@6.3.1`, `@dnd-kit/sortable@10.0.0`, `@dnd-kit/utilities@3.2.2` to `apps/web/package.json`/`pnpm-lock.yaml`.
- Create `apps/web/src/components/reui/kanban.tsx` and an adjacent `kanban.LICENSE` retaining the upstream MIT text; create `components/tasks/task-board.tsx`.
- Extend `lib/task-planning.ts`/test, `components/tasks/use-task-planning.ts`, `pages/tasks.tsx`, `tests/browser/tasks.spec.ts`, and `task-planning.spec.ts`.
- Update `docs/development/console-setup.md`, `ui-components.md`, `control-mcp.md`, and the focused design/parent roadmap status.

**Interfaces:**

- Consume the [ReUI Radix primitive](https://raw.githubusercontent.com/keenthemes/reui/main/registry-reui/bases/radix/reui/kanban.tsx) in documented commit-only `onMove` mode; record source URL, retrieval revision/hash and local import changes with the copy. Existing `radix-ui` and `@/lib/utils` supply Radix/cn integration.
- `TaskBoard({page,pending,onMove,onOpenTask,onLoadMore,loadingGroups})` consumes the same generated Page as List. `onMove` accepts `TaskMove` plus Task ID, never the whole board.
- Export `taskDropIntent(page:TaskPlanningPage,taskId:string,targetStatus:Task['status'],targetIndex:number):TaskMove|null`. Remove the active card from the visible target list before resolving index; choose before next visible card, otherwise after previous visible card, otherwise end. Copy both revisions from the captured snapshot/Task. Return null for an unchanged placement or invalid target. The server resolves hidden/unloaded neighbors.
- Ordinary controls send start/end/up/down or target-status end intents through the same mutation. Default five status order is `open,ready,in_progress,in_review,completed`; explicit cancelled filter uses that group without enabling column reorder.

- [x] **Step 1: Add red acceptance cases.** Pin the helper with visible `[A,B]` hiding H in full `[A,H,B]`, same-stage indexes after removal, empty stage and after-last-loaded card. Browser cases cover pointer and keyboard cross-stage moves; Move to/up/down without drag; touch scrolling with a dedicated handle; Escape/outside drop no HTTP mutation; one drop exactly one move POST; stale revision/error keeps canonical placement and permits explicit retry; filter and >50-card paging with hidden/unloaded neighbors; switching List/Board preserves selected detail/search; no planning action creates/cancels a Run. In the real API test, reload after move, race two revisions, and confirm persisted canonical order rather than trusting optimistic mocks.
- [x] **Step 2: Observe red before adopting the primitive.** Run the focused helper tests and tasks browser cases. Missing Board/actions are expected; dependency/startup problems are not the intended failure.
- [x] **Step 3: Implement the Board and non-drag paths.** Add the pinned dependencies and MIT source, adapting only its local imports/styles as required. Compose fixed columns without column handles, separate Task links and labeled card handles, explicit counts/Load more, accessible announcements, and internal horizontal scrolling. Wire commit-only onMove; never write during hover. Freeze the gesture's revision snapshot, allow one pending mutation, lock Project/collection changes while pending, and refetch canonical state on settle. On a newer collection revision cancel stale gesture/page state rather than merging generations. Keep source-managed/active-run field restrictions in detail; stage movement remains allowed. Enable the Board tab only with working interaction paths.
- [x] **Step 4: Update guidance.** Explain Save versus Start Run, manual stages and cancelled filtering, shared views, preferences versus actual Run Agent, editing locks, conflict recovery, keyboard/manual ordering, and new MCP examples including null clears. Document the third-party source/provenance and that Gantt and automatic scheduling remain future work.
- [x] **Step 5: Run final checks and inspect the UI.** Use a disposable database and existing fake providers/workers. Required commands:

  ```bash
  corepack pnpm contracts:check
  corepack pnpm typecheck
  corepack pnpm test
  corepack pnpm build
  go test -race ./internal/migrate ./internal/postgres ./internal/httpapi ./internal/integrations ./internal/controlmcp
  go vet ./internal/postgres ./internal/httpapi ./internal/controlmcp
  go build ./...
  corepack pnpm exec playwright test --config playwright.tasks.config.ts
  corepack pnpm exec playwright test --config playwright.console.config.ts
  corepack pnpm exec playwright test tests/browser/task-planning.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts tests/browser/integrations.spec.ts tests/browser/linear-agent.spec.ts tests/browser/linear-updates.spec.ts tests/browser/pr-review-execution.spec.ts tests/browser/mcp.spec.ts
  corepack pnpm exec playwright test --config playwright.docs.config.ts
  corepack pnpm exec playwright test --config playwright.github-delivery.config.ts
  corepack pnpm exec playwright test --config playwright.pr-reviews.config.ts
  ```

  Keep `tasks.spec.ts` on its mock-only config and `task-planning.spec.ts` on the real stack; do not silently skip database cases. Inspect 390/1440 px screenshots of long-name Board/List/detail, cancelled filter, missing preference, and failed move/conflict. Check keyboard focus, screen-reader announcements and browser page errors. If a fixture prerequisite is missing, resolve it or report it accurately; a skipped suite is not a passing verification. Do not rerun unchanged green suites without a new reason.
- [x] **Step 6: Review and commit this slice** as `feat: add accessible task kanban`. Have one fresh reviewer inspect the whole feature against the spec and Review Focus; reproduce/fix important findings and run relevant regressions. Transfer only verified deltas to the original workspace if isolated by snapshot. Do not merge, push, or deploy as part of this plan.

## Self-review and handoff

The four tasks cover storage, existing creation paths, content/Run races, bounded grouped reads, HTTP/MCP compatibility, Save/launch identity, details/history, Board ordering, ordinary accessibility controls, and final regression evidence. Every Review Focus case has named assertions. Signatures and DTO names above are the shared interfaces; OpenAPI is authoritative for wire types.

Native execution is recommended, as in phase 1: implement each task in this session, with one fresh final reviewer. The tasks share revision, placement, collection, and launcher contracts. Implementation and one fresh whole-feature review are complete. Both stale-data findings and the ineffective detail move control were reproduced and fixed; relevant suites passed again.


## Completion evidence

Implemented in an isolated snapshot of the original dirty workspace; only feature
deltas are transferred back, after checking baseline hashes. Existing unrelated
changes are preserved. No merge, push or deployment is part of this delivery.

- Contract generation check, TypeScript checking, 36 frontend unit tests and production build passed.
- PostgreSQL-backed race suites passed for migrate, postgres, httpapi, integrations and controlmcp; `go vet` and `go build ./...` passed.
- Browser scenarios verified: Task planning 24, console 19, real API/fake worker 17, docs 7, GitHub delivery 5 and PR reviews 18. Two real-stack fixtures needed updates (MCP read-only count and pointer activation distance); their focused reruns passed. The three real Task planning cases passed again after review fixes. No required database cases were skipped.
- Inspected 390 px and 1440 px screenshots for Board, List, details, cancelled filtering, unavailable preference and failed moves. Long names remain contained; keyboard focus, live announcements, pointer/touch cancellation and browser-error checks passed.
- One fresh reviewer found two Important stale-data issues: saved launchers retained old inputs, and loaded tails retained obsolete Run summaries. Both have RED→GREEN browser regressions. The enabled-but-ineffective detail move control was treated as a behavior bug and also fixed with a regression. No review finding remains deferred.

### Implementation rulings

- TaskPlanningCapabilities is a non-locking shared read predicate; only writes acquire Project→Task locks and recheck it — detail reads must work in read-only transactions — incorrect use by a future writer could permit a race, so writers own locking explicitly.
- TaskCreate TypeScript alias makes only priority optional despite the generator's default-to-required behavior — preserves existing creation callers and matches the actual optional API field — incorrect optionality would be caught by HTTP create tests; no change to generated contracts.
- ReUI commit-mode collision behavior is locally adapted to preserve column hits and cancel outside drops, and activator attributes/ref move to the handle — upstream sticky targeting otherwise violates the accepted cancellation/accessibility behavior — source hash and MIT license retained in ui-components.md and kanban.LICENSE.
- ordinary Run historical input snapshots — retain existing behavior and label current Task context, as the approved scope specifies — older ordinary Runs may show subsequently edited Task context; immutable snapshots require a separate change.
- automatic workflow, scheduling, provider synchronization and Gantt — remain outside this approved increment; stages are manual — users cannot schedule or synchronize Task stages yet.
- Agent deletion — no deletion API introduced; retained the deferred scope FK and Project cascade tests — a future deletion API must define preference handling.
- accidental snapshot node_modules symlinks — exclude tooling artifacts from product commits/transfer and install pinned dependencies in the original workspace — local dependencies must be installed for Kanban to run.
