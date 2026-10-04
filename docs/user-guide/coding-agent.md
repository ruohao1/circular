---
title: "Connect your coding agent"
description: "Add Circular to your coding assistant with a connection URL or one command."
---

Use your coding assistant to browse projects, inspect results, and manage work in
Circular. Tasks and runs it creates also appear in the console.

The connection is included with Circular. Keep Circular running, and use a coding
assistant on the same computer. You do not need another account or model API key.

## Connect your assistant

Open [Setup → MCP](/setup?section=mcp), choose your **Coding assistant**, and
choose its **Access**:

| Choice           | What it allows                                                                                                                                |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Read only**    | Browse projects, repositories, agents, and tasks; read results and inspect agent recommendations.                                             |
| **Full control** | Everything above, plus create projects, agents, and tasks; choose models and reasoning levels; accept recommendations; start and cancel runs. |

Access applies to **every project in this Circular installation**. Choose
**Read only** if you only want help understanding existing work.

### Codex

Copy the **Connection command** from the console and run it in the terminal where
you use Codex. Start a new Codex session to load Circular's tools.

### Other assistants

Copy the **Connection URL** from the console into your assistant's MCP settings.
Name the connection **Circular** and choose **Streamable HTTP** if asked. Reconnect
your assistant to load its tools.

MCP is the connection assistants use to access Circular. If your assistant only
accepts a command to launch a local server, see [the alternative below](#clients-that-require-a-local-command).

## Try a first request

Ask your assistant:

```text
Use Circular to list my projects and summarize the latest run.
```

With **Full control**, your assistant can also create a GitHub repository for a
connected project. For example: “Use Circular to create a private GitHub repository
named my-demo in my personal account and attach it to Test 2.” Circular needs
[repository creation permission](/docs/connections#create-a-new-github-repository).
Creating a repository does not start an agent run.

You can also ask your assistant to publish a completed run:

```text
Use Circular to create a draft pull request from this successful run's changes.
```

Circular handles the branch and draft pull request using the project's GitHub
connection. Your assistant can check publishing progress and retrieve the pull
request link. [Allow pull request publishing](/docs/connections#automatically-open-draft-pull-requests)
first if the connection needs additional permissions. Publishing does not merge
the pull request or start another agent run.

With **Full control**, you can also ask:

```text
Use Circular to prepare a small task for my project. Show me the repository,
task, agent, model, and reasoning level before starting the run.
```

Include the project and repository names when you know them. Open **Runs** in
the [console](/) to follow the work and review its result. Starting a run uses
the selected Circular agent and its existing model connection. See
[agents and runs](/docs/agents-and-runs) for how to review changes.

## Check or change the connection

In Setup → MCP, **Ready to add** means Circular's connection is available. After
your assistant uses it, **Last used by** shows the latest activity. Your assistant's
tool list confirms whether it has loaded Circular; activity is not a live
connection indicator.

If the console shows **MCP unavailable**, make sure Circular is running and select
**Try again**. If your assistant is missing tools, check that you copied the whole
command or URL and started a new session. A localhost URL works only from an
assistant running on the same computer as Circular.

To change access, select the new choice and update your assistant's connection
with the new command or URL. Changing the dropdown alone does not change an
existing connection.

## Clients that require a local command

In Setup → MCP, open **Advanced: use a local server**. This alternative requires
the Circular installation folder, a Linux or macOS terminal, and Docker or Go on
the computer where you use your assistant. Ask the person who installed Circular
for these details if needed.

1. Choose **Run with** to match your installation and enter the **Circular folder**.
2. Copy and run the **Build command** once.
3. Copy the **Local connection command** for Codex, or select the JSON configuration
   for another assistant. Add it to your assistant and start a new session.

The access choice at the top of the page also applies to these commands. Keep
Circular running. If the local connection fails, open **Check the local server**
and run the generated **Check command**.
