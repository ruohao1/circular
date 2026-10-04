---
title: Troubleshooting
description: Find missing repositories, reconnect your tools, and get a stalled task moving again.
---

## No repositories are available

Check the **Project** selector first. Repositories belong to a project, so a
repository added elsewhere will not appear in this project's launcher.

For GitHub, open [Setup → Integrations](/setup?section=integrations):

1. Choose the account where your GitHub App is installed.
2. Open **Manage GitHub access** and allow access to the repository you need.
3. Return to Circular and select **Refresh GitHub repositories**.
4. Add the repository to your project.

Connecting GitHub alone does not add every repository. If the connection has
expired or access was revoked, reconnect it. The repository also needs an initial
commit and a default branch before an agent can use it.

For a repository entered manually, check its clone URL and branch in
[Setup → Repositories](/setup?section=repositories).

## Repository creation needs attention

In **Setup → Integrations → GitHub**, choose the account that will own the
repository, then select **Create repository**. For a personal repository, this
must be the GitHub account connected to the Circular project.

If Circular asks to **Allow repository creation**, the app owner must enable
**Repository permissions → Administration → Read and write** in the GitHub App
settings. The account or organization owner then approves the changed permissions
through **Manage GitHub access**. Return and select **Refresh permissions**.
Organization rules may also limit who can create repositories and whether they
can be public or private.

If the repository was created but still needs access, open **Manage GitHub
access** and select the new repository. Return to the saved request and select
**Check access and add repository**. You do not need to create it again.

If the connection was interrupted, select **Finish adding repository** and
**Check and finish adding**. Circular checks the saved request before taking
further action. You can reload the same browser tab without losing it.

If Circular still cannot confirm the result, check the account's repositories
on GitHub. A repository that was created there can be added through the normal
**Add repository** action once the GitHub App has access. Do not submit another
creation request until you know whether the first one succeeded.

Once you have checked the result and handled any repository on GitHub, expand
**Resolve this request manually** and select **Clear saved request**. This removes
the saved reminder in this browser tab; it keeps any repository already created
or added to Circular.

## A GitHub app was renamed

Open **Setup → Integrations → GitHub → Connection settings → App settings** and paste the app's
current public app URL, then save it. This updates the link used to install
the app. You can copy its current URL from the app's GitHub page.

Return to **Manage GitHub access** to check which repositories are allowed.

## GitHub or Linear did not connect

Return to [Setup → Integrations](/setup?section=integrations) and check the
connection message. Creating an app on the provider's website is only one step:
finish the setup in Circular and approve the connection in your browser.

For Linear, copy the **Client ID** back into Circular and select
**Save and connect Linear**. Keep the redirect address Circular supplies. Leave
**Public**, **Client credentials**, and **Webhooks** off for this private setup.
If the optional **Developer URL** rejects a localhost address, leave it empty.

If GitHub reports a localhost webhook error, restart app registration from
Circular's Connect button. The current setup does not require webhooks.

If you use Circular on another computer and the browser returns to an unavailable
localhost page, ask the person managing your installation to check its connection
addresses. `localhost` always means the computer running that browser.

The [connections guide](/docs/connections) walks through the complete flow.

## Run updates are missing from Linear

Open [Setup → Integrations](/setup?section=integrations) for the run's Circular
project. Make sure Linear is connected and **Publish run updates** is on.
If **Allow Linear comments** appears, use it to approve comment access, then
turn publishing on. Existing connections may need this extra permission.

Only tasks imported from Linear have an issue to update. Publishing starts with
future run activity; enabling it does not post comments for earlier activity.

Open the run's **Linear issue** card to check delivery:

- **Waiting to publish** means the update is queued.
- **Retrying** means Circular will try again after a temporary delivery failure.
- **Reconnect required** means you need to reconnect Linear in Integrations.
  Reconnecting to the same workspace resumes waiting comments.
- **Could not publish** means delivery stopped. Read the error and check that
  the connected account still has access to the issue. After correcting access,
  use **Reconnect Linear** to retry the update.
- **Not published** means the update was skipped, for example because publishing
  was turned off or the connection was removed before it was sent.

Turning publishing off or disconnecting discards waiting comments. Re-enabling
publishing does not restore them. A comment delivery problem does not change the
run's result; you can still review its output in Circular.

## A pull request was not created

Open the run's **GitHub pull request** card. A run must finish successfully,
belong to a connected GitHub repository, and have captured changes. A run with
no file changes shows **No changes to publish**.

If the card says **Ready to publish**, select **Create draft pull request**.
For future runs, you can turn on **Automatically open draft pull requests** in
the project's GitHub integration. Enabling it does not publish older runs.

For a permission error, open **GitHub publishing settings**. The app owner must
enable **Repository permissions → Contents → Read and write** and **Pull
requests → Read and write**. The installation owner approves the update through
**Manage GitHub access**. Return to Circular and select **Refresh publishing
permissions**, then retry publishing from the run page.

**Waiting to publish** means Circular is preparing the draft. **Retrying** means
it will try again after a temporary problem. For **Needs attention**, read the
error and fix the reported problem before selecting **Retry publishing** when
it is available.

