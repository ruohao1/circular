# Console Foundation and Run Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Circular a Project-aware console where people can identify active work, inspect failures and incoming requests, and start a Task through a compact launcher.

**Architecture:** Add a bounded Run summary read endpoint and an optional request-attention filter; retain the execution and launch APIs. Extract the shared shell and launcher from `main.tsx`, then build Overview and Runs from shared query/table components. Keep the existing Run detail and integration actions in place.

**Tech Stack:** Go, PostgreSQL/pgx, OpenAPI with generated TypeScript, React 19, Vite, TanStack Router/Query, Tailwind 4, shadcn/Radix Nova, Vitest, Playwright.

**Spec:** [Console foundation design](../specs/2026-10-03-console-foundation-design.md), scoped from [the phased product design](../specs/2026-10-03-console-and-task-planning-design.md).

## Global Constraints

- Use React 19, Vite, TanStack Router/Query, Tailwind 4, and the existing shadcn/Radix Nova components; add no frontend dependency for phase 1.
- Use the domain names Project, Repository, Agent, Task, Run, and Workspace consistently; a Task and its Run attempts remain distinct.
- Keep `/runs/:runId`, `/?taskId=…`, `/setup` search parameters and OAuth callbacks, `/requests/:requestID`, and `/docs` working.
- Preserve existing Run detail, cancellation, event replay, Artifacts, GitHub/Linear delivery, and PR review behavior.
- Preserve existing Task and Run read/list response shapes; add a dedicated Run queue endpoint without modifying `RunRead` or introducing a migration.
- Starting execution requires the existing explicit Start Run action; navigation, filtering, and opening a dialog never start, retry, approve, or publish work.
- Use the current theme tokens and locally bundled Geist font; no paid component source or external font request is required.
- Do not stage or overwrite unrelated work already present in the workspace.

## Review Focus

- Delayed Project A responses after switching to Project B must not show A's data or apply its draft in B; exercise this in Tasks 2 and 3.
- Imported/discovery Tasks and Task-created/Run-failed launches must retain their saved identity, Project, and Agent through dialog dismissal/reopening; exercise this in Task 2.
- Equal timestamps, multiple attempts, literal search characters, and Run state transitions must not duplicate rows or create a cross-Project cursor; exercise this in Tasks 1 and 3.
- An endpoint error must stay distinguishable from an empty collection, and an older approval must not disappear behind newer completed requests; exercise this in Tasks 1 and 4.
- Long names, keyboard-only interaction, narrow screens, and unassigned requests must keep both navigation and destination context usable; exercise this in Tasks 2–4.

## Working baseline and file boundaries

The workspace already contains substantial tracked and untracked implementation work. Before execution, record `git status --short` and establish an isolated baseline containing that work; a worktree from HEAD alone would omit code this plan depends on. Follow the worktree skill at execution time and retain the baseline without staging another person's edits. Each task's commit step means only that task's changes, after its checks pass.

Read `CONTEXT.md`, the focused spec, `apps/web/components.json`, and `docs/development/ui-components.md` before implementing. No dependencies, product code, database mutations, or commits were made while preparing this plan.

| Boundary | Responsibility |
| --- | --- |
| `internal/httpapi/run_queue.go` | Validate and serve the dedicated summary read endpoint using existing HTTP/query helpers |
| Existing external request HTTP/service files | Apply the optional attention filter inside the existing scope and pagination rules |
| `apps/web/src/components/console/shell.tsx` | Shared navigation, Project control, global launcher trigger, shell states |
| `apps/web/src/components/console/task-launcher-context.tsx` | Draft ownership, imported Task opening, launch/retry orchestration |
| `apps/web/src/components/console/task-launcher.tsx` | Accessible dialog and existing launch form |
| `apps/web/src/components/console/run-queue.tsx` | Reusable named Run rows and queue state presentation |
| `apps/web/src/components/console/run-duration.tsx` | Display elapsed execution time from actual Run timestamps |
| `apps/web/src/components/console/request-attention.tsx` | Bounded Project/unrouted request sections using existing review links |
| `apps/web/src/lib/run-queue.ts` | URL validation, query keys, polling policy, and duration transformation |
| `apps/web/src/pages/runs.tsx`, `overview.tsx` | Compose the two destination screens without duplicating row/launcher logic |

