---
title: "Console setup"
description: "Create projects, add source code, configure agents, and connect your tools in the console."
---

Open **Setup** in the desktop sidebar or mobile navigation at
http://localhost:5173/setup. Resource forms and integrations use the shared shadcn
components and save through the resource API.

The console navigation contains **Overview**, **Runs**, **Requests**, **Setup**,
and **Docs**. A shared header provides the **Project** picker and **New Task**
button across console pages; Docs has its own layout.

1. In **Projects**, create a project with a name and optional description. It
   becomes the selected project. Select an existing project from the list or the
   Project menu to work with its resources.
2. In **Repositories**, enter a name, clone URL or local path, and default branch.
   The source must be accessible to the worker, and the branch must contain a
   commit. Registering a repository saves its details; cloning and access checks
   happen when a Run starts. With Docker Compose, a local path must exist inside
   the worker container, not just on the host.
3. In **Agents**, the supplied **Repository discovery** Agent is ready to use.
   Choose a Repository and click **Explore repository** to prepare a discovery
   Task. Its launcher opens automatically with the discovery Agent selected;
   choose **Start Run** to execute it. It inspects the codebase and reports
   its purpose, architecture, workflow, priorities, and useful specialist Agents
   with suggested instructions. Its instructions ask for analysis without file
   changes; the report appears in the Run's agent output. Preparing the Task does
   not start execution. The Agent uses the worker's existing Codex connection.
   After the Run, use **Suggested agents → Review & create** to edit a
   recommendation's name, instructions, model, and variant before creating it.
   The orchestrating Agent chooses the model and reasoning level for each role
   and explains its recommendation. You can keep, override, or restore those choices.
   Explicit recommendations in older discovery reports can use this flow too.
4. To add a custom specialization, enter a name and choose **Codex** or **Test (fake)**. Add optional
   instructions that apply to tasks assigned to the Agent. Codex provides **Model**
   and **Variant** selectors, defaulting to **GPT-6-Astra / Low**. Variant is the
   reasoning effort; only supported levels appear for each model. Use **Custom model…**
   for another model ID available to your account. On an existing Agent, including
   Repository discovery, choose **Edit model**, adjust its settings, and **Save model**.
   The fake
   backend runs a simulated task without contacting a model provider.
5. Select **Go to Runs** to open `/runs`, then **New Task** in the shared header.
   Choose a Repository and Agent, enter the Task title and optional description,
   and select **Start Run**. New Repositories and enabled Agents are immediately
   available in the launcher.

For private source code, open **Integrations**, connect GitHub, choose the account
where the GitHub App is installed, and add an authorized Repository. For issue-based
work, connect Linear, choose a team and optional Linear project, select the Circular
Repository, and import an issue. The launcher opens the saved Task; choose an Agent
and start its Run. Importing the same issue again opens the existing Task and retains
its original Repository and issue snapshot. Use **New Task** for a separate Task.
First-time app registration starts from these Connect buttons in your own browser:
GitHub returns its app settings automatically, and Linear asks you to copy its
public Client ID back to Circular once. See [connection setup](integrations.md) for
server prerequisites and reconnect/disconnect behavior.

The selected Project is shared across console pages and remembered in browser
storage. If the saved Project no longer exists, the console uses the first
available Project. Selection still works when browser storage is unavailable.
The shared picker is temporarily disabled while Setup saves, while a launch
dialog owns the Project context, while a request action is pending, and while a
Run or request detail initially resolves its Project. Opening a Run or an assigned
request directly selects its actual Project; changing Projects from that detail
returns to Overview. On an unassigned request, choosing a Project stays on the
request and clears any previously selected routing destination.

Setup tabs have direct URLs, such as
`/setup?section=repositories`, and survive a refresh. Switching Projects clears
repository and agent form drafts so they cannot be saved into another Project
by mistake. A failed save retains the current draft for correction or retry.

## Overview and Run queue

**Overview** at `/` shows up to ten Active Runs, five Recent failures, and five
Project requests needing attention. **Unrouted requests · all Projects** is a
separate, installation-wide section showing up to five unassigned requests,
including when no Project exists. Request attention means routing, approval, or
access is needed. Open a request to use its existing review actions. Recent
failures are historical Run attempts; their presence does not mean an unresolved
alert. Each section has its own loading, empty, error, and retry states.