If the result is uncertain, select **Check again**. Circular checks the same
run's branch and pull request so an interrupted response does not require a new
run or a duplicate publishing request. An agent's successful result remains
available even if publishing fails.

## An agent exists but cannot run

Creating an agent saves its role and model selection. It does not connect a
model account. GitHub and Linear connections do not provide model access either.

Read the error on the run page. If it mentions sign-in or an unavailable coding
connection, ask the person managing Circular to enable or reconnect Codex. See
[Model access](/docs/connections#model-access) for how that connection is used.

If the chosen model is unavailable, open **Setup → Agents → Edit model** and
select one your connected account can use. Save your choice before starting
another run. Changing settings does not restart a failed run.

## The run is queued or has failed

Open the run and inspect its status, error message, and **Timeline**. A queued
run is waiting to be picked up. If it stays queued, ask the person managing
Circular to check that execution is available.

For an access error, check the repository's permissions and selected branch.
For a model error, check the agent's model settings and coding connection.
Resolve the reported cause before starting another attempt.

Use **Cancel Run** if you no longer want queued or active work to continue.
Output recorded so far stays available for review.

## A local repository cannot be opened

A folder on your computer may not be available to the Circular installation
running the task. Use a GitHub connection or an accessible Git clone URL, or ask
the person managing the installation to make the folder available.

## My coding agent cannot see Circular

In [Setup → MCP](/setup?section=mcp), copy the connection command for Codex or the
connection URL for another assistant. Keep Circular running on the same computer
as your assistant. For Codex, start a new session after adding the connection.

**Ready to add** means Circular's connection is available. Check your coding
agent's tool list to confirm it loaded Circular. If inspection works but you
cannot create tasks or start runs, check whether you selected **Read only**.

See [Connect your coding agent](/docs/coding-agent) for the full walkthrough.

## A pull request review needs attention

An **Incomplete** report is different from a report with no blocking findings: check its limitations and whether a structured report was submitted. A **Failed** run can still retain a partial report. If the PR changed, select **Review latest commit**; if its current status cannot be verified, check GitHub access and select **Refresh PR status**.

For a disabled or unavailable reviewer, select an enabled Codex agent in **Setup → Integrations → GitHub → PR reviews**. For a publication problem, use **Retry publication** or **Check publication result** on the existing report. These actions do not run the model again. See [Pull request reviews](/docs/pull-request-reviews).

## Circular identity needs attention

In **Setup → Integrations**, check **Published as** under the affected provider. If it is paused, select **Resume bot**. Use **Reconnect bot** to restore access; for Linear, choose the same workspace. For GitHub, verify the app’s repository permissions and installation access, or use **Update signing key** if its key was replaced.

A failed setup attempt preserves the working connection. Circular does not silently switch an app publication to your personal account. Shared Linear identity can serve several projects; pausing one project does not revoke the others.

**Needs confirmation** means a publication may already exist but its receipt could not be verified. Check the original issue or pull request before taking further action. Circular preserves the original author and avoids creating another copy.

## Incoming events are waiting

Open **Integrations → Incoming events** and compare the callback address with the provider's app settings. The public HTTPS address must reach the dedicated receiver on port **8001**. A working browser login does not prove that provider events can reach it.

For Linear, check that the signing secret in Circular matches the app's webhook settings. Save the replacement secret, then check reception after the provider sends another event. Circular accepts the previous secret briefly during a rotation. Invalid signatures and stale Linear deliveries do not change reception status.

If GitHub reports a configuration or access problem, use **Provider webhook settings** to review the app, then save and check again. Existing delivery records remain available while access is repaired.

## Linear requests need attention

| What you see                                  | What to do                                                                                                                                        |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| External requests are off                     | Enable Circular’s identity, allow mentions and delegation, verify reception, then explicitly enable a route.                                      |
| Waiting for a verified delivery               | Check the Linear app’s callback address, signing secret and Agent session events category. Trigger a delivery, then check reception again.        |
| Needs a route                                 | Open **Requests → Unrouted requests**, choose the destination project and configure a matching route. Preparing the request requires approval.    |
| Approval required                             | Review the instructions, agent and model, then select **Start reviewed request**.                                                                 |
| Settings changed                              | Refresh the preview and review the current selection before starting.                                                                             |
| Waiting for another run                       | Follow the active run. After it finishes, start this waiting request explicitly.                                                                  |
| Access needed                                 | Reconnect the app or restore repository access. Revoked access stops linked work and disables routes; re-enable only after checking the settings. |
| Delivery needs attention                      | Repair Linear access from the request’s settings link. Coding results remain available in Circular.                                               |
| Checking the original activity receipt        | Wait for reconciliation. Circular is checking the existing update and will not post a second copy blindly.                                        |
| A stopped request still has a provider update | An operation already started can finish. Its receipt and any completed results are retained.                                                      |

Use a Linear **issue** for coding requests. Start a new session to run changed
instructions; follow-up messages do not modify a running job. If a teammate cannot
open Circular links containing `localhost`, ask the operator for a reachable
console address.