Keep routing/bootstrap and the current Run detail in `main.tsx`. Do not turn this into a whole-app extraction.

### Task 1: Serve named, paginated Run summaries and filtered request attention

**Files:**

- Create: `internal/httpapi/run_queue.go`, `internal/httpapi/run_queue_test.go`
- Modify: `internal/httpapi/api.go`, `internal/httpapi/external_requests.go`, `internal/integrations/linear_requests.go`
- Modify/test: `internal/httpapi/external_requests_test.go`
- Modify: `contracts/openapi.json`, `apps/web/src/generated/api.ts`, `apps/web/src/api.ts`

**Interfaces:**

- Produces: `(a *api) runQueue(w http.ResponseWriter, r *http.Request)` registered as `GET /api/v1/projects/{project_id}/run-queue`.
- Produces OpenAPI: `RunQueueGroup = all | active | failed | finished`; `RunQueueRepository { id: UUID, name: string }`; `RunQueueItem { run: RunRead, task_title: string, agent_name: string, repository: RunQueueRepository | null }`; `RunQueuePage { items: RunQueueItem[], next_cursor: string }`. All object properties are required.
- Produces frontend exports: generated aliases `RunQueueGroup`, `RunQueueItem`, `RunQueuePage`; `RunQueueQuery { group?: RunQueueGroup; q?: string; cursor?: string; limit?: number }`; `api.runQueue(project: string, query?: RunQueueQuery): Promise<RunQueuePage>`.
- Extends: `integrations.RequestListQuery` with `Attention bool`. Preserve `ExternalRequests(ctx context.Context, q RequestListQuery) (RequestPage, error)`.
- Extends frontend: `api.externalRequests(project: string, unrouted = false, cursor = "", options: { attention?: boolean; limit?: number } = {})`. Keep the same `Promise<ExternalRequestPage>` and three-argument callers working; export the generated `ExternalRequestPage` alias.

- [x] **Step 1: Write HTTP tests for the endpoint and attention predicate.** Use the existing `setup`, `fixture.create`, and `fixture.request` helpers and the existing external-request fixture pattern. The following named cases pin the behavior:

  | Test | Assertions |
  | --- | --- |
  | `TestRunQueueProjectSummaries` | Project A returns only A's Runs with actual Task/Agent names, nullable Repository, distinct attempts, and unchanged nested `RunRead` fields; B's records are absent |
  | `TestRunQueueGroupsAndLiteralSearch` | Seed all nine `RunStatus` values; All=9, Active=6, Failed=1, Finished=3; trimmed mixed-case titles match; `%` and `_` match literal characters |
  | `TestRunQueuePagination` | Three Runs with equal creation times and `limit=1` traverse once each in descending UUID order; the terminal cursor is empty; a newly inserted newer Run appears after restarting at newest |
  | `TestRunQueueValidationWithoutDatabase` | Malformed Project UUID, group, limit, cursor, or overlong search/cursor receives 422 using an unreachable database pool, proving syntax validation runs first |
  | `TestRunQueueCursorScopeAndMissingProject` | Reusing a cursor in another Project/group/search gives 422; unknown Project gives 404; empty known Project returns `items: []`; responses are `no-store` |
  | `TestExternalRequestAttentionPagination` | More than 20 newer completed requests do not hide an older approval; only the three attention statuses match; Project and unrouted scopes stay separate; a cross-scope cursor gives 422 |
  | `TestExternalRequestAttentionCompatibility` | Omitted/false attention retains existing listing; true filters before limit; invalid boolean gives 422; terminal pagination and existing private-request guard are preserved |

  For validation, include these exact query vectors in a Go table:

  ```go
  {"group=unknown", 422}, {"limit=0", 422}, {"limit=101", 422},
  {"limit=1.5", 422}, {"cursor=not-a-cursor", 422},
  // attention endpoint:
  {"unrouted=true&attention=yes", 422},
  ```

