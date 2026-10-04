---
title: Run your first task
description: Set up a project in the console, choose an agent, and review its work.
---

Start in the Circular console. You will need a repository the agent can access
and an available coding connection. If someone set up Circular for you, ask them
to confirm model access is ready. See [Connections](/docs/connections#model-access)
if you are unsure which account is used.

## Create a project

Open [Setup → Projects](/setup?section=projects), enter a name, and select
**Create project**. Circular selects your new project and includes a
**Repository discovery** agent.

If your project already exists, select it from the **Project** menu instead.
Its repositories and agents will be available while you work in that project.

## Add a repository

For GitHub, open [Setup → Integrations](/setup?section=integrations), connect your
account, and add the repository you want to use. Follow
[Connect GitHub](/docs/connections#connect-github) if this is your first connection.

You can also open [Setup → Repositories](/setup?section=repositories) and enter
a name, Git clone URL, and default branch, then select **Add repository**. The
branch must contain at least one commit. For a private GitHub repository, use
the GitHub connection so Circular has permission to access it.

Once added, the repository appears in your project's task launcher.

## Create an agent

Open [Setup → Agents](/setup?section=agents). For a first look at your repository,
use the included **Repository discovery** agent: choose your repository and
select **Explore repository**. Circular prepares a task for you to review.

For your own task, use **Create agent**. Give it a name, choose **Codex**, and add
instructions describing its role. Keep **GPT-6-Astra / Low**, or choose a
different **Model** and **Variant**, then select **Create agent**.

**Variant** is the model's reasoning effort. The available choices depend on
the selected model. You can change these settings later with **Edit model**.
See [Agents and runs](/docs/agents-and-runs#choose-a-model-and-variant) for details.

**Test (fake)** is available for a demo without model access. It generates sample
output; choose **Codex** when you want the agent to do your requested work.

## Start your task

1. Open [Runs](/). If you selected **Explore repository**, review the prepared task.
2. For a new task, enter a title and describe the outcome you want.
3. Choose your repository and agent.
4. Select **Start Run**.

A useful first task is a small review: ask the agent to summarize one part of the
project and suggest an improvement. Include any constraints, such as making no
file changes. Clear tasks make results easier to assess.

## Review the result

Follow **Agent output** as the agent works. When the run finishes, read its
answer, open **Changes** to inspect any proposed file changes, and download the
**Final diff** from **Artifacts** if you need it.

If the agent suggests specialist agents, use **Review & create** to inspect and
adjust each recommendation before creating it. Creating a suggested agent does
not start another run.

Circular keeps the results for review. A successful run does not automatically
publish changes to GitHub. If something goes wrong, the run page's error and
**Timeline** help you identify what needs attention; see
[Troubleshooting](/docs/troubleshooting).
