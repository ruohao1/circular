---
title: Pull request reviews
description: Ask a dedicated reviewer to assess a pull request, read its findings, and follow feedback in GitHub and Linear.
---

Circular includes a **PR reviewer** agent for each project. You can choose its model and reasoning level, edit its instructions, and decide when it runs. The default model is Astra.

## Review a pull request

For a Task using **Review required**, open **Tasks → Task details → Task completion**.
After the coding result is accepted, bind its PR number if necessary, then select
**Review PR**. This also works for tracked external coding work; no Circular coding
Run is needed. The reviewer runs independently and keeps its own report.

1. Open a completed coding run with a published pull request.
2. Select **Review PR**, check the reviewer, model, reasoning level, and commit, then select **Start review**.
3. Open the reviewer run to follow progress and read its findings.
4. Read the same assessment on GitHub. If Linear updates are enabled, the linked issue also receives a short review update.

This starts an additional agent run. The reviewer must be a different agent from the one that made the changes. You can cancel a queued or running review with **Cancel Run**.

Each review names the exact commits it examined. Its report includes a summary, findings with file and line links, checks, and limitations. The source files stay read-only during a review.

The reviewer can use Git to verify the captured commit, check the source status, and compare the PR against its merge base. Each review receives its own read-only Git metadata for the captured commits and their ancestry. The shared repository cache and GitHub and Linear credentials stay outside the review container.

## Understand the result

| Result                      | Meaning                                                                                                                                |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| **Findings**                | The reviewer reported at least one critical, high, or medium issue.                                                                    |
| **No blocking findings**    | The reviewer completed its coverage without reporting a critical, high, or medium issue. Low-severity suggestions may remain.          |
| **Incomplete**              | Some coverage was unavailable, or the reviewer did not submit a complete structured report. Read the limitations before relying on it. |
| **Failed** or **Cancelled** | The review did not finish successfully. Any available partial report remains visible.                                                  |
| **Outdated**                | The PR's base or head changed after this review. The original assessment stays available for its original commits.                     |
| **Freshness unavailable**   | Circular cannot currently verify whether the PR still matches. Use **Refresh PR status** after checking the GitHub connection.         |

A completed run and a published comment are separate states. You can read findings in Circular while GitHub or Linear publication is waiting or needs attention. An assessment is not a guarantee that the changes are defect-free.

## Task completion requirements

Before starting work, save the Task and choose its completion policy and keyed
requirements in **Task completion**. The first work claim freezes them. To change
a frozen contract, plan a new Task; reopening does not relax its requirements.

- **Direct completion:** a successful coding result with evidence for every
  requirement moves the Task to Done after ownership and cleanup are released.
- **Review required:** the same accepted result moves the Task to Review. It
  additionally needs a PR in the connected Repository, the selected CI jobs, and
  a current independent review. A successful Run or the agent's final text alone
  does not complete the Task.

In **Setup → Repositories → Required completion checks**, select and save the
required GitHub Actions jobs before starting review-required work. Existing
Tasks keep their frozen selection. Circular suggests its three CI jobs only for
the Circular repository; suggestions still need an explicit save.

**Ready to merge** means the server verified the latest selected CI attempts and
a complete retained independent review of the current base and head, without
medium, high or critical findings. Low findings remain visible as advice. This
readiness is separate from the planning stage **Ready**. Starting a newer review
replaces the earlier review for this decision, even while the newer one is queued.

Publish the PR manually from its successful Run, or bind an existing PR by number.
Automatic draft publishing skips policy-managed work. A review-required Task
still needs a PR when the coding Run has no changes.

Merge the PR in GitHub, refresh completion evidence, then explicitly **Move to
Done**. Circular verifies the merge against the tested tree; it never merges or
moves review-required work to Done automatically. Uncertain revision proof,
missing permissions, stale checks or changed commits keep completion blocked.
Use the displayed setup, refresh, binding and review controls to resolve the
blockers. The final Done request refreshes evidence and rechecks the current
attempt; it cannot finish work superseded by another agent.

## Review new changes or try again

If the PR changes, select **Review latest commit** to review its current version. **Review again** starts another attempt, even for the same commits. Both actions start additional model work.

**Retry publication** retries the existing feedback without running the reviewer again. If GitHub may have received a request whose response was lost, the action is **Check publication result**. Circular looks for that review's receipt instead of posting a duplicate. The findings remain available in Circular during recovery.

After a connection interruption during launch, reopen **Review PR** and use **Check review request**. Circular reuses the earlier request so a lost response does not create a second run.

## Choose the reviewer and model

In **Setup → Integrations → GitHub → PR reviews**, choose an enabled Codex agent in **PR reviewer**. Select **Edit model** to choose the model and reasoning level, then **Save model**. Agent instructions can be edited in Setup.

Changing the selected agent or model affects future reviews. Existing reviews keep the reviewer and model settings they started with. If a selected reviewer is disabled or missing, choose an available reviewer explicitly.

## Review new pull requests automatically

Turn on **Automatically review published PRs** in the same settings card. This starts additional model work after Circular publishes a new pull request. It does not review older pull requests or every new push automatically.

Turning this off cancels pending automatic launches and automatic review runs that are still queued. A review already running can finish. You can still start a manual review.

## Follow feedback in GitHub and Linear

Circular posts a GitHub comment review tied to the reviewed commit. File links open the relevant version of the file. You still decide whether to merge, and the assessment does not replace a required GitHub approval.

For a task linked to a Linear issue, enable **Publish run updates** in the Linear connection to receive a short completion summary with the assessment, counts, commit, and Circular link. A GitHub link is included when available before the Linear update starts delivery.

## When a review needs attention

- **Reviewer unavailable:** choose an enabled Codex reviewer different from the source coding agent.
- **Permission problem:** check GitHub App repository access and Pull requests permission, then refresh permissions in Setup.
- **Incomplete:** read the checks and limitations. A missing report is not a clean result.
- **Execution limit reached:** reviews have a 15-minute execution limit. Split a large PR into focused changes and try again.
- **PR too large:** split changes if the review exceeds 1,000 changed files or a 32 MiB diff. Binary files and submodules are reported as coverage limitations.
- **Outdated:** keep the old report for reference and use **Review latest commit**.
- **Publication uncertain:** use **Check publication result**. Starting another reviewer run is a separate decision.

See [Connections](/docs/connections) for connection setup and [Troubleshooting](/docs/troubleshooting) for recovery steps.

## Reviews of work requested in Linear

When a run came from a native Linear agent session, Circular attaches its PR and
review updates to that session. It does not also add the ordinary run-summary
comments. Stopping the original request stops active linked reviews and cancels
reviews that have not started. Existing published reviews remain on GitHub.