- [x] **Step 2: Run the new tests and confirm the intended failures.** With a disposable `TEST_DATABASE_URL`, run `go test ./internal/httpapi -run 'TestRunQueue|TestExternalRequestAttention' -count=1 -v`. The missing endpoint/filter must fail; a database skip or connectivity failure is not the expected red result.
- [x] **Step 3: Implement the read behavior and contract.** Validate query syntax first, then Project existence. Use `projection("RunRead", "r")` inside an explicit JSON summary, parameterized joins/search, `(r.created_at,r.id)` ordering, and `limit+1`. Use `strpos(lower(t.title), lower($query)) > 0` for literal substring matching. Keep cursor helpers private to `run_queue.go`; encode version 1, Project, normalized group/query, `created_at`, and Run ID with URL-safe base64 JSON. Enforce the spec's limits, status sets, nulls, and empty-page envelope. Extend the existing request SQL with the attention predicate before limit and scope its cursor lookup without requiring the cursor row to retain its old status. Add the OpenAPI operation, responses, enums, and optional attention parameter; do not put join-only fields in `RunRead`, whose properties map directly to table columns.
- [x] **Step 4: Generate the types and add the API wrappers above.** Run `corepack pnpm contracts:generate`; keep query serialization in `api.ts` and use generated schema aliases rather than a parallel handwritten response model.
- [x] **Step 5: Verify the contract and backend slice.** Run `go test ./internal/httpapi ./internal/integrations -count=1`, `corepack pnpm contracts:check`, and `corepack pnpm typecheck`. Expect no failed tests, no contract drift, and no TypeScript errors; database-dependent tests must actually execute. Confirm existing `/runs` and `/tasks` still return their original arrays.
- [x] **Step 6: Commit only this slice** as `feat: add project run queue summaries` after inspecting the diff against the preserved working baseline.

### Task 2: Put Project context and the compact launcher in the shell

**Files:**

- Create: `apps/web/src/components/console/shell.tsx`, `apps/web/src/components/console/task-launcher-context.tsx`, `apps/web/src/components/console/task-launcher.tsx`
- Modify: `apps/web/src/use-project.tsx`, `apps/web/src/main.tsx`, `apps/web/src/pages/setup.tsx`, `apps/web/src/pages/request.tsx`
- Reuse without changing launch semantics: `apps/web/src/launch.ts`, `apps/web/src/components/ui/dialog.tsx`, `apps/web/src/components/resource-select.tsx`
- Create/test: `tests/browser/console.spec.ts`
- Modify/test: `tests/browser/execution.spec.ts`, `tests/browser/setup.spec.ts`

**Interfaces:**

- Consumes: existing `useProject()`, `launchTask(selection, client?)`, `LaunchError.taskId`, `api.task`, `api.createRun`, and generated Task/Run types.
- Extends `useProject()`: `selectionLocked: boolean` and `acquireSelectionLock(): () => void`; keep `selectProject(id: string): void` and existing fields. Both functions have stable identities. The lock disables user selection; it does not prevent deliberate imported/deep-link scope alignment.
- Produces: `useProjectSelectionLock(locked: boolean): void` in `use-project.tsx`, with effect cleanup and independent ownership so one consumer cannot release another's lock.
- Produces: `ConsoleShell({ children }: { children: React.ReactNode }): React.JSX.Element`.
- Produces: `TaskLauncherProvider({ children }: { children: React.ReactNode }): React.JSX.Element` and `useTaskLauncher(): { openNewTask(): void; openImportedTask(taskId: string): void }` in the context file. The provider owns the rendered dialog and launch state.
- Produces dialog contract: exported `TaskLauncherDialog(props: TaskLauncherDialogProps): React.JSX.Element`; define the typed form/view model alongside it and keep API mutation/navigation ownership in the provider. The provider is the sole consumer of that rendering contract.
- Root composition: `ProjectProvider → TaskLauncherProvider → ConsoleShell → Outlet`; Docs keeps its independent layout.