**Runs** at `/runs` lists attempts for the selected Project with the Task title,
Agent, Repository, status, and execution duration. A Run without a start time
shows **Not started**. Retries have separate rows, and PR review Runs retain their
kind label. Open a row to inspect the existing Run detail, including cancellation,
events, reports, Artifacts, and delivery actions.

Use **All**, **Active**, **Failed**, or **Finished**, and **Search Tasks** to match
a literal substring of a Task title without case sensitivity. Finished includes
succeeded, failed, and cancelled attempts. The newest page contains up to 50 Runs;
**Older Runs** and **Newest Runs** navigate the queue. Filters and search are kept
in the URL and browser history. Changing a filter, search, or Project starts at
the newest page. Older pages do not refresh automatically. If a background
refresh fails, a visible notice identifies the retained data and offers retry.

## Starting and resuming Tasks

**New Task** opens a dialog for the selected Project. The Project is fixed inside
the dialog; its Repository choices and enabled Agents belong to that Project.
Missing resources link to the relevant Setup section. Closing and reopening the
dialog retains the draft for the lifetime of the console shell. Drafts belong to
their Project and imported Task identity, so switching Projects cannot move them
into another Project. These launch drafts are separate from Setup form drafts.

An imported or discovery link such as `/?taskId=…` opens the saved Task
automatically in its Project. Its title, description, and Repository stay
read-only. Choose an Agent and select **Start Run**; opening the link does not
start execution. Closing the dialog removes only `taskId` from the Overview URL.
An invalid or inaccessible Task shows an error and cannot launch.

If the Task is saved but its Run cannot start, **Retry starting Run** uses that
saved Task and the chosen Agent. The saved inputs stay locked, including after
closing and reopening, and retry does not create another Task. **Start another
Task** clears that draft without deleting the saved Task and provides its resume
link. Submission and dismissal are disabled while a launch request is pending.
Otherwise, Escape or Close dismisses the dialog and restores focus. A successful
launch clears its draft and opens the Run detail.

See [Linear requests](../user-guide/linear-agent.md),
[pull request reviews](../user-guide/pull-request-reviews.md), and
[GitHub/Linear delivery](integrations.md) for the existing review and publication
flows. A successful Run does not imply human acceptance of its result.

## Other Setup capabilities

**MCP** connects external coding agents to Circular through a local stdio server.
It is available before choosing a Project and covers the whole Circular instance.
Choose Docker Compose or a local Go binary, enter the absolute repository folder,
and select full control or read-only access. Copy the build command, client
configuration and first prompt. The page checks API reachability and provides a
separate server check command. See [control MCP](control-mcp.md) for tool behavior
and launch retry guarantees.

The Setup area lists and creates resources and edits existing Codex model settings.
Other resource edits and deletion are not included. Creating a Codex Agent does not enable the worker or
sign in to Codex. The worker uses the connection configured in the
[Codex backend guide](codex-backend.md); credentials are not entered in these forms.

Every new Project receives one **Repository discovery** Agent in the same
transaction as Project creation. Revision `0005` supplies it to existing Projects.
A stable preset identity prevents duplicates and preserves custom names,
instructions, backend configuration, and disabled status on later setup requests.
Custom Agents with the same display name are retained; the supplied Agent gets a
numbered name. Discovery recommendations do not create specialist Agents automatically.

Browser coverage in `tests/browser/setup.spec.ts` exercises empty states, resource
creation through the real API, failed-save recovery, Codex configuration
validation, project isolation and persistence, mobile navigation, and a complete
Run using the fake backend. It never launches a Codex Run.
`tests/browser/console.spec.ts` covers shared Project context, launch draft and
partial-launch recovery, the Run queue, and independent Overview section states.
`tests/browser/integrations.spec.ts` adds console app registration and OAuth redirects through owned test providers,
private Repository selection, Linear issue import and Run creation, reconnect after
revocation, disconnect, callback rejection, and mobile states. It uses no provider
accounts or paid model calls.
`tests/browser/proposals.spec.ts` covers Markdown reports, mobile review dialogs,
model selection, retries after a lost creation response, and persistent dismissal
and creation status. It uses a fake Run and the real proposal API.
`tests/browser/mcp.spec.ts` covers client configuration, copy controls, read-only
selection, API check recovery and mobile layout without launching any Run.
