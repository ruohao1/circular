# Task workspace controls implementation plan

> **For agentic workers:** Use the existing isolated feature branch. Implement with focused parallel ownership and test-first changes; review the combined diff before release. Keep this plan with the implementation.

**Goal:** Give Tasks a compact Project breadcrumb and a collection toolbar with predefined views at the left and layout, filters, and sidebar controls at the right.

**Architecture:** Preserve the existing Task collection, details, paging, draft guards, and layouts. Add an optional server-side `scope` to both collection queries. A workflow sidebar reuses grouped server totals from a `limit=1` planning query; it does not count loaded Tasks.

**Tech stack:** Existing Go/PostgreSQL API, React, TanStack Query/Router, Radix primitives, Tailwind, Vitest, and Playwright. No dependencies or schema changes.

**Design:** The user approved the two-row design in chat on 2026-10-16: `Project / Tasks` above predefined views, with Search, Layout, Filters, and Sidebar at the right. Active means Ready/In progress/Review; Backlog means open; All Tasks includes Done/Cancelled. The first sidebar provides workflow totals and stage filters. Existing List/Kanban/Gantt tabs become a compact layout control.

## Constraints and review focus

- Omitted API scope retains legacy behavior; explicit scopes constrain records and totals before paging, with cursor binding.
- UI defaults to All Tasks. Preset changes clear stage/detail selection, preserving layout, date, search, and resource/priority filters; unsaved drafts still require the existing discard decision.
- Sidebar stage counts include the selected preset and all non-stage filters, across the complete collection and all planned dates. Its displayed matching total accounts for the selected stage. Stage buttons toggle that filter.
- Sidebar is closed initially, a docked complementary region at >=1024px and a dismissible dialog below. Project and filter changes cannot show another scope's counts; errors offer Retry.
- Keep keyboard names/focus, long-title truncation, Project selection locks, and mobile navigation. Header changes apply only to Tasks; creation moves into its toolbar.
- Preserve deployment-only backend changes by copying retained source and overlaying named changed files. Verify API scopes before activating the new frontend; no migrations or unrelated service restarts.

## Tasks

- [x] **API scopes:** Add regression tests for all six stages, filtered intersections, pagination/cross-scope cursor rejection, legacy defaults, and Timeline totals. Implement `scope?: active | backlog | all` in both HTTP handlers and OpenAPI; regenerate types. Run the HTTP API suite against an owned disposable database.
- [x] **Breadcrumb:** Test the compact Tasks breadcrumb, keyboard Project selection, dirty detail lock, empty Projects, and narrow screens. Change `console/shell.tsx`; preserve other page headers and Project navigation behavior. Run focused console tests.
- [x] **Collection toolbar and sidebar:** Test scope URL normalization/query cache separation, presets across layouts/Back, server totals beyond 50 loaded Tasks, stage filtering, sidebar mobile close/focus, and no writes. Add scope to `api.ts`, planning search/options and both collection hooks; update `tasks.tsx` and add a small `task-summary.tsx` component. Extend existing browser fixtures and update intentional toolbar assertions.
- [ ] **Verify and release:** Run frontend unit/typecheck/build, Tasks/Timeline/Console browser suites, contracts check, and Go API checks. Review screenshots and combined diff. Commit this plan with the implementation; overlay retained release source, deploy API then frontend with rollback evidence, and verify read-only live desktop/mobile behavior and data/service preservation.

## Execution evidence

- Test-first scope/query normalization: initial two expected failures, then all 47 frontend unit tests passed.
- HTTP API: 55 top-level tests passed, no skips, against an owned disposable PostgreSQL instance; generated contracts are current.
- Console: 24 browser tests passed, including held setup-save navigation. Its regression failed before restoring the New Task setup-saving guard.
- Sidebar width regression failed at 1024px (68px title) and 1280px (collapsed title); using collection container width fixed both. Seven focused layout/metadata tests passed, including Kanban filling the available height.
- Complete browser suites: Tasks 41/41, Timeline 26/26, Console 24/24. Frontend unit 47/47, typecheck, contract generation check, production build, and diff checks passed. Final independent review found no remaining blockers. Live release checks follow activation.
- Existing limitation discovered: installed TanStack History 1.162.1 rolls forward one entry when a multi-entry native Back jump is cancelled, potentially leaving the address bar out of sync with retained Task details. Ordinary one-entry Back cancellation/acceptance is covered and passes. No dependency patch is included in this workspace change.