- [x] **Step 1: Add browser cases for Project context and launcher recovery.** Seed isolated Projects through the existing test API; intercept only the individual responses needed for delay/failure cases. Add these named tests to `console.spec.ts`:

  | Test | Required assertions |
  | --- | --- |
  | `Project context survives navigation and storage failure` | Exactly one visible Project combobox on Overview/Setup/Requests; selection survives navigation and reload when storage works; unavailable storage does not crash |
  | `detail scope follows the record` | Deep-linked Run/assigned request selects its actual Project; a user Project change returns to Overview; unassigned request stays open, changes routing scope, and clears stale route selection |
  | `Setup and request saves hold their Project` | Delayed mutations disable the global picker; error/success releases it; existing callbacks choose their intended Project |
  | `launcher retains drafts by Project` | Close/reopen retains draft A; switching to B cannot display/use A's Repository, Agent, or partial Task; return to A resumes A |
  | `partial launch retries the saved Task` | Force first `POST /runs` to fail after Task creation; close/reopen and retry; exactly one `POST /tasks`, same Task/Agent in retry, and no duplicate submit while pending |
  | `imported and discovery links open the launcher` | `/?taskId=…` opens automatically in the correct Project, keeps imported fields read-only, honors the suggested Agent, and creates no Task; invalid ID cannot launch; closing clears `taskId` |
  | `launcher and navigation work with keyboard on mobile` | At 390 px, focus is contained and restored; Escape/Close work unless launch is pending; long names and dialog controls remain accessible without page overflow |

  Use exact public labels in assertions, for example:

  ```ts
  await page.getByRole("button", { name: "New Task", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByLabel("Task title", { exact: true })).toHaveValue("Draft A");
  // After the injected createRun failure and reopening:
  await page.getByRole("button", { name: "Retry starting Run", exact: true }).click();
  expect(taskPosts).toHaveLength(1);
  expect(runPosts[1]).toMatchObject({ task_id: savedTask.id, agent_id: agent.id });
  ```

- [x] **Step 2: Run the new browser cases and confirm missing-dialog/shell failures.** Run `corepack pnpm exec playwright test tests/browser/console.spec.ts` with the repository's disposable browser fixture database configured.
- [x] **Step 3: Implement the shell and Project ownership.** Extract current navigation/layout; add the shared Project picker, New Task trigger, mobile navigation dialog, and Project loading/error/empty states. Preserve current destinations until Task 3 adds `/runs`. Remove the Setup duplicate picker but keep its existing mutation locks. Align loaded detail scope without redirect loops; keep unassigned request routing usable and clear its selected route when the routing Project changes. Do not change the request fingerprint/review checks. Use an owned-lock cleanup hook, not one shared boolean that components overwrite.
- [x] **Step 4: Extract the existing form and implement the dialog provider.** Move existing validation/defaults/import/discovery behavior, not a rewritten launch API. Keep draft records keyed by Project plus imported Task identity above dialog visibility; preserve partial Task/Agent on close and prevent dismissal during a pending request. Handle rapid imported-Task changes so a late response cannot reopen the wrong Task. On “Start another Task”, retain the saved Task's resume link and clear only that draft. On success invalidate `['run-queue', projectId]`, clear the successful draft, and navigate to `/runs/$runId`. Replace the root's always-visible form with this launcher, leaving its existing recent list until Task 4.
- [x] **Step 5: Update affected browser flows and verify them.** Ordinary launch scenarios in `execution.spec.ts` and `setup.spec.ts` explicitly click New Task before choosing Repository/Agent. Imported/discovery flows keep automatic opening and existing readonly checks. Run `corepack pnpm test`, `corepack pnpm typecheck`, and `corepack pnpm exec playwright test tests/browser/console.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts tests/browser/integrations.spec.ts tests/browser/linear-updates.spec.ts tests/browser/linear-agent.spec.ts`. Expect real success/failure/cancellation, import, review, callback, and keyboard assertions to pass; do not weaken assertions to fit a broken flow.
- [x] **Step 6: Commit only this slice** as `feat: add shared console shell and task launcher`.

