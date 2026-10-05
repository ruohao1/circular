# Circular console foundation and Run queue

Status: phase 1 implemented and verified on 2026-10-03. See the implementation record in the linked plan for checks and execution adjustments.

Parent: [Console and Task planning](2026-10-03-console-and-task-planning-design.md). Implementation: [phase 1 plan](../plans/2026-10-03-console-foundation.md).

## Intent and scope

The user liked the UI references, requested a plan, suggested Kanban/Gantt, and then said “1looks nice.” This plan interprets that response as choosing phase 1, the console foundation and Run queue. The earlier question about Task scheduling versus actual Run timing remains separate.

Make the first screen answer three questions: what is running, what needs my attention, and how do I start work? Keep Task titles and Project context visible throughout these flows.

Deliver the shared console shell, an Overview, a searchable Run queue, a compact launch dialog, and links into existing request review. Task planning, Kanban, Gantt, and the file-oriented Run review redesign remain later phases. Phase 1 does not need Task schema changes.

## Global constraints

- Use React 19, Vite, TanStack Router/Query, Tailwind 4, and the existing shadcn/Radix Nova components; add no frontend dependency for phase 1.
- Use the domain names Project, Repository, Agent, Task, Run, and Workspace consistently; a Task and its Run attempts remain distinct.
- Keep `/runs/:runId`, `/?taskId=…`, `/setup` search parameters and OAuth callbacks, `/requests/:requestID`, and `/docs` working.
- Preserve existing Run detail, cancellation, event replay, Artifacts, GitHub/Linear delivery, and PR review behavior.
- Preserve existing Task and Run read/list response shapes; add a dedicated Run queue endpoint without modifying `RunRead` or introducing a migration.
- Starting execution requires the existing explicit Start Run action; navigation, filtering, and opening a dialog never start, retry, approve, or publish work.
- Use the current theme tokens and locally bundled Geist font; no paid component source or external font request is required.
- Do not stage or overwrite unrelated work already present in the workspace.

## Shell and navigation

| Location | Phase 1 behavior |
| --- | --- |
| `/` | Overview; an existing `taskId` opens the imported Task launcher automatically |
| `/runs` | Run queue with URL search state `group`, `q`, and `cursor` |
| `/runs/:runId` | Existing Run detail; Runs breadcrumb points to `/runs` |
| `/requests` | Existing incoming requests page |
| `/setup` | Existing configuration sections and callbacks |
| `/docs` | Existing independent docs layout |

Navigation is Overview, Runs, Requests, Setup, Docs. Do not show empty Tasks/Board/Timeline destinations before those features exist. Use a desktop sidebar and an accessible mobile navigation dialog. The shared header holds one visible Project picker and a New Task button.

Retain the `circular.selected-project` localStorage key and fallback to the first available Project. The picker is disabled while Setup saves, while a launch dialog owns Project context, and while a Run/request detail is initially resolving its scope. Release temporary locks on unmount and on errors.

Opening a Run or a Project-bound request directly selects its actual Project after loading. Changing Projects from one of these details navigates to Overview. An unassigned request is explicitly installation-wide; its Project picker remains available to choose a routing destination, and changing it stays on that request and clears the selected destination route. Keep the existing reviewed request actions intact. A request mutation locks Project selection until it settles.

Keep one Project control in the shell and remove the duplicate from Setup. Project errors appear in the shell; no Projects offers a link to `/setup?section=projects`. New Task is disabled until a valid Project has loaded and while detail scope is unresolved. Missing Repositories or enabled Agents offers the relevant Setup section from the launcher.

## Overview and Run queue

Overview has an Active Runs section (up to 10), Recent failures (up to 5), Project requests needing attention (up to 5), and a separately labeled “Unrouted requests · all Projects” section (up to 5). Each section has its own loading, error, retry, and empty state. “View all Runs” and “All requests” open the existing destination collections. Do not describe historical failures as unresolved alerts or show a fabricated total from a partial page.

Run rows show the Task title first, followed by Agent, Repository or “No Repository”, Run status, elapsed execution time, creation time, and secondary Run ID/attempt. Preserve the PR review Run kind label. Queued/provisioning Runs without `started_at` show “Not started” in the duration field; do not confuse queue wait with execution time. Each row has a real Run link usable with a keyboard and the browser's open-in-new-tab action.

The Run queue offers All, Active, Failed, and Finished filters and a “Search Tasks” field. Search matches Task titles only, case-insensitively and as literal substrings. Active means `queued`, `provisioning`, `running`, `waiting_for_approval`, `waiting_for_input`, or `finalizing`; Failed means `failed`; Finished means `succeeded`, `failed`, or `cancelled`. One row is one attempt, so retries remain separately visible. Success never changes Task workflow or implies human acceptance.

Newest page size is 50. “Older Runs” follows `next_cursor`; “Newest Runs” clears it. Preserve filters in the URL and browser history. A filter/search/Project change clears the cursor. Debounce typing by 250 ms and replace the URL during typing; deliberate filter/page changes create history entries. No per-row Task or Agent requests.

