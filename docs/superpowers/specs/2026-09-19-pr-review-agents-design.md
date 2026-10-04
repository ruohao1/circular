# Dedicated PR reviewers

Status: design approved on 2026-09-19. The [implementation plan](../plans/2026-09-19-pr-review-agents.md)
is prepared for review; product implementation has not started.

Tracking: [ISQ-255](https://linear.app/isqrd/issue/ISQ-255/add-dedicated-pr-reviewer-agents-and-a-review-workflow).

## Goal and agreed behavior

Give Circular an issue and get back a PR with an independent agent review, so the
user can make an informed merge decision. The agreed next step is a dedicated PR
reviewer, separate from the coding agent, with feedback available in both Circular
and GitHub and a short update in Linear.

The first version provides one built-in **PR reviewer** per project, selectable
model and reasoning level, a **Review PR** action, and an **Automatically review
published PRs** project setting. The existing model default is GPT-6-Astra; the
reviewer uses the selected model's catalog default effort unless the user changes
it. No claims about model quality, pricing, or account availability are implied.

Automatic review defaults off, independently of automatic PR publishing. This is
a proposed default: the user approved the review workflow but has not requested
enabling additional automatic model work on any existing project. Creating the
built-in agent or upgrading Circular must not start reviews.

Success means a user can open a Circular-created PR, start its independent review,
read evidence-backed findings on GitHub or in Circular, and see which exact commit
was reviewed. A review of older code must never look current after the PR changes.

## Scope

Include PRs successfully published by Circular into their original repository.
Existing published PRs can be reviewed manually. After project opt-in, newly
published PRs can start a review automatically. Enabling the setting does not
backfill old PRs. The user retains the merge decision.

The first version posts one GitHub review body containing the verdict, actionable
findings with commit-specific file/line links, limitations, and a Circular report
link. Native inline review threads, arbitrary external PR import, security/test
specialist rosters, webhook-triggered re-review, automated fixes, bot approval
identity, and automatic merging are separate follow-ups.

## Current implementation constraints

- `internal/agents/discovery.go` supplies only the Repository discovery preset.
  Its stable preset key and upgrade behavior are the pattern for the PR reviewer.
- `internal/postgres/execution.go` currently provisions every run from
  `repositories.default_branch`. A reviewer must instead use a trusted PR snapshot.
- Coding and review execution can share the existing queue, leases, cancellation,
  event stream, usage accounting, and isolated container lifecycle.
- `internal/migrate/0011.sql` queues GitHub publication for every opted-in successful
  run. Review runs need an explicit purpose that is excluded from this trigger and
  from the manual coding-run publication endpoint.
- The existing Linear transition trigger would otherwise describe a review as a
  coding run. Review updates need their own semantics and the same workspace guard.
- `internal/agenttools` already provides a private, run-scoped MCP spool for
  structured agent output. It can carry a validated review report without giving
  the agent GitHub credentials or the general control MCP.
- The working tree contains earlier uncommitted product changes. Implement this
  feature without reverting, staging, or committing unrelated work.

## Approach

Use a first-class review record linked to an ordinary Circular run. Circular owns
PR inspection, commit selection, launch identity, report validation, and external
publication. The reviewer receives an immutable source snapshot and review context
and returns findings through a small private tool.

Two alternatives were considered. A reviewer prompt on an ordinary coding task
would reuse the UI quickly but would still inspect the default branch and could
enter the PR publication trigger. A separate review executor or external bot would
duplicate the queue and execution controls and require another integration setup.
The selected approach reuses execution while making review-specific rules explicit.

## User experience

### Project settings

In Setup → Integrations → GitHub, add **PR reviews** with an enabled reviewer picker,
its selected model and reasoning level, and **Automatically review published PRs**.
Use the existing model configuration UI and shadcn components. The default reviewer
is editable and can be disabled; upgrades must preserve those choices.

Explain that enabling automatic review starts an additional agent run after a new
PR is published. Disabling it prevents future automatic launches and cancels
automatic reviews that have not started. An in-progress review can finish and has
an explicit cancel action. Manual reviews remain available when automation is off.

An unavailable reviewer or permission failure has a specific recovery action.
Never substitute another reviewer or model silently. A disabled default reviewer
stays disabled until the user changes it.

### Published run and review report

On the coding run's GitHub card, show **Review PR** and the latest review status.
The launch action identifies the reviewer, model, and reasoning level. Repeated
clicks or response retries refer to the same launch intent.

The linked reviewer run has its own normal run page, a **PR review** badge, a link
to the coding run and PR, live output, duration/usage, and a structured report.
Show the reviewed commit, last freshness check, reviewer/model snapshot, finding
severity, file/line references, checks performed, and limitations. Reports remain
available even when GitHub or Linear publication fails.

Use these user-facing states:

| State                 | Meaning and action                                                                              |
| --------------------- | ----------------------------------------------------------------------------------------------- |
| Not reviewed          | Start Review PR.                                                                                |
| Queued                | The independent reviewer run is waiting.                                                        |
| Reviewing             | Show progress and cancellation.                                                                 |
| Findings              | The review completed and reported actionable findings.                                          |
| No blocking findings  | The review completed without blocking findings; show any advisory findings and coverage limits. |
| Incomplete            | Source coverage or structured output was insufficient for a completed assessment.               |
| Failed / Cancelled    | Show the execution outcome; retry explicitly starts another reviewer attempt.                   |
| Outdated              | The PR head or base changed; preserve the old report and offer Review latest commit.            |
| Freshness unavailable | GitHub could not be checked; show when it was last checked and a refresh action.                |

Publication has a separate status: pending, published, retrying, uncertain, or
skipped. A GitHub outage must not turn a completed review into a failed coding run.
**Retry publication** never starts a model run. **Review again** does and is clearly
labelled as a new attempt.

## Identity and persistence

Add an additive migration following schema 0011. Existing runs default to the
coding purpose. Preserve all existing project, agent, task, run, artifact, and
integration records.

- Add `runs.kind` with `coding` and `pr_review`. Public ordinary run creation cannot
  choose `pr_review`; only the review launcher can create that kind with its matching
  review record. A reviewer run can retain the originating task association without
  creating duplicate issue imports or changing the task's acceptance criteria.
- Add project review settings with automatic execution off and the selected reviewer.
- Store a review's source run/task, project and repository identity, GitHub installation
  and repository IDs, PR number/URL, base/head commit SHAs, merge-base SHA, immutable
  reviewer instructions/model/effort snapshot, input fingerprint, linked run ID,
  report artifact, coverage/verdict, and freshness metadata.
- Store launch intents keyed by project, source run, and client request key with a
  parameter fingerprint. Replaying a key with changed parameters is a conflict.
- Store the GitHub publication intent/receipt separately from model execution.
  Use the existing Linear outbox with a review-specific phase and review run ID.

The logical review identity includes the verified repository and PR, base SHA,
head SHA, and reviewer configuration fingerprint. Normal manual and automatic
launches reuse an existing review of that identity. An explicit new attempt creates
a new review linked to the previous attempt and requires a fresh request key.
Automatic processing never retries failed model execution by itself.

Separate run execution state, report verdict/coverage, freshness, and publication
state in storage. UI labels are a projection of those fields, not a single enum
that conflates failure, findings, and stale code.

## Launch and automatic scheduling

1. Validate that the source run has a delivered PR, the repository still belongs
   to the same project, and the selected enabled reviewer belongs to that project.
   The originating coding agent cannot be selected as its own reviewer role.
2. Read the live PR through the existing trusted GitHub connection. Verify numeric
   repository identity, PR number, same-repository head/base, and open state.
   Closed or merged PRs retain their reports but cannot launch a new review in v1.
3. Persist the live base/head identity, originating task text, reviewer configuration,
   and launch intent. Under a database uniqueness/locking fence, allocate the review
   record and ordinary queued run atomically.
4. Automatic scheduling records a durable intent in the same transaction that records
   successful PR delivery. Draining that intent rechecks settings and reviewer
   availability before creating model work. Replayed PR receipts cannot enqueue twice.
5. Snapshot resolution and provider retries may retry automatically before a run starts.
   Once model execution starts, normal run recovery applies: interrupted execution
   is reported, not silently replayed.

The first version automatically reviews once after Circular publishes a PR.
New commits detected later mark prior reports outdated; re-review of those commits
is manual in v1. This keeps the initial setting's behavior predictable without
introducing webhooks or unbounded automatic feedback loops.

## Exact source snapshot and runtime

The trusted worker obtains the persisted PR head and base commits from the verified
GitHub repository using the existing credential adapter and repository lock. It must
validate fetched object identities and retain the required objects under trusted
review refs. A moving PR ref must not replace the requested commit: if the requested
snapshot is no longer obtainable, fail visibly or prepare a new review of the new
head through the normal launch path.

Compute the merge base locally from the captured base/head and materialize the PR
comparison from merge base to head. Persist a manifest and checksummed diff, including
renames, deletions, binary indicators, and changed line ranges. Record the captured
base as well as merge base, since they have different meanings.

Provision the source worktree at the exact head SHA and mount it read-only at
`/workspace`. Mount a separate trusted input bundle read-only at `/review-context`.
That bundle contains the original task and Linear references, PR metadata, manifest,
diff, and retained coding-run test evidence with its provenance. Do not place input
files into the tracked source tree or expose the shared Git cache.

Use writable temporary storage for the model process, output spool, and optional
checks on an isolated scratch copy. Test results reported by the coding agent are
evidence from that run, not tests rerun by the reviewer. First-version review does
not install dependencies or change source; it reports checks unavailable in the
current environment. Network/model authentication retains the existing runtime
policy; GitHub and Linear credentials remain in trusted services.

Review purpose, mounts, and input identity participate in runtime policy validation
and recovery checks. Ordinary callers cannot request arbitrary mounts or commit refs.
The review must have a finite execution limit of 15 minutes in v1 and use the existing
CPU/memory limits and cancellation controls. Cancellation or a time limit cannot
produce a completed clean verdict.

Review runs retain their report and evidence through the existing artifact lifecycle.
They do not produce a publishable coding diff. Both automatic and manual PR delivery
reject review runs, even if an unexpected local mutation or artifact appears.

## Structured report

Add a private, run-scoped `submit_pr_review` tool. It saves a validated report through
the same spool/event pattern as agent proposals. The tool receives no provider
credentials, publication authority, or permission to launch other runs. It is present
only for review workloads; agent proposal creation is not part of this role.

The report contains:

- A concise summary and coverage status: `complete` or `incomplete`.
- Findings with severity (`critical`, `high`, `medium`, `low`), title, repository-relative
  path, side (`base` or `head`), start/end lines, evidence, consequence, and suggested fix.
- Checks with command or method, outcome, and evidence or reason not run.
- Coverage limitations, including unsupported binary/submodule content and unavailable
  test infrastructure.

Circular derives the verdict from validated findings and coverage. Critical/high/medium
findings are blocking recommendations; low findings are advisory. An incomplete report
cannot become No blocking findings. A clean report means no blocking finding within the
stated reviewed scope, not a guarantee that the software has no defects.

Validate UTF-8, bounded text, safe repository-relative paths, known manifest entries,
and positive line ranges in the captured base/head files. New findings must be relevant
to the PR changes; reject forged source identities. IDs, commit SHAs, and publication
URLs come from trusted context, not model output. Repeated identical submission is safe;
a conflicting second submission must be rejected rather than silently replacing output.

V1 bounds: 32 MiB captured diff, 1,000 changed files, 256 KiB structured report,
50 findings, and 50 check records. Oversized inputs fail before model execution with
clear guidance to split the PR. Unsupported content is visible as a coverage limitation;
it is never silently treated as reviewed. Evidence/source corruption fails the review.

A valid report plus successful process completion is required for a completed review.
Missing/invalid structured output is Incomplete even when the model exits successfully.
A report emitted before a later execution failure remains partial and cannot publish a
completed verdict. Preserve available output for diagnosis.

A valid report with incomplete coverage is still useful feedback: its review assessment
is Incomplete, and any published summary must state that limitation prominently. Execution
success, completed coverage, and a clean verdict remain separate facts.

## GitHub and Linear publication

After retaining a valid report from successful execution, re-read the PR head and base. If they changed,
mark the report outdated and do not newly post it as a current review. If freshness cannot
be checked, keep publication pending and expose the reason. A change after this check
cannot be made atomic with GitHub publication; every comment therefore names and binds
to the exact reviewed head and clearly limits its assessment to that commit.

Use `POST /repos/{owner}/{repo}/pulls/{number}/reviews` with `event: COMMENT` and the
captured `commit_id`. The existing Pull requests write permission supports this call.
See [GitHub's review endpoint](https://docs.github.com/en/rest/pulls/reviews#create-a-review-for-a-pull-request).

The body identifies **Circular PR reviewer**, reviewer/model, verdict, findings with
commit-specific file/line links, checks/limitations, and the Circular report. Bound the
GitHub body to 30,000 characters; if needed, include the highest-severity findings and an
explicit omitted count with the complete report link. It must not silently omit blocking
findings while claiming a clean review. This review body is the v1 GitHub feedback surface;
native inline comments are deferred.

Circular currently uses a connected user's GitHub App user token. GitHub does not allow
authors to approve their own PRs, so a review assessment is not a GitHub approval and does
not satisfy required-review rules. See [GitHub's approval rules](https://docs.github.com/en/pull-requests/how-tos/review-pull-requests/approving-a-pull-request-with-required-reviews).
No new bot identity, approval event, permission grant, or merge is introduced.

Persist a publication marker and started flag before the GitHub request. Recovery lists
reviews across pages and matches the review marker, repository/PR identity, commit, and
expected author before accepting a receipt. An ambiguous request with no matching receipt
becomes uncertain; retry checks for the result without blindly submitting another review.
Definitive rejection can be retried after correcting permissions or validation.

If Publish run updates is enabled for the source project's connected Linear workspace,
enqueue a concise **PR review completed** or **PR review incomplete** update with the
actual coverage/verdict, finding counts, and links.
Do not post raw logs or full source. Prefer the GitHub review link when available and keep
the Circular report link usable if GitHub publication is delayed. The durable Linear phase
is unique per review run, so retries do not post another completion comment. A later
GitHub receipt can update the same pending body; already delivered updates are not duplicated.
Disconnection/workspace change follows the existing skip/reconnect policy.

## Freshness after completion

Store the latest observed head/base and check time independently of the immutable reviewed
snapshot. Refresh freshness when opening the review, before publication, on explicit refresh,
and through a rate-limited background check for tracked open PRs. Background checks are no
more frequent than once per minute per PR, stop for known closed/merged PRs, and do not start
model work. Multiple API processes share a durable lease and respect provider rate limits.

The UI displays Last checked and immediately marks a detected version change outdated.
On provider failure, display Freshness unavailable rather than asserting that the old report
is current. Existing GitHub comments retain their commit-specific historical context.

## HTTP, MCP, and module ownership

The public interface is project-scoped review settings, preparation/launch for a source run's
published PR, review inspection/history, publication retry, and normal run cancellation.
Clients use durable request keys for any operation that can start model work. Read operations
never launch or publish. The read-only MCP omits launch, settings changes, and publication retry.

Keep orchestration in trusted Circular code, not in prompts. Reuse the following modules:

| Module                                                               | Responsibility                                                                                                 |
| -------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `internal/agents`                                                    | Built-in reviewer preset and customization-preserving upgrade.                                                 |
| `internal/prreviews` (new)                                           | Review identities, context/report validation, fingerprints, and private report spool.                          |
| `internal/postgres`                                                  | Atomic launch, snapshots, report persistence, leases, and retry identities.                                    |
| `internal/integrations`                                              | Verified GitHub PR reads, review publication/recovery, automatic intents, freshness checks, and Linear outbox. |
| `internal/git`                                                       | Verified PR objects, merge-base/diff manifest, and exact-commit worktree.                                      |
| `internal/execution`, `internal/runtimes`                            | Review purpose, read-only input/source mounts, bounded execution, cancellation, and retention.                 |
| `internal/backends`, `internal/codexworkload`, `internal/agenttools` | Review prompt/context, private report tool, and validated report events.                                       |
| `internal/httpapi`, `internal/controlmcp`, `contracts`               | Thin validated routes/tools with the same launch and retry semantics.                                          |
| `apps/web`                                                           | Project opt-in, reviewer selection, progress/report UI, freshness and publication recovery.                    |

The implementation plan will define the exact route/tool names and task-level function
signatures against these interfaces. User documentation remains under `docs/user-guide`;
this technical design is not part of the public Fumadocs navigation.

## Validation and delivery

Use owned provider fixtures and disposable databases for normal tests. Cover:

1. Upgrade preserves old agents/configuration and creates exactly one reviewer preset per
   project without queueing model work. A custom agent with the same name is preserved.
2. A seeded PR defect yields a finding at the correct head/base location; a clean change can
   complete without blocking findings. Missing output and incomplete coverage are distinct.
3. Default branch movement, force-pushed heads, base changes, deleted branches, renamed/deleted
   files, binary changes, and retained object corruption never substitute different code.
4. Concurrent manual/automatic launches and lost responses allocate one review/run per normal
   intent; explicit re-review makes one new attempt. Publication retry never launches a model.
5. Reviewer/model changes after queueing cannot alter the stored execution snapshot. Wrong-project,
   disabled, originating-coder, and unavailable reviewers have actionable failures.
6. Review source/input mounts are read-only. Cancellation, timeout, crash recovery, and retention
   preserve available evidence and never queue coding publication or recursive review work.
7. GitHub COMMENT publication uses the reviewed commit and recovers its exact receipt. Ambiguous
   responses cannot duplicate comments; revoked access and rate limits remain recoverable.
8. Linear opt-in, workspace identity, and one-comment semantics remain intact. Review runs do not
   emit misleading coding-run transition comments.
9. API/MCP permissions and request keys match, and the browser supports manual/automatic launch,
   progress, findings, incomplete/outdated states, publication retry, and mobile layout.
10. Oversized inputs, invalid paths/lines, duplicate/conflicting reports, stale provider reads,
    and inaccessible original artifacts fail clearly instead of producing a clean assessment.

After design and implementation review, rebuild/migrate the local stack while preserving all
existing records. Keep review automation off on upgrade. A separately authorized live review of
`circular-test` PR #1 can verify one real reviewer run and the GitHub/Linear feedback, without
merging or launching a new coding task.
