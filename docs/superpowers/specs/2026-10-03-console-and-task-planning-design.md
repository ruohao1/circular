# Circular console and Task planning: design draft

Status: initial phased design. The focused phase 1 console foundation is implemented; later phases remain outside its PR. The original planning choices below provide historical context.

Phase 1 now has a [focused console design](2026-10-03-console-foundation-design.md) and [implementation plan](../plans/2026-10-03-console-foundation.md). These make the console slice concrete; later planning views retain the open choices recorded below.

## Purpose and assumptions

Make Circular useful throughout engineering work: plan Tasks, monitor execution, inspect results, and decide what happens next. Preserve the distinction between a durable Task and each execution Run.

The user requested UI/UX references, liked the shortlist of shadcn, ReUI, AI Elements, and Pierre Diffs, then requested a plan and suggested Kanban and Gantt views. Kanban belongs in scope. This draft interprets Gantt as planning Task dates and dependencies; whether the user also wants an execution timeline remains an open product choice.

Success means a person can identify the work needing attention, find a Task across views, inspect its attempts, and review its output without losing Project context.

## What exists today

- `apps/web/components.json` selects `radix-nova`. The app uses React 19, Tailwind 4, Vite, TanStack Router/Query, and a Go/PostgreSQL backend.
- `apps/web/src/main.tsx` contains the application shell, a large Task-and-Run launcher, a recent Runs table centered on IDs, and Run detail views for output, changes, and events.
- `apps/web/src/use-project.tsx` already retains the selected Project. The selection control is currently repeated within individual pages.
- `apps/web/src/components/external-request-list.tsx` and `apps/web/src/pages/request.tsx` support incoming requests, including routing and review before starting work.
- `contracts/openapi.json` defines Task statuses `open`, `in_progress`, `completed`, and `cancelled`. Tasks have no planning dates, preferred Agent, priority, dependencies, saved board order, or edit revision.
- The HTTP API lists, creates, and reads Tasks, but has no Task editing endpoint. The frontend does not yet expose a Tasks collection view.
- `apps/web/src/launch.ts` creates a Task and then starts its Run. Planning requires a separate Save Task action while retaining the existing direct-launch flow.
- `internal/postgres/run_launch.go` creates attempts without advancing Task workflow state. Historical Run success must not be treated as proof that a person accepted the Task result.

## Recommended product structure

Use persistent Project navigation with these destinations:

| Destination | Purpose |
| --- | --- |
| Overview | Work needing attention and active Runs, with a compact New Task entry point |
| Tasks | One collection with List, Board, and Timeline views |
| Runs | Execution attempts, status, duration, Agent, and Task context |
| Requests | Incoming delegated work, routing, and existing approval actions |
| Agents | Agent capabilities and configuration, with links into existing setup |
| Settings and Docs | Project configuration, Repositories, integrations, MCP, and guidance |

Retain existing URLs and integration callback behavior. Moving Agents into primary navigation can initially link to the existing Setup section. Deep links into Runs remain valid.

All Task views use the same records, mutations, filters, and Task detail surface. A view switch preserves the selected Project, search, filters, and selected Task. Store shareable view state in the URL; retain optional display preferences locally.

## Kanban design

- Each card represents one Task. Its attempts are visible in Task details.
- Proposed columns: Backlog, Ready, In progress, Review, Done. Preserve the existing wire values `open`, `in_progress`, and `completed`; add `ready` and `in_review` only when this workflow is accepted. Cancelled Tasks remain accessible through a filter.
- Show title, Repository, priority, preferred Agent, and a separate execution-status indicator. A preferred Agent is a planning default; every Run still records the Agent actually selected.
- Workflow stages are manually managed initially. Show completed Runs needing review as an attention signal; do not silently mark Tasks Done or overwrite their stage when an older attempt finishes.
- Dragging a card changes its stage and saved ordering. It does not start, retry, cancel, publish, or approve a Run.
- Provide a Move to action and ordering controls usable without dragging. Support keyboard and touch interaction.
- Persist a move atomically and reconcile the server response. On a failed save, restore the previous card placement and explain the failure. On a stale edit, refresh rather than overwrite another person's change.
- Preserve ordering when filters hide neighboring cards. The backend determines ordering within the complete Project and stage, rather than relying on a filtered array index.