### Task 3: Add the searchable Run queue

**Files:**

- Create: `apps/web/src/lib/run-queue.ts`, `apps/web/src/lib/run-queue.test.ts`
- Create: `apps/web/src/components/console/run-queue.tsx`, `apps/web/src/components/console/run-duration.tsx`, `apps/web/src/pages/runs.tsx`
- Modify: `apps/web/src/main.tsx`, `apps/web/src/components/console/shell.tsx`, `apps/web/src/pages/setup.tsx`
- Extend/test: `tests/browser/console.spec.ts`

**Interfaces:**

- Consumes: Task 1's `api.runQueue`, `RunQueueQuery`, `RunQueuePage`, `RunQueueItem`, `RunQueueGroup`; Task 2's shell and Project context.
- Produces: `RunQueueSearch { group: RunQueueGroup; q: string; cursor: string }`; `parseRunQueueSearch(search: Record<string, unknown>): RunQueueSearch`. Default invalid URL state to `all`, empty q/cursor; trim q and cap to 200 code points; reject overlong cursor from UI state.
- Produces: `runQueueOptions(project: string, query: RunQueueQuery)` returning TanStack `queryOptions<RunQueuePage>` with key `['run-queue', project, normalizedQuery]`, `enabled: !!project`, and the polling policy from the spec. No cross-Project placeholder data.
- Produces: `runDurationSeconds(run: Pick<Run, 'started_at' | 'finished_at'>, now: number): number | null`; null before start, nonnegative whole seconds otherwise. `RunDuration({ run }: { run: Run }): React.JSX.Element` handles its live display timer.
- Produces: `RunQueue({ items }: { items: RunQueueItem[] }): React.JSX.Element`, a reusable table/list with actual Run links; loading/error/empty/pagination belong to the enclosing section/page.
- Produces: `RunsPage(): React.JSX.Element` registered at `/runs`, using `useSearch({ from: '/runs' })` and the shared search parser.

- [x] **Step 1: Add tests for URL state, timing, and queue interactions.** In `lib/run-queue.test.ts`, pin these transformation cases:

  ```ts
  expect(parseRunQueueSearch({ group: "unknown", q: "  Fix_%  " }))
    .toEqual({ group: "all", q: "Fix_%", cursor: "" });
  expect(runDurationSeconds({ started_at: null, finished_at: null }, 10_000))
    .toBeNull();
  expect(runDurationSeconds({ started_at: "2026-10-03T10:00:00Z",
    finished_at: "2026-10-03T10:00:05Z" }, Date.parse("2026-10-03T11:00:00Z")))
    .toBe(5);
  ```

  Add browser tests `Run queue preserves filters in navigation`, `Run queue handles Project changes during delayed requests`, and `Run queue keeps newest and older pages separate`. Assert Task/Agent/Repository names, distinct attempts, waiting/terminal status text, `No Repository`, PR review kind, keyboard links, literal search, filter/page URL history, resetting cursor on Project/filter change, no per-row `/tasks/:id` requests, and absence of old Project rows after a delayed response. Changing All to Active must update after a Run finishes. Verify long titles at 390 px and 1440 px.
- [x] **Step 2: Confirm the tests fail for missing behavior.** Run `corepack pnpm --filter @circular/web test src/lib/run-queue.test.ts` and `corepack pnpm exec playwright test tests/browser/console.spec.ts -g 'Run queue'`.
- [x] **Step 3: Implement shared queries, duration, and rows.** Use the Task 1 DTO directly, stable keys including Project/group/q/cursor/limit, visible-document polling, and per-section errors. Derive elapsed time from start/finish only; stop terminal timers and clamp negative clock skew. Keep full long names accessible, textual statuses, secondary ID/attempt, and real anchors. Do not add metrics that require new counts or result evidence.
- [x] **Step 4: Add the Runs route and controls.** Wire URL state, 250 ms replace-history search debounce, explicit filter/page navigation, 50-row default, Older Runs/Newest Runs controls, and cursor resets. Reset an old cursor before enabling a request for a new Project/filter so the UI never sends another Project's cursor. Change shell navigation to Overview `/`, Runs `/runs`, Requests, Setup, Docs; Run detail breadcrumb/back links now target `/runs`. Leave Run detail content and actions intact. Update Setup's “Go to Runs” destination to `/runs` while retaining access to New Task in the header.
- [x] **Step 5: Verify this destination.** Run `corepack pnpm test`, `corepack pnpm typecheck`, and `corepack pnpm exec playwright test tests/browser/console.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts`. Expect all to pass, no stale cross-Project rows, and unchanged execution/detail behavior.
- [x] **Step 6: Commit only this slice** as `feat: add searchable project run queue`.

