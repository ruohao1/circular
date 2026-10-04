---
title: "Console components"
description: "Build consistent console controls, reports, and review flows with the shared components."
---

The console uses shadcn/ui's Radix Nova components with Tailwind CSS v4. The
generated source lives in `apps/web/src/components/ui`; the registry, aliases,
and preset are recorded in `apps/web/components.json`.

## Adding a component

With pnpm available on your PATH, run from the repository root:

```bash
corepack pnpm --filter @circular/web exec shadcn add dialog
```

If Corepack is installed but the `pnpm` command is missing, enable its shim once
with `corepack enable pnpm`. The shadcn CLI invokes pnpm when adding dependencies.
The checked-in CLI dependency and workspace lockfile make the installed tooling
version reproducible; component definitions are fetched from the official registry.

Import components through the `@/` alias:

```tsx
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
```

Use shared primitives for controls and surfaces. `ResourceSelect` composes shadcn's
`Select` for the Project, Repository, and Agent pickers, including themed popup
menus, keyboard navigation, and empty/disabled states. Use `Tabs` and `TabsContent` together
so keyboard navigation and panel associations remain accessible. Compose domain
components outside the generated `ui` directory: `RunStatus`, `ErrorAlert`, and
`EmptyState` are examples.

## Theme and layout

`apps/web/src/index.css` owns the color, radius, status, and typography tokens.
The console uses the dark theme with a locally bundled Geist font. Change tokens
there to update every component; use component variants and Tailwind classes for
layout instead of global `button`, `input`, or panel style overrides. Status and
diff colors share the `success`, `warning`, `destructive`, and `primary` tokens.

The [brand guide](../brand.md) defines the approved Orbit mark, palette, and
typography, and records the remaining palette alignment with the existing theme.

After frontend changes, run the frontend checks and browser scenarios described
in the README. Browser coverage includes launch, completion, cancellation,
failure, replay, downloads, keyboard tab navigation, and mobile overflow checks.
The [Setup area](console-setup.md) uses the same controls for creating Projects,
Repositories, and Agents, and for the GitHub/Linear connection and import flows.
Provider account, team, project, and Repository menus also use `ResourceSelect`.
Browser coverage includes failed saves, project switching, and provider connections.
Update a running local console with:

```bash
docker compose build web
docker compose up -d --no-deps web
```

See the official [Vite setup](https://ui.shadcn.com/docs/installation/vite) and
[theme reference](https://ui.shadcn.com/docs/theming) for the underlying conventions.

## Run reports and agent suggestions

`Markdown` renders agent output and Task descriptions using `react-markdown`,
GFM tables/task lists, and syntax highlighting. Raw HTML is skipped, unsafe link
schemes are disabled, repository paths are shown as references, and remote images
are rendered as text without fetching them. Code blocks have a copy button.

`RunReport` gives the latest complete message (or live partial message) the main
reading area. Earlier progress messages collapse below it. Preview/source controls,
copy, and Markdown download operate on the displayed report. The report uses page
scrolling; wide tables and code blocks scroll within their own surfaces.

`RunAgentProposals` displays durable MCP proposals and explicit recommendations
from older discovery reports. The shared Radix dialog keeps the create action
visible on small screens while its fields scroll. Model and variant controls reuse
`CodexModelFields`. Saved creation/dismissal states survive reloads, and creating
an Agent invalidates the launcher's Agent choices without starting another Run.
Model recommendations include an expandable explanation on the card and the
original suggested settings in the dialog. **Use recommendation** restores both
selectors, including after a custom model was entered. The saved Agent uses the
reviewed settings; the proposal retains the orchestrator's original recommendation.