## Gantt design

Treat Timeline as a view of planned Task dates:

- Optional planned start and finish dates, initially at calendar-day precision. Tasks without a complete date range remain visible in an Unscheduled list and can be scheduled explicitly.
- Day/week/month navigation, a Today control, and Task bars that open the shared Task details.
- Start with read-only bars plus a date editor, then add drag and resize once saving and conflict handling are verified. Keyboard users retain the date editor.
- Store calendar dates as dates, not midnight UTC timestamps. Define the inclusive finish date at the API boundary and adapt it to the chosen component's end-date convention.
- A one-day Task is valid. A finish before the start is rejected. Clearing a schedule explicitly returns the Task to Unscheduled.
- Dependencies reference Tasks in the same Project. Reject self-links, duplicates, and cycles on the server, including cycles created by concurrent edits.
- In the initial planning scope, dependency links express intended ordering and highlight conflicting dates. They do not automatically start Runs or reschedule other Tasks. Execution dependency gates and automatic scheduling require a separate design.
- Keep dependency labels available in Task details even if the chart renderer needs an additional dependency-arrow layer.
- Do not invent percent-complete figures from elapsed Run time or generated tokens.

An execution timeline is a separate possible view: actual Run start/finish times, concurrent attempts, and recorded waiting periods. It uses timestamped execution facts and has no editable scheduling bars. If requested, plan it alongside the Runs work rather than substitute it for the Task Gantt.

## Run queue and review

- Use Task titles as primary labels. Keep Run IDs, attempt numbers, backend, and timestamps secondary.
- Show attention reasons explicitly: failed execution, requests requiring routing or approval, and captured changes available for review. Use supported API states; avoid inferring approval readiness from an unrelated status.
- Keep the request review flow's existing context and action checks when linking it from Overview.
- Run detail starts with a clear outcome and preserves access to the full report, Changes, Activity, Artifacts, GitHub delivery, Linear delivery, and existing PR reviews.
- Evaluate Pierre Diffs for file-oriented unified/split views. Handle binary files, renames, empty changes, and large patches; preserve raw patch download.
- Summaries remain attributable to the Agent. A Checks view requires recorded command/test evidence; an Agent's prose claim is not a verified result. Add such a view only where that data is available.
- Failed Runs open useful failure context; changing presentation does not mutate execution history.

## Components

| Area | Proposed choice | Evaluation required |
| --- | --- | --- |
| Foundation and navigation | Existing shadcn/Radix Nova | Preserve tokens, routing, focus behavior, and mobile navigation |
| Run queue and detail layout | ReUI examples as references; free primitives where useful | Choose Radix variants; paid Pro source is an optional acquisition, not a dependency of the plan |
| Board and Gantt | Evaluate ReUI first, Kibo second | Vite integration, keyboard interaction, controlled updates, date semantics, dependency rendering, and realistic data volume |
| Code review | Pierre Diffs | Patch formats, large-file behavior, theming, and download fallback |
| Agent activity controls | Selected AI Elements patterns/components | Documentation assumes Next.js and AI SDK; inspect individual dependencies and adapt Circular Events explicitly |

A bounded component evaluation precedes adoption. Keep Circular's Task and Run models outside component-specific types so a renderer can be changed without changing the API.

## Delivery sequence

Each phase should produce a useful increment and receive its own implementation plan once its design is settled.

1. **Console foundation and Run queue.** Put Project context in the shell, make task names visible, add a compact launcher, preserve existing deep links, and build the attention view from existing request and execution states. Include lookup APIs needed to avoid one request per row.
2. **Run review.** Add file navigation and Pierre Diffs, improve outcome hierarchy, group readable activity, and preserve all current delivery/review actions. Keep raw events available for diagnosis.
3. **Task planning and Kanban.** Introduce Task editing, workflow stages, priority, preferred Agent, revision checks, and persisted ordering. Ship Save Task, a shared Task detail surface, List and Board together. Extend OpenAPI, generated TypeScript, HTTP behavior, and relevant MCP Task operations consistently.
4. **Task schedules and Gantt.** Add optional date ranges and dependencies, then Timeline over the same collection. Ship date editing and validation before drag scheduling. Add cycle protection, unscheduled Tasks, and clear date-conflict feedback.
5. **Navigation and onboarding polish.** Add contextual keyboard actions, richer Agent navigation, setup readiness guidance, responsive refinements, and documentation for the complete workflow.

