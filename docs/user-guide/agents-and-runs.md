---
title: Agents and runs
description: Choose models, explore a codebase, review suggested agents, and follow execution.
---

An agent is a reusable helper with instructions and a chosen model. A task is
what you want done. A run is one attempt by an agent to complete that task.

## Explore a repository

Each project includes a **Repository discovery** agent. Once your
[coding connection is ready](/docs/connections#model-access):

1. Open [Setup → Agents](/setup?section=agents).
2. Choose a repository under **Repository discovery**.
3. Select **Explore repository** to prepare a task.
4. Review the task in the launcher, then select **Start Run**.

Discovery asks the agent to explore the repository and report what it finds.
Use its report to understand the project and decide what to work on next.
It is instructed to analyze without making changes; review its output when it
finishes. Preparing the task does not start execution.

## Choose a model and variant

New Codex agents default to **GPT-6-Astra / Low**. In **Create agent**, select the
**Model** and **Variant** you want. Variant means reasoning effort; the picker
shows the supported choices for the selected model.

To change an existing agent, select **Edit model**, choose its settings, and
select **Save model**. Saving does not start a run. Make your selection before
starting the next run; work already in progress keeps its settings.

**Custom model…** accepts a model ID available to your account. The model catalog
ships with Circular and is not a list fetched from your personal account. If a
run reports that a model is unavailable, choose one your configured connection
can use.

The [connections guide](/docs/connections#model-access) explains model access.
**Test (fake)** is only a demo: it produces sample output and does not carry out
your requested coding task.

## Review suggested agents

A Codex agent can propose specialist agents, including instructions, a model,
and reasoning effort. Recommendations appear under **Suggested agents** on the
run page.

Select **Review & create** to inspect the proposal. Adjust the name,
instructions, model, or variant before creating the agent. The dialog explains
the orchestrating agent's model recommendation; **Use recommendation** restores
its suggested settings.

Creating a proposal's agent makes it available for future tasks in the same
project. It does not start a run. You can dismiss a recommendation you do not
need; creation and dismissal status remain visible after a refresh.

## Start a task

In [Runs](/), enter the title and description, choose a repository and an enabled
agent, then select **Start Run**. Be concrete about the outcome and any checks the
agent should perform. For example:

> Add a health endpoint that returns the service version. Follow the existing
> API conventions and run the relevant tests.

You can also [import a Linear issue](/docs/connections#import-a-linear-issue)
as a task, or prepare work through the
[coding-agent connection](/docs/coding-agent). Imported issues open the
launcher so you can choose an agent before starting work.

## Follow execution and review output

The run page shows its status and available output:

- **Agent output** renders completed agent messages as Markdown.
- **Timeline** shows the ordered execution events.
- **Changes** shows the proposed file changes.
- **Artifacts** lets you download outputs such as the final diff.
- Run details show available usage information.
- After a successful run, **GitHub pull request** shows whether its changes are
  ready to publish, waiting, or available as a draft pull request.
- For imported tasks, **Linear issue** links to the issue and shows the delivery
  status of run comments.

Use **Cancel Run** to request cancellation while work is queued or active.
Circular keeps the output recorded so far. A successful
run means execution completed; review its output and diff before applying the
changes elsewhere.

## Publish a draft pull request

Open a successful run and review **Changes**. In **GitHub pull request**, select
**Create draft pull request**. Circular publishes the captured changes to a
dedicated branch and opens a draft pull request against the run's base branch.
The run must belong to a connected GitHub repository and have captured changes.
A run with no changes does not need a pull request.

You can leave the page while publishing finishes. **Open pull request** appears
when the draft is ready. Review it on GitHub and mark it ready for review when
you are satisfied. Circular never merges the pull request automatically.

To do this for future runs automatically, turn on
[Automatically open draft pull requests](/docs/connections#automatically-open-draft-pull-requests)
in the project's GitHub integration. This setting starts off; older completed
runs can still be published individually from their run pages.

If publishing needs attention, follow the error on the run page. **Retry
publishing** or **Check again** continues the same run's request. You do not need
to run the agent again. See
[publishing troubleshooting](/docs/troubleshooting#a-pull-request-was-not-created).

To share run progress, results, and the published pull request on its Linear
issue, turn on [Publish run updates](/docs/connections#publish-run-updates-to-linear).
This posts comments; the issue's workflow status stays under your control.

## Dedicated PR reviews

Use the project’s **PR reviewer** to inspect a published pull request with its own model and reasoning level. Start from **Review PR** on the completed coding run. The review run keeps its findings, exact commits, checks, and publication status together. See [Pull request reviews](/docs/pull-request-reviews).

## Agents selected by Linear requests

A [Linear request route](/docs/linear-agent) chooses an existing agent and
repository. The request preview shows the model and reasoning level. Approval
uses that reviewed preview; a later change to the agent does not change a run
already started. Follow-up messages are saved without altering current execution.
Open **Requests** to approve, inspect or stop delegated work.