### Task 4: Compose the Overview and attention sections

**Files:**

- Create: `apps/web/src/pages/overview.tsx`, `apps/web/src/components/console/request-attention.tsx`
- Modify: `apps/web/src/main.tsx`, `docs/development/console-setup.md`, `docs/development/ui-components.md`
- Extend/test: `tests/browser/console.spec.ts`
- Reuse: `apps/web/src/components/external-request-list.tsx` status labels and existing request-detail links

**Interfaces:**

- Consumes: Task 3's `runQueueOptions` and `RunQueue`; Task 1's `api.externalRequests` fourth argument; Task 2's `useTaskLauncher().openImportedTask`.
- Produces: `ConsoleOverview({ taskId }: { taskId?: string }): React.JSX.Element`; the index route validates/forwards `taskId` and opens each changed imported identity once.
- Produces: `RequestAttention({ projectId, unrouted = false }: { projectId: string; unrouted?: boolean }): React.JSX.Element`. Query key `['external-requests', projectId, unrouted, 'attention', 5]`; request with `attention: true, limit: 5`; retain the existing invalidation prefix.

- [x] **Step 1: Add Overview browser scenarios.** Add `Overview shows execution and request attention honestly`, `Overview handles independent failures and recovery`, and `Overview preserves launcher deep links`. Seed/mock active/failed attempts, no Runs, assigned approval/access requests, unassigned routing requests, a request that ceases to need attention, a pending section, and a failed section. Assert the spec's limits (10/5/5/5), separate installation-wide label, real detail links, no inline approval, explicit initial error instead of empty-success, stale notice on background error, successful retry, and imported dialog opening only once across polling. A failure in Requests must not hide Active Runs; zero Projects must still expose Setup and unassigned requests.
- [x] **Step 2: Confirm these acceptance cases fail on the old landing page.** Run `corepack pnpm exec playwright test tests/browser/console.spec.ts -g 'Overview'` and inspect the missing-section assertions.
- [x] **Step 3: Implement the screen and attention component.** Compose Active Runs with `{ group: 'active', limit: 10 }`, Recent failures with `{ group: 'failed', limit: 5 }`, and the two filtered request sections. Each section owns its query states and retry action. Retain last data only for that exact query key; use an explicit notice for failed refresh. Requests poll every 15 seconds while visible; do not filter their first page in the browser. Remove the obsolete `RunOverview`/launcher/table code from `main.tsx`, preserving the separate Run detail. Link collection actions to `/runs` or `/requests` and retain all import search behavior.
- [x] **Step 4: Update console guidance.** Describe persistent Project selection, Overview versus Runs, opening New Task, partial-launch recovery, filtering/search, and installation-wide unassigned requests. Keep the current Radix component guidance and link to the unchanged review/delivery documentation; do not describe Kanban/Gantt as shipped.
- [x] **Step 5: Run the final checks for the whole slice.** With a disposable test database, run the commands below. All must exit 0; PostgreSQL tests must not silently skip. Inspect the 390 px/1440 px browser screenshots for Project control, long-name queue, attention errors, mobile navigation, and launch dialog, plus browser console errors. Use existing fake providers/workers; these checks do not require live provider messages or publishing.

  ```bash
  corepack pnpm contracts:check
  corepack pnpm typecheck
  corepack pnpm test
  corepack pnpm build
  go test -race ./internal/httpapi ./internal/integrations ./internal/controlmcp
  go vet ./internal/httpapi ./internal/integrations
  go build ./...
  corepack pnpm exec playwright test tests/browser/console.spec.ts tests/browser/execution.spec.ts tests/browser/setup.spec.ts tests/browser/integrations.spec.ts tests/browser/linear-updates.spec.ts tests/browser/linear-agent.spec.ts tests/browser/identity.spec.ts tests/browser/github-create.spec.ts tests/browser/pr-review-execution.spec.ts
  corepack pnpm exec playwright test --config playwright.docs.config.ts
  corepack pnpm exec playwright test --config playwright.github-delivery.config.ts
  corepack pnpm exec playwright test --config playwright.pr-reviews.config.ts
  ```

  Stop and report a missing fixture prerequisite accurately; a skipped suite is not a verified result. Do not rerun unchanged suites after a clean pass without new evidence of a problem.