If planning is the immediate priority, phases 2 and 3 can exchange order. Gantt depends on the shared Task planning foundation; it should not block the first console improvements.

## Implementation boundaries to carry into the detailed plans

- Extract shell, overview, Runs list, and Run detail responsibilities from `apps/web/src/main.tsx` as those areas change. Do not undertake an unrelated whole-app rewrite.
- Add Task feature components and query/mutation helpers under a cohesive frontend feature directory, consumed by List, Board, Timeline, and Task details.
- Add focused Task planning handlers and persistence code rather than extending generic resource creation with board-specific behavior. Candidate boundaries are `internal/httpapi/task_planning.go` and `internal/postgres/task_planning.go`.
- Preserve existing Task GET/list response shapes; use additive fields and a dedicated planning collection endpoint if pagination or aggregate summaries require a different envelope.
- Keep Task revisions and collection ordering concurrency distinct. Define server-owned placement operations using neighboring Task IDs or an equivalent stable scheme; clients must not submit the whole board as an overwrite.
- Use forward-only migrations. Existing Tasks preserve their saved workflow state and receive neutral planning defaults. Do not infer Done from successful historical Runs.
- Keep imported provider identity and instructions intact. Planning state is local to Circular initially; editing dates or moving a card does not publish changes to Linear or GitHub.
- Update schema validation deliberately for new date, enum, revision, and relation fields. The existing generic HTTP decoder alone does not provide all required planning validation.

## Acceptance and verification

- A Task has the same title, state, and identity in List, Board, Timeline, and its Run history.
- Users can create and save a Task without triggering execution. Existing direct-launch and retry flows retain their behavior.
- Board movement works with a pointer, keyboard, and a non-drag menu. Reloading preserves ordering; concurrent stale edits do not erase newer changes.
- Switching Projects or changing filters during a mutation cannot apply its result to the wrong collection.
- Unscheduled, one-day, multi-day, and invalid schedules behave consistently around timezone and daylight-saving boundaries.
- Dependency mutations reject invalid relations and concurrent cycles. Historical Task data survives migration.
- The UI keeps planning state, Run success/failure, Workspace cleanup, and external delivery status distinct.
- Review remains usable for failed Runs, no-change Runs, binary patches, renamed files, and large diffs.
- API/contract checks, Go persistence and HTTP integration tests, frontend tests for transformation/mutation behavior, and Playwright scenarios cover the above. Use fake execution fixtures for planning and UI acceptance.

## Product choices for review

The draft recommends List and Kanban as the first planning release, followed by Gantt for planned Task dates and dependency visibility. Full planning-and-execution scheduling together would have broader backend scope and delay the first usable release. If the real need is observing parallel Agent activity, prioritize an execution timeline instead of adding planning dates.

The pending question is whether Gantt means Task scheduling, actual Run timing, or both as distinct views. The proposed workflow columns and manual stage changes should also be reviewed before detailed API work is planned.

## References

- [ReUI Run queue](https://reui.io/blocks/solutions/agents/solution-agents-2)
- [ReUI Run detail](https://reui.io/blocks/solutions/agents/solution-agents-3)
- [ReUI Kanban](https://reui.io/components/kanban): drag-and-drop and keyboard interaction; free primitives and separate Pro blocks.
- [ReUI Gantt](https://reui.io/components/gantt): controlled scheduling, date scales, validation callbacks, Radix/Base UI variants, and examples including dependency arrows.
- [Kibo Kanban](https://www.kibo-ui.com/components/kanban)
- [Kibo Gantt](https://www.kibo-ui.com/components/gantt): documented draggable/resizable items, markers, grouping, and multiple items per row; dependency behavior still needs evaluation.
- [Pierre Diffs](https://diffs.com/)
- [AI Elements setup](https://elements.ai-sdk.dev/docs/setup)
- [AI Elements IDE example](https://elements.ai-sdk.dev/examples/ide)
