---
title: "Console setup"
description: "Create projects, add source code, configure agents, and connect your tools in the console."
---

Open **Setup** in the desktop sidebar or mobile navigation at
http://localhost:5173/setup. Resource forms and integrations use the shared shadcn
components and save through the resource API.

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
   Task, then **Start Run** in the launcher. It inspects the codebase and reports
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
5. Select **Go to Runs**, enter a task, and start it. New repositories and enabled
   agents are immediately available in the launcher.

For private source code, open **Integrations**, connect GitHub, choose the account
where the GitHub App is installed, and add an authorized Repository. For issue-based
work, connect Linear, choose a team and optional Linear project, select the Circular
Repository, and import an issue. The launcher opens the saved Task; choose an Agent
and start its Run. Importing the same issue again opens the existing Task and retains
its original Repository and issue snapshot. Use **New task** for a separate task.
First-time app registration starts from these Connect buttons in your own browser:
GitHub returns its app settings automatically, and Linear asks you to copy its
public Client ID back to Circular once. See [connection setup](integrations.md) for
server prerequisites and reconnect/disconnect behavior.

The selected Project is shared between Setup and Runs and remembered in browser
storage. If the saved Project no longer exists, the console uses the first
available Project. Setup tabs have direct URLs, such as
`/setup?section=repositories`, and survive a refresh. Switching Projects clears
repository and agent form drafts so they cannot be saved into another Project
by mistake. A failed save retains the current draft for correction or retry.

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
`tests/browser/integrations.spec.ts` adds console app registration and OAuth redirects through owned test providers,
private Repository selection, Linear issue import and Run creation, reconnect after
revocation, disconnect, callback rejection, and mobile states. It uses no provider
accounts or paid model calls.
`tests/browser/proposals.spec.ts` covers Markdown reports, mobile review dialogs,
model selection, retries after a lost creation response, and persistent dismissal
and creation status. It uses a fake Run and the real proposal API.
`tests/browser/mcp.spec.ts` covers client configuration, copy controls, read-only
selection, API check recovery and mobile layout without launching any Run.