Poll newest All/Active Run pages every 2 seconds; Failed/Finished and attention sections every 15 seconds. Older Run pages do not poll and show a “Newest Runs” control. Poll only while the document is visible. Never display one Project's placeholder data under another Project's heading. Background refresh errors retain the current Project's last data with a visible stale/retry notice; initial errors never display an empty-success message.

Request attention uses only `needs_routing`, `awaiting_approval`, and `needs_access`. Opening a request follows the existing detail/review flow; no inline approval action is added. Artifact availability and unread/reviewed tracking are not attention signals in this phase; those require the Run review design.

## Read API

Add `GET /api/v1/projects/{project_id}/run-queue`, operation ID `listProjectRunQueue`, with these inputs:

- `group`: `all | active | failed | finished`, default `all`.
- `q`: trimmed Task-title substring, default empty, maximum 200 Unicode code points. `%` and `_` are literal characters.
- `limit`: integer 1–100, default 50.
- `cursor`: opaque keyset cursor, default empty, maximum 2048 bytes.

The response is `RunQueuePage { items: RunQueueItem[], next_cursor: string }`. `RunQueueItem` has required fields `run: RunRead`, `task_title: string`, `agent_name: string`, and `repository: { id: UUID, name: string } | null`. An empty terminal page is `{ items: [], next_cursor: "" }`.

Join Runs to Tasks and Agents and left-join Repository in one bounded query; reuse the existing Run projection within the explicit summary object. Sort by `(run.created_at DESC, run.id DESC)` and fetch `limit + 1`. Cursor version 1 encodes Project ID, normalized group/search, and the last creation timestamp/Run ID; reject a malformed cursor or a cursor for different filters/Project with 422. Decode/validate all query syntax before a database query. Missing Projects return 404, and responses use `Cache-Control: no-store`. Parameterize search and scoping; never interpolate user input into SQL. Pages reflect live state, not a frozen historical snapshot.

Extend `GET /api/v1/external-requests` with optional `attention=true|false`, default false. Apply the attention predicate before ordering/limiting for Project and unrouted queries. Keep existing scope validation, private-request guard, UUID cursor, pagination envelope, and default behavior. This avoids client filtering hiding older approvals behind a first page of completed requests. Scope the cursor lookup to the selected Project or unrouted collection; reject a cursor from another scope. Filter changes start at the first page.

Add these endpoints/types to OpenAPI and regenerate TypeScript. No new MCP operation is required for this console-specific read view; existing Task/Run tools keep their contracts.

## Launch dialog

Keep launch logic in `launch.ts` and move the existing form into a Radix dialog hosted once inside the console shell. Global New Task opens it for the selected Project. Retain the accessible labels Project, Repository, Agent, Task title, Description, and Start Run; Project is shown as fixed text inside the dialog rather than a second picker.

The launcher selects only Repositories and enabled Agents from its captured Project. Preserve the discovery Agent suggestion, imported title/description/Repository, missing-resource guidance, title length limit of 500, and Start Run disabled/pending/error behavior. Imported fields retain their existing read-only behavior. An invalid or inaccessible imported Task shows an error and cannot launch. Closing an imported dialog removes only `taskId` from the Overview URL with replace navigation.

Keep draft/partial launch state above the dialog's open state, scoped by Project and imported Task identity for the lifetime of the shell. Closing, reopening, or switching Projects cannot move a draft or saved Task into another Project. A Task-created/Run-failed result keeps its saved Task ID and chosen Agent and offers “Retry starting Run”; this retries `createRun`, never `createTask`. A resumed partial draft remains locked to those saved inputs. Offer “Start another Task” only when no request is pending; it clears that draft without deleting the saved Task and presents its `/?taskId=…` resume link.

While a launch request is pending, disable submission and prevent dialog dismissal. At other times Escape and Close work, focus returns to the trigger, and reopened drafts retain their input. On success, clear that draft, invalidate the relevant Run queue queries, close, and navigate to the existing Run detail. Phase 1 preserves the launch API's current retry semantics; stronger idempotency for uncertain network results is separate work.

## Accessibility and acceptance

- At 390 px and 1440 px widths, the shell, sections, dialog, and long Task/Repository/Agent names cause no page-level horizontal overflow. A table may scroll within its own surface.
- Keyboard users can choose Project, navigate pages and filters, open/close the launcher, select Repository/Agent, submit, and open a Run or request. Dialogs have titles, descriptions, focus containment, and focus restoration.
- Run state is conveyed in text as well as color. Loading/error/empty states are distinguishable and usable when one endpoint fails.
- A completed result, a failed attempt, a cancelled attempt, and several attempts of the same Task remain individually inspectable.
- Existing execution, Setup, discovery, provider import/callback, request approval, Artifact, and delivery tests remain meaningful and pass after navigation updates.

## References

[Existing concept](../../../output/ui-ux-ideas/circular-concept.html), [ReUI Run queue reference](https://reui.io/blocks/solutions/agents/solution-agents-2), and [shadcn Radix sidebar patterns](https://ui.shadcn.com/docs/components/radix/sidebar). ReUI informs the hierarchy; its paid block source is not needed.
