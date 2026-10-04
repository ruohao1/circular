---
title: Circular’s bot
description: Keep your connections and choose who publishes activity.
---

**Enabling Circular’s bot keeps your existing GitHub and Linear connections.**
It lets new activity appear under your app’s name and avatar. Earlier PRs and
comments keep their original author.

| Connection | What you already have                | What the bot adds                                                    |
| ---------- | ------------------------------------ | -------------------------------------------------------------------- |
| GitHub     | Browse, add and create repositories. | Publish new PRs and reviews as Circular.                             |
| Linear     | Browse and import issues.            | Post run updates as Circular; configure mentions and delegated work. |

## Enable the bot

1. Open [Setup → Integrations](/setup?section=integrations) and select your project.
2. Under GitHub or Linear, select **Enable Circular bot**.
3. Complete the provider’s step below, then check the name beside **Published as**.

**GitHub:** If asked for a private key, open your existing GitHub App’s settings,
find **Private keys → Generate a private key**, and upload the downloaded `.pem`
file in Circular. This lets GitHub recognize the app. New apps created through
Circular save this key automatically; older setups may need this one-time step.

**Linear:** Approve the app authorization in your existing workspace. This allows
Circular to post under its own app name.

Use the separate publishing switches to choose which updates, PRs and reviews
Circular sends. Enabling the bot leaves those settings as they are.

## Let Linear send work to Circular

Follow [Requests from Linear](/docs/linear-agent) to allow mentions and delegation,
verify incoming events, and choose a repository, agent and run mode. Work can
start from Linear only after you explicitly enable requests. The human assignee
remains responsible for the issue.

## Pause or repair the bot

**Pause bot** pauses the bot for this project. Circular asks you to
repair unavailable app access; it does not silently publish as your account.
A Linear app authorization can be shared by several projects; Circular shows
which ones are affected when reconnecting.

See [connection settings](/docs/connections) for publishing permissions and event
setup, or [troubleshooting](/docs/troubleshooting#circular-identity-needs-attention)
for recovery.
