---
title: "Connect GitHub and Linear"
description: "Add GitHub repositories, import Linear issues, and share run updates."
---

Open [Setup → Integrations](/setup?section=integrations) and select the Circular
project you want to use. Each project has its own connections.

Connect GitHub to choose source code for your tasks. Connect Linear if you want
to start work from your team's issues. You can use either connection on its own.

Already connected? [Enable Circular’s bot](/docs/circular-identity) to publish
under its app name while keeping your existing connections.

## Connect GitHub

1. Select **Connect GitHub**.
2. If Circular shows **Create your GitHub App**, enter your **Organization**
   or leave it blank for a personal account. Select **Continue on GitHub**.
3. On GitHub, name the app, create it, and install it on the repositories you
   want Circular to access. Circular supplies the app settings.
4. Return to Circular. If prompted, select **Connect GitHub** again and approve
   access to your account.

App creation is a one-time step for this Circular installation. If the app
already exists, **Connect GitHub** takes you straight to authorization.

### Add a repository to your project

1. Under GitHub, choose the **GitHub account** that owns the repository.
2. Find the repository and select **Add repository**. Its button changes to
   **Added**.
3. Select **View repositories** to see it in your Circular project.

Connecting GitHub makes repositories available to browse; **Add repository**
adds the one you choose to the project. It will then appear in the task launcher.

If a repository is missing, open **Manage GitHub access**, allow access to it on
GitHub, then return and select **Refresh GitHub repositories**. The repository
must also be accessible to the GitHub account you authorized. Before starting a
run, make sure it has an initial commit and a default branch.

### Create a new GitHub repository

1. Under GitHub, choose the **GitHub account** that will own the repository.
   This can be your connected personal account or an organization where the
   GitHub App is installed and you can create repositories.
2. Select **Create repository**.
3. Enter a **Repository name** and, optionally, a description.
4. Choose **Private** or **Public**. Private is selected by default. Public
   repositories and their contents are visible to anyone on the internet.
5. Select **Create and add repository**.

Circular creates the repository on GitHub with a README and an initial commit,
then adds it to the selected Circular project. When you see **Repository ready**,
you can select it in the task launcher and describe what you want an agent to
build.

If you see **Allow repository creation**, the app owner needs to open the
GitHub App's settings and change **Repository permissions → Administration** to
**Read and write**. The account or organization owner then approves the updated
permissions through **Manage GitHub access**. Return to Circular and select
**Refresh permissions**. You do not need another token or API key.

If Circular shows **Repository created — finish adding it**, open **Manage
GitHub access** and check access to the new repository, then use **Check access
and add repository**. Circular finishes adding the existing repository.

