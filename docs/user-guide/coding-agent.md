---
title: "Connect your coding agent"
description: "Add Circular to your coding assistant with a connection URL or one command."
---

Use your coding assistant to browse projects, inspect results, and track its own
work in Circular. It can also start a Circular-managed Run when you request one.

The connection is included with Circular. Keep Circular running, and use a coding
assistant on the same computer. You do not need another account or model API key.

## Connect your assistant

Open [Setup → MCP](/setup?section=mcp), choose your **Coding assistant**, and
choose its **Access**:

| Choice           | What it allows                                                                                                                                |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Read only**    | Browse projects, repositories, agents, and tasks; read results, current ownership and work-session activity.                                             |
| **Full control** | Everything above, plus create projects, agents, and tasks; choose models and reasoning levels; accept recommendations; claim and report work; start and cancel runs. |

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

Choose the Task's completion policy and requirements in Task details before its
first claim or Run. Your assistant can inspect **Task completion**, refresh authoritative
GitHub evidence, bind a PR number, and start an independent Task review. It must
resolve the returned blockers before moving a review-required Task to Done;
reporting success or moving a card cannot bypass the requirements.

Circular-managed coding Runs submit their structured result through the local
`submit_task_result` tool. For work performed by your existing assistant, use the
workflow below. Linear workflow status sync is still separate; current run/review
comments remain available.

## Track work your assistant performs

With **Full control**, ask:

```text
Use Circular to find a Ready task in my project. Read its scope, dependencies,
completion policy and current owner. If the requirements are clear, claim it and
do the work here. Keep Circular informed with heartbeats, then submit the result
and required evidence. Ask me when the requirements or ownership need resolving.
```

Your assistant uses `get_task_work` to inspect a candidate, then `claim_task_work`
to register a Work session and move the Task to In Progress. This does not launch
another coding agent. An existing owner blocks competing claims.

The assistant must retain its session credential and operation IDs for retries.
It sends `heartbeat_task_work` about every 30 seconds while working. After 120
seconds without contact, Circular reports that the session needs attention;
ownership stays with that session. Connecting MCP alone does not claim a Task or
start heartbeats. Automatic client skills/plugins remain a follow-on.

`finish_task_work` submits the outcome, summary and required evidence. Successful
review-required work enters In Review; eligible direct-completion work becomes
Done. A failed or cancelled attempt closes without declaring the Task complete.
Missing required evidence keeps the claim active so the assistant can correct it.

Use `get_task_work` or `get_work_session` to inspect ownership and contact; both
are available in **Read only** mode. If a client has stopped permanently, explicitly
ask to release that exact owner with a reason. `release_task_work` prevents future
reports from its credential, but cannot stop an external process editing files.
Native cancellation waits for cleanup. Prepare the Task as Ready before a new
attempt. A competing agent must never release another owner automatically.

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