- [x] **Step 6: Review the final diff and commit only this slice** as `feat: add console overview and attention sections`. Report the concrete behavior, checks actually run, and any remaining limitations. Do not deploy or merge as part of this plan.

## Handoff

Recommended execution method: native execution in this session, because the four tasks share the launch state, Project context, and Run queue interfaces. Keep each deliverable reviewable and perform a final review before integrating. Kanban/Gantt and Run review keep their separate later plans.

## Implementation record — 2026-10-03

Phase 1 is implemented and verified. The shared Project shell, Task launcher, Overview, named Run queue, and server-side request-attention filter are complete. Kanban, Gantt, and redesigned Run review remain later phases.

- Verification: 28 frontend unit tests; 259 backend tests/subtests with the race detector and no skips; 71 unique browser scenarios (19 console, 22 real execution/Setup/integration, 7 docs, 5 GitHub delivery, 18 PR review). Contracts, TypeScript, Go vet, and frontend/backend builds passed. The frontend build emits a non-blocking chunk-size advisory.
- Desktop and mobile screenshots were inspected at 1440 px and 390 px, including long queue names, launcher focus, and stale request errors. Tables scroll inside their own surfaces.
- Final review found a historical cursor scope bug. A failing test reproduced Project A pagination → Project B → navigation away/back; pagination now records its Project in browser history and reads URL/search scope from one location snapshot. All seven queue browser cases passed after the fix. No review findings remain unresolved.
- Other observed regressions fixed before delivery: imported Repository selection after reload, focus restoration for automatically opened Tasks, and in-dialog retry when Project loading fails.

Execution adjustments: protected original Git metadata required an isolated source snapshot that included the existing dirty workspace. Backend changes were committed separately; frontend Tasks 2–4 were combined to keep their shared root composition and destination imports coherent. Deterministic UI delay/error cases use `playwright.console.config.ts`; existing real-stack suites verify execution and integrations. External-request compatibility cases share the attention test fixture. The Run page uses the shared search parser with `useRouterState` instead of separate `useSearch` and history subscriptions, preventing mixed navigation snapshots. Only changed files are returned after comparing the originals with the snapshot. No merge, push, or deployment is part of this work.

## Integration verification — 2026-10-05

The original phase 1 commits were extracted onto merged main `7882d79`, preserving the later execution, integration, and reviewer Git fixes. Task planning, Kanban, and scheduling implementations are excluded. The console browser configuration now keeps artifacts under `test-results/console`.

Validation passed: 28 frontend unit tests; generated contracts, TypeScript, and the frontend build; 308 backend tests/subtests with the race detector and no skips across HTTP, integrations, and control MCP; Go vet and build across all packages; and all 75 browser scenarios with no skips or flaky results. The 19 console scenarios also passed with local fonts available, and their desktop/mobile screenshots were inspected. All execution and provider checks used disposable local fixtures.

Independent review found no spec gaps or blocking standards issues. Component imports were aligned with the documented `@/` alias convention. The optional suggestion to share the small stale/error/retry presentation was deferred; the sections retain independent query state. Merge and deployment remain separate from this PR preparation.
