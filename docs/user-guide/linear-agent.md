---
title: Requests from Linear
description: Mention or delegate Circular, choose an agent, and follow the result.
---

Circular can receive work from a Linear issue and run your selected coding agent.
Progress stays visible in both the Linear agent session and Circular’s
[Requests inbox](/requests).

Your existing Linear connection stays connected. Enabling the bot lets it post
as Circular; receiving work also needs the event and destination setup below.

## 1. Enable Circular’s bot

In [Setup → Integrations](/setup?section=integrations), enable
[Circular’s bot](/docs/circular-identity). Select **Allow mentions and
delegation** and approve the authorization in Linear. Check the app name and
workspace shown in Circular.

Mentioning Circular calls it into an issue conversation. Delegating an issue asks
Circular to work on it. The human assignee remains the owner of the issue; the
Circular coding agent is the worker selected by your route.

## 2. Verify request reception

Under **Work from Linear → Verify incoming events**, configure reception using your installation’s receiver address and the
Linear app’s signing secret. Enable **Agent session events** in the app’s webhook
settings. Use **Check reception** after a delivery and look for **Receiving
events**. If you cannot configure the receiver, ask your Circular operator.

## 3. Choose where work goes

Under **Work from Linear**, select **Add destination**:

- Choose a Linear project or team whose issues should use this route.
- Choose a repository and an enabled coding agent in this Circular project.
- Review the agent’s model and reasoning level. Change them under
  [Setup → Agents](/setup?section=agents) if needed. Agents default to Astra when
  no model has been chosen.
- Choose **Start automatically** or **Ask in Circular first**.
- Select **Enable Linear requests**.

Automatic mode starts new matching requests and can consume model usage. Approval
mode waits for you to review the request in Circular. No route starts work until
it has been explicitly enabled. Settings for a Linear project take precedence
over its team, including when paused.

Incoming work uses your existing draft-PR and automatic-review settings. Enabling
requests does not enable either of those publishing options.

## Review a request and follow the result

Open **Requests** to see received text, requester, repository, agent, model and
reasoning level. **Unrouted requests** includes requests without a destination,
regardless of your currently selected project. Choose a matching route and select
**Prepare request** to make an approval preview.

Select **Start reviewed request** when ready. If agent or route settings have
changed, refresh and review the new preview first. Once a run starts, its approved
instructions and model settings are saved for that run.

Circular sends concise progress and the result to the native Linear agent
session. When enabled, draft PR and review links join that same session. The full
run report, checks and changes remain in Circular. A problem delivering an update
does not turn successful coding work into a failed run.

## Stop or change the work

Use Linear’s stop control or **Stop request** in Circular. This stops active work
and linked active reviews, and prevents new publishing or review operations.
A provider operation that already started may finish; Circular checks its receipt.
Existing PRs, reviews and completed run results remain available.

Follow-up messages are saved and acknowledged, but they do not change a running
agent’s instructions. Start a new Linear session for a different task. Each
session starts at most one run. If another session is already working on the same
issue, the new request waits for you to start it explicitly after that run finishes.

Requests without an issue cannot start coding work. Requests whose human requester
cannot be verified require approval. If app access is revoked, Circular stops
linked external work and disables its routes; reconnect, review the settings and
explicitly enable the route again.

## If progress is missing

Check **Linear delivery** on the request. Temporary outages are retried.
**Checking the original activity receipt** means Circular is verifying whether
Linear accepted an update; it will not create another copy blindly. Follow the
connection and routing settings link to repair access.

See [troubleshooting](/docs/troubleshooting#linear-requests-need-attention) for
routing, access and receiver problems.