If the result is interrupted or uncertain, use **Finish adding repository** and
**Check and finish adding** to check the same request. Keep using this saved
request until its result is clear; starting over under another name could create
another repository. The request stays available when you reload the same browser
tab. See [troubleshooting](/docs/troubleshooting#repository-creation-needs-attention).

### Automatically open draft pull requests

Under GitHub, turn on **Automatically open draft pull requests** to publish
changes after future successful runs in the selected project. This starts off.
Circular publishes each run's captured changes to a dedicated branch and opens
a draft pull request for your review. It never merges the pull request
automatically. Runs without changes do not create pull requests.

If you see **Allow pull request publishing**, the app owner must set
**Repository permissions → Contents** and **Pull requests** to **Read and write**
in the GitHub App settings. The installation owner then approves the updated
permissions through **Manage GitHub access**. Return and select **Refresh
publishing permissions**, then turn on automatic drafts. These are repository
permissions; organization administration permission is not needed.

For a completed run, use **Create draft pull request** on its run page. This
works independently of the automatic setting. The **GitHub pull request** card
shows progress and links to the result; retries continue the same request.

Turning automatic drafts off stops future automatic publishing and leaves
existing branches and pull requests on GitHub. Publishing already in progress
may finish; check its status on the run page.

## Connect Linear

1. Select **Connect Linear**.
2. If Circular shows **Set up Linear once**, select **Create Linear app**. A
   prefilled form opens in Linear.
3. Create the app in your workspace. Leave **Public**, **Client credentials**,
   and **Webhooks** off. Keep the prefilled **Redirect URI**; **Developer URL**
   can stay empty.
4. Copy the app's public **Client ID** into **Linear Client ID** in Circular.
5. Select **Save and connect Linear** and approve access on Linear.

You do not need a client secret or personal API key. If the app is already set
up, **Connect Linear** opens authorization directly.

### Import a Linear issue

1. Choose a **Linear team** and, optionally, a **Linear project**.
2. Choose the **Circular repository** where the work belongs.
3. Find the issue and select **Import issue**.
4. In the task launcher, review the task, select an agent, and select **Start Run**.

Importing the same issue again in the same Circular project opens its existing
task. Later edits to the Linear issue are not copied into that task.

### Publish run updates to Linear

Turn on **Publish run updates** in the Linear connection card to share progress
on imported issues. This setting belongs to the selected Circular project and
starts off.

If Circular shows **Allow Linear comments**, select it and approve the added
comment permission on Linear. Return to Circular and turn on **Publish run
updates**. Reconnecting alone does not enable publishing.

Circular posts a comment when an imported task's run starts and another when it
finishes, fails, or is cancelled. If the run finishes before its start comment is
sent, Circular posts the result directly. Comments link to the run and include
a result summary and any GitHub pull request links reported by the agent.
When Circular publishes a draft pull request, it also shares that link on the
imported issue if run updates are enabled. Pull request publishing has its own
GitHub setting described above. Run links open
your Circular installation; someone reading the issue needs access to that
installation to open them.

On the run page, **Linear issue** opens the original issue and shows whether the
latest update is waiting, published, or needs attention. Temporary delivery
failures are retried automatically. The integration card also shows updates
waiting or needing attention.

Publishing applies to future run activity; it does not post the history of older
runs. Turning it off stops future comments and discards comments still waiting
to be sent. Comments already posted stay in Linear. Disconnecting also discards
waiting comments. If Circular asks you to reconnect because authorization has
expired, reconnecting to the same workspace lets pending comments resume.

Issue status stays under your control in Linear. Publishing comments does not
move issues between workflow states or synchronize later issue edits.

For the next steps, see [agents and runs](/docs/agents-and-runs).

## Reconnect or change access

Open **Connection settings** under GitHub or Linear to reconnect your account
or manage the app. Use **Reconnect GitHub** or **Reconnect Linear** if Circular
asks you to authorize again. For an enabled Linear bot, use **Reconnect bot**. Use **Disconnect GitHub** or **Disconnect Linear** to remove
the connection from this project; repositories and tasks already added remain.

If you renamed the GitHub app, open **Connection settings → App settings** in Circular, paste the
new **GitHub app URL**, and select **Save GitHub app URL**.

If a connection button is unavailable, ask the person managing your Circular
installation to enable provider setup. See [troubleshooting](/docs/troubleshooting)
for other connection problems.

## Model access

GitHub and Linear access are separate from Codex sign-in. Agents use the Codex
connection configured for your Circular installation; these integration buttons
do not sign in to Codex. If a run reports missing or expired model authentication,
ask the person managing the installation to sign in again.

The default setup uses a ChatGPT subscription sign-in. An installation can also
be configured to use API billing. Connecting GitHub or Linear does not change
that choice. Check with the person managing Circular if you are unsure which
account your runs use.

## Pull request review feedback

In GitHub’s **PR reviews** settings, choose the reviewer and optionally enable **Automatically review published PRs**. This starts additional model work for future PRs published by Circular. Linked Linear issues receive a short review summary when **Publish run updates** is enabled. See [Pull request reviews](/docs/pull-request-reviews) for the complete workflow.

## Give Circular its own identity

Select **Enable Circular bot** under GitHub or Linear to publish new activity
with your app’s name and avatar. Your existing connection stays connected.
Follow the short [Circular’s bot guide](/docs/circular-identity) for the provider’s
setup step. Publishing and incoming work keep their separate controls.

## Receive events from GitHub and Linear

In your project's **Integrations**, enable the Circular bot. For GitHub, expand **Incoming events**. For Linear, open **Work from Linear → Verify incoming events**. Select **Configure receiver**. Reception settings belong to the app and are shared by the projects listed there.

Incoming events need a public HTTPS address. Your installation's operator must point that address to Circular's dedicated receiver on port **8001**. An existing HTTPS proxy or a managed tunnel can do this. Keep the console and control API private. The receiver cannot serve the console, MCP tools, OAuth callbacks, or run artifacts.

Enter the public receiver address. Circular configures GitHub's webhook address and signing secret when you save. For Linear, copy the callback address into the app's webhook settings and paste its signing secret into Circular. The field clears after saving. Use **Check reception** after updating the provider.

**Waiting for a verified delivery** means the address is saved but Circular has not received a correctly signed event. **Receiving events** includes the time of the last verified delivery. Saving or checking an address does not create a test event or start work.

A browser OAuth callback can use localhost when the browser and Circular run on the same computer. Provider webhooks are sent by GitHub or Linear's servers, so they require the public receiver address.

## Give Circular work from Linear

After enabling [Circular’s bot](/docs/circular-identity), verify incoming
events and configure [Requests from Linear](/docs/linear-agent). Mentions and
issue delegation then use your selected route, coding agent, model and run mode.
The **Requests** inbox shows received work and anything that needs your attention.
