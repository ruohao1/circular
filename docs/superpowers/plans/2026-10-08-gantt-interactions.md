# Gantt interactions implementation plan

> **For agentic workers:** Use superpowers:executing-plans; native implementation is already selected in this session.

**Goal:** Move and resize saved Task schedules in Gantt, with one-step Undo and safe conflict handling.

**Architecture:** Reuse controlled ReUI gestures and the existing shared planning mutation. Capture immutable schedule and revision snapshots; accept no optimistic dates. Undo uses the successful write's revisions, never rebased revisions.

**Tech Stack:** Existing React, TanStack Query, ReUI, Vitest, Playwright; no new dependencies or backend changes.

**Spec:** [Task schedules design](../specs/2026-10-04-task-schedules-gantt-design.md#drag-resize-and-accessibility), narrowed by the user's approval of drag, resize, Undo, and conflict handling on October 8.

## Global constraints

- Inclusive civil dates 0001–9999; one-day snapping and minimum duration; preserve full dates on clipped bars.
- Only the existing date PATCH with both revision tokens may persist a gesture. No Run side effects.
- Shared List/Kanban/Gantt filters and drafts remain intact. Keep native date fields and keyboard shift controls.
- Ruling: implement the approved interactions before dependencies; dependencies and their migration remain deferred.
- Ruling: one Undo slot, replaced by the next successful local write and cleared on Project changes; no history stack or redo.

## Review focus

- A gesture uses an old snapshot after scope, revision, loading, or permission changes: cancel and perform no write.
- Pointer release outside the chart, Escape, blur, cancellation, unmount, or resize: perform no write and release locks.
- Undo after a concurrent update or after the Task leaves the window: retain exact response tokens and reject conflicts without replay.
- Dates near year limits, DST, leap days, or clipped boundaries: validate UTC renderer conversion and inclusive duration.
- Touch scrolling and keyboard editing: preserve intentional scrolling and equivalent accessible date editing.

### Task 1: Safe schedule intents and shared writes

**Files:** `lib/task-timeline.ts`, its unit test, `components/tasks/use-task-planning.ts`, `use-task-timeline.ts`, `tests/browser/task-fixture.ts` (all frontend files under `apps/web/src`).

**Interfaces:** `TaskScheduleSnapshot`, `TaskScheduleIntent`, `scheduleIntent`; shared controller exposes `reschedule(intent)`, `undoSchedule()`, `canUndoSchedule`.

- [x] Write and observe failing unit checks for stale snapshots, invalid/no-op dates, boundary conversion, and duration preservation.
- [x] Implement date-only shared mutation, canonical refresh, scope validation, error announcements, and one-step Undo with exact response revisions.
- [x] Make the existing browser fixture enforce expected Task/board revisions.

### Task 2: Gesture wiring and verification

**Files:** `components/tasks/task-timeline.tsx`, `task-schedule-fields.tsx`, `pages/tasks.tsx`, `tests/browser/timeline.spec.ts`; a small Gantt lifecycle component if needed to subscribe inside the existing provider.

- [x] Write and observe failing browser checks for move, both resize edges, Undo/conflicts, cancellation, disabled states, and keyboard equivalents.
- [x] Wire synchronous ReUI update callbacks, returning false after starting the shared mutation. Pause polling and lock navigation during gestures; cancel stale/outside gestures and restore focus.
- [x] Run focused unit/browser checks, existing Tasks regressions, typecheck, and production build. Inspect desktop/mobile screenshots and obtain one fresh diff review.
- [x] Transfer only baseline-matching files to the shared workspace. Publish frontend only through the existing backed-up deployment flow; perform read-only live verification.

## Execution record

- Baseline: `9121a72` in `/tmp/circular-timeline`. Workspace and previous retained release are the transfer baselines.
- Preflight: backend already checks both revision tokens; renderer supports synchronous proposal rejection, horizontal moves, daily snapping, and clipped-edge handles. Outside release needs a consumer guard.

- Verification: 46 unit tests, 23 Gantt browser tests, 27 existing Tasks browser tests, typecheck and production build passed. Real CDP touch covers scrolling, hold-drag, resize and pointer cancellation. Desktop/touch screenshots inspected.
- Fresh review: fixed wheel-scroll cancellation, Undo focus restoration after controls reenable, and inaccurate off-page Undo guidance. Regression checks passed.
- Vendor findings: tiny bars had overlapping handles and resize jumps; cap handle widths and preserve the actual endpoint grab offset. Daily move/resize/Undo passes at all scales in opposing timezones.

- Published frontend-only release `vps-gantt-interactions-20261008`; API/services/configuration unchanged. Read-only live desktop/mobile checks passed with zero writes/errors and 23 Runs preserved. The selected Project has four Unscheduled Tasks and no dated bars; populated evidence uses local fixtures.

## GitHub release reconciliation — 2026-10-16

The released Task planning, Kanban, dates, Timeline, gestures and column quick-add
changes from `e27a62db153f1efb2935b656023991dd818292a6` were extracted onto GitHub
main `cf485db8ec0c296f01cba9c84dcb8f6c2a8375aa`. Existing reviewer Git isolation,
provider browser corrections, CI rules and prior release records remain intact.
Migrations `0016` and `0017` are unchanged. Scoped API/migrator release overrides
are included; browser configurations now retain artifacts under `test-results/`.
Task dependencies remain outside this release.

Formatting, Go vet/build, contracts, TypeScript, 46 frontend tests and the
production build passed. Race-enabled Task migration/storage/HTTP/MCP checks
passed 72 tests/subtests without skips. All 31 Task/Kanban and 26 Gantt browser
tests passed. A Compose configuration check verified shared/scoped image tags and
private backend ports using synthetic values. No production state was changed.

A focused integration review found that the imported real-API planning tests used
the local API port and a host-only repository path in Compose mode. The fixture
now reuses the existing execution tests' mode-specific API URL and mounted Git
bundle, without relaxing Git ownership checks. All three planning scenarios and
MCP discovery passed against both the local Go stack and a freshly built default
Compose stack on the combined delegation branch. Both draft branches include the
fixture correction.

This is the prerequisite branch for the delegation draft PR. The combined branch
must retain migration `0018` for delegation and pass its integration checks before
either draft is considered for release.

The first full GitHub browser run then exposed eight Timeline cases whose custom
browser contexts hardcoded the standalone port `15178`. Four context constructors
now inherit Playwright's `baseURL` fixture, preserving their timezone, viewport
and touch settings across standalone, local CI and Compose configurations. All
eight affected cases passed with both the default local stack and a freshly built
Compose stack. Independent review found no further issues in this test-only fix.
