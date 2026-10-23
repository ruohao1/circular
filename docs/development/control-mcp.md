---
title: "Control Circular from a coding agent"
description: "Connect an external coding agent to Circular using its local MCP server."
---

Circular includes a Streamable HTTP MCP server in the API service. It calls
Circular's public HTTP API, so the console displays the same projects, tasks,
agents, runs, events and proposals. Model execution uses the worker's existing
connection, including its ChatGPT subscription login. MCP setup needs no new
model API key or provider login.

Open **Setup → MCP** (`/setup?section=mcp`) for a copyable URL or Codex registration
command. The connection covers all projects in the instance. No extra build,
folder entry, or process is required for the default connection.

## Bundled HTTP connection

The API serves full control at `/mcp` and inspection-only tools at `/mcp/read-only`.
With the default local configuration:

```sh
codex mcp add circular --url http://localhost:8000/mcp
```

Start a new Codex session after registering. Use `/mcp/read-only` instead for
inspection tools. Both endpoints are stateless Streamable HTTP with JSON
responses, an explicit Host/Origin allowlist, and a 256 KiB request limit.
Resource calls dispatch through the same API handlers in-process, preserving
the validation, project scope, and retry protection used by the console.

The native API binds to `127.0.0.1:8000`; the Docker image listens on its container
interface while Compose publishes the API port on host loopback by default.
The public API URL determines allowed MCP hosts, including equivalent loopback
addresses when configured locally. Browser Origins must match the API, console,
or an explicitly configured CORS origin; wildcard CORS does not permit arbitrary
MCP origins. Native clients may omit Origin. Forwarded headers do not override
these checks. There is no user authentication; deliberate network exposure needs
the same access boundary as the rest of the API.

`GET /api/v1/mcp/connection` returns the connection URLs and recent successful
client activity. Activity contains bounded client name/version, access mode, and
timestamp, is held in memory, and resets on API restart. It is not evidence of a
persistent connection or authenticated identity.

The standalone `circular-mcp` command remains available under **Advanced: use a
local server** for clients that require stdio. These alternatives are described
below.

## Docker Compose

Keep the normal Circular stack running. From the repository directory, build
the optional image once:

```sh
docker compose --profile mcp build mcp
```

Register it with Codex, replacing `/absolute/path/to/circular` with your checkout:

```sh
codex mcp add circular -- docker compose \
  -f /absolute/path/to/circular/compose.yaml --profile mcp \
  run --rm --no-deps -T mcp
```

Start a new Codex session after registering the server. The MCP process starts
on demand, talks to `http://api:8000` on the Compose network, and exits when its
client closes stdin. Its container is non-root, read-only, has no extra
capabilities, and receives no volumes, provider credentials or Docker socket.
The **host-side** Docker command still requires access to your Docker daemon.
The standalone stdio process opens no listening port.

To expose inspection tools only, pass the complete server command (Compose
arguments after the service name replace its default command):

```sh
codex mcp add circular -- docker compose \
  -f /absolute/path/to/circular/compose.yaml --profile mcp \
  run --rm --no-deps -T mcp --api-url http://api:8000 --read-only
```

Add `--web-url` if the console uses an origin other than `http://localhost:5173`.
The Setup page generates this argument using the console's current origin.

## Native binary

With Go 1.27.1 or later:

```sh
mkdir -p dist
go build -o dist/circular-mcp ./cmd/circular-mcp
codex mcp add circular -- /absolute/path/to/circular/dist/circular-mcp \
  --api-url http://localhost:8000 --web-url http://localhost:5173
```

Other stdio MCP clients can use this entry (merge into their configuration):

```json
{
  "mcpServers": {
    "circular": {
      "command": "/absolute/path/to/circular/dist/circular-mcp",
      "args": ["--api-url", "http://localhost:8000"]
    }
  }
}
```

Some clients use a different top-level key. The command and argument array stay
the same. The binary accepts `--read-only`, `--api-url`, `--web-url` and `--check`.
It does not read `.env`, personal Codex settings, or provider credentials. URLs
are startup configuration, never tool arguments; credentials, queries and
fragments in these URLs are rejected. Diagnostics go to stderr, leaving stdout
for MCP messages.

`--check` checks API health and retry-safe launch support without creating any
records or starting MCP. For example:

```sh
dist/circular-mcp --api-url http://localhost:8000 --check
docker compose --profile mcp run --rm --no-deps -T mcp \
  --api-url http://api:8000 --check
```

The API must include migration `0008` and its matching OpenAPI contract. The MCP
server refuses to launch against an older API that would ignore the retry key.
Circular's resource API currently has no user authentication; its existing
trusted-local-network deployment model also applies to MCP. Read-only mode is a
restriction on this MCP connection, not an authorization layer on the HTTP API.

## Tools and workflow

| Tools                                                                          | Behavior                                                                                                                                                                    |
| ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `list_projects`, `list_repositories`, `list_agents`, `list_tasks`, `list_runs` | Inspect bounded collections. Use `next_offset` when `has_more` is true. Repository, agent and task lists require a project ID. Runs can be filtered by project and/or task. |
| `list_models`                                                                  | Read the API's model catalog and supported reasoning levels. Astra is the default. Account availability may differ.                                                         |
| `get_task`, `get_run`                                                          | Read the objective or execution snapshot. Run details include proposals, artifact IDs, usage and a console link.                                                            |
| `get_run_events`                                                               | Replay ordered output after a sequence, with `next_after` for the next request. Raw backend envelopes are omitted.                                                          |
| `read_artifact`                                                                | Read UTF-8 text such as a final diff in character-based chunks. Binary artifacts remain downloadable through the console.                                                   |
| `create_project`, `add_repository`, `create_agent`, `update_agent_model`       | Set up the project and its team; choose model and reasoning effort explicitly. New Codex agents default to Astra when omitted.                                              |
| `list_github_accounts`                                                         | List installed GitHub accounts and creation permissions for a project. Use `next_page` to continue.                                                                         |
| `create_github_repository`                                                     | Create an actual GitHub repository with a README and attach it to the project. Private by default; retain the same `request_key` and parameters when retrying.              |
| `get_github_publishing`, `get_run_pull_request`                                | Inspect project publishing settings and a run's delivery status and PR link.                                                                                                |
| `set_github_publishing`, `publish_run_pull_request`                            | Control legacy automatic drafts, or manually publish a successful Run; policy-managed Tasks always require manual publication. Publication is asynchronous; retry with the same run ID.                        |
| `create_task`, `prepare_discovery`                                             | Prepare work without starting execution. Discovery returns its supplied agent ID.                                                                                           |
| `start_run`, `cancel_run`                                                      | Queue actual execution or cancel queued/active work. A launch requires a stable `request_key`.                                                                              |
| `propose_agent`, `create_proposed_agent`, `dismiss_agent_proposal`             | Save a recommendation for review, create the reviewed agent, or dismiss it. Agent creation does not start execution.                                                        |

Read-only mode exposes inspection tools and omits mutation tools. MCP annotations identify reads, changes, and retry behavior.
Full control can start model work; use the client's own approval preferences to
decide which calls require review.

GitHub repository creation requires migration `0010`, a connected GitHub App,
and approved Administration write permission on the selected installation. It
performs an external write and is available only in full-control mode. A
`needs_access` result means the acknowledged repository still needs attachment;
retry the same request after restoring access. An `uncertain` result never
repeats GitHub's create request: inspect GitHub and import the repository manually
if it exists. Do not replace its request key to retry an ambiguous result.

Draft PR delivery requires migration `0011`, approved Repository Contents and
Pull requests write permissions, and access to the run's retained diff and
original Git base. Automatic publishing is off by default and applies to future
successful runs. `publish_run_pull_request` queues delivery of one existing run;
poll `get_run_pull_request` for its result. Reusing the run ID resumes the same
delivery. An uncertain PR response is reconciled by inspecting the dedicated
branch and PR marker; it never blindly sends another create request. These tools
do not merge PRs or change the repository's default branch.

An example first request:

> Use Circular to inspect my project and its agents. Prepare a small coding task
> and show me the task, agent, model and reasoning level before starting it.

The typical sequence is to select existing IDs, prepare the task, call
`start_run`, then inspect `get_run` and `get_run_events` until the run reaches a
terminal status. Poll at reasonable intervals. For private GitHub repositories,
use the console's integration import so installation identity is retained.
OAuth configuration, provider secrets, deletion, and arbitrary HTTP/file access
are not exposed as tools.

## Launch retry contract

`POST /api/v1/runs` accepts an optional `request_key` of 1–200 nonblank characters,
scoped to the Task. Keys are trimmed before persistence. MCP requires one:

```json
{
  "task_id": "<task UUID>",
  "agent_id": "<agent UUID>",
  "request_key": "<task UUID>:first-pass"
}
```

The first request creates a Run and returns HTTP 201. The same Task, key, Agent
and `external_refs` return the existing Run with HTTP 200, including after it
finishes or the MCP process restarts. Reusing the key with a different Agent or
references returns 409. A fresh key deliberately creates another attempt;
unkeyed HTTP requests preserve the existing new-attempt behavior.

The API locks the Task before checking the key and allocating an attempt, and a
unique index enforces `(task_id, request_key)`. This survives concurrent clients
and lost responses without relying on a process-local cache. Replay happens
before mutable Agent availability checks. Existing Runs receive a null key.

Other creation tools are not automatically retried. If a network response is
uncertain, inspect current state before repeating them. Proposal acceptance and
cancellation retain their existing idempotency rules. The HTTP adapter never
follows redirects or retries requests, has a 30-second timeout and an 8 MiB
response cap, and bounds MCP output. Artifact chunks contain at most 32,000
Unicode characters; collection pages contain at most 100 records and 256 KiB.

## Separation from the in-run proposal server

`internal/controlmcp` and `cmd/circular-mcp` serve external clients.
`internal/agenttools` remains the private MCP inside Codex workloads,
with `list_models`, `propose_agent`, and role-scoped `delegate_task` or
`submit_pr_review`. Run containers are not given this
control server, its API address, or authority to start more work. Saved draft
recommendations still enter the same console review flow.

Tests exercise the MCP protocol against a real disposable API/database,
creation and model selection, launch replay across sessions, proposal review,
cancellation, artifact integrity and scoping, invalid input, old API rejection,
HTTP failures and read-only mode. Browser coverage checks the setup commands,
clipboard, access selection, API failure recovery and mobile layout. These
tests use fake execution and do not call a model provider.

## PR review workflow

The control server exposes `get_pr_review_settings`, `prepare_pr_review`,
`list_pr_reviews`, `get_pr_review`, and `refresh_pr_review` in both access modes.
Refresh checks GitHub through a shared per-PR throttle and starts no execution or
publication. It is the sole POST action allowed by the read-only HTTP client.

Full-control connections additionally expose `set_pr_review_settings`,
`launch_pr_review`, and `retry_pr_review_publication`. Prepare a delivered source
coding run, then pass its `input_fingerprint` and a stable UUID `request_key` to
launch. Retries reuse that key and payload. An intentional new attempt uses a new
key, `mode=again`, and `previous_review_id`. Automation is off by default and
applies only to future PR deliveries. Publication retries reconcile the same
receipt; they never start another model run.

These tools call the public `/api/v1` review endpoints and inherit their project,
source/reviewer, origin, immutable identity, and credential checks. Only the
trusted launcher creates `runs.kind=pr_review`; ordinary run creation remains
coding. The private runner tool `submit_pr_review` belongs to a separate server
and is never exposed by the control MCP.


## Native Linear requests

Use `circular_list_requests` (a project or `unrouted=true`) and
`circular_get_request` to inspect received work. Full control can call
`circular_start_request` with the current `expected_input_fingerprint`, or
`circular_stop_request`. Start preserves one run per session; stop also fences
future provider writes and descendant reviews. App setup, signing secrets and
route enablement remain console-only.

## Delegation policy and observation

`get_delegation_targets` accepts `agent_id` and is available in both access modes.
Full control also exposes `set_delegation_targets`, accepting `agent_id` and
`target_ids`. The operation replaces the complete allowed set; `[]` disables
future delegation admissions. Only enabled, same-Project specialists are allowed.
Policy is owner-controlled and unavailable to Run containers. Setting policy does
not start work, and revoking it does not cancel an already admitted child.

An opted-in parent may request one child through its private `delegate_task` tool.
`get_run` exposes the same `invocation` number and `delegation` linkage as the Run
execution API, and existing event reads show suspension and continuation. The
parent stays in the same Run and attempt while waiting. It automatically continues
only after the child has terminated and released its resources. The usage response
keeps its existing shape and sums parent invocations only.

Use the existing cancellation tool on the parent to stop both unfinished Runs and
prevent continuation. Cancelling only the child resumes the parent with a cancelled
outcome. Automatic GitHub/Linear publication belongs to the root; explicit manual
publication retains its existing authorization. See
[Codex backend](codex-backend.md#bounded-specialist-delegation) for limits,
checkpoint semantics, rollout and recovery diagnostics.

## Task completion and external work

`get_task_completion` is read-only and returns the frozen policy, accepted report,
bound PR, observation age, required checks, latest independent review and stable
blocker codes. `prepare_task_pr_review` and `list_task_pr_reviews` are also
available read-only. Preparation verifies inputs but starts no model work.
For a completed Task, the verdict describes the accepted historical result
(`next_stage=completed`, `merge_readiness=not_applicable`); observation expiry or
a later GitHub reconnect does not invalidate it. Reopening requires fresh tracked
work. Completed Tasks retain their evidence and review history without refresh,
rebinding or new-review controls.

Full control additionally exposes `refresh_task_completion`,
`bind_task_pull_request` and `launch_task_pr_review`. Bind only a positive PR
number with the current Task revision and completion session ID; the server
resolves its Repository and provider identity. Rebinding clears earlier readiness.
Prepare a review first, then launch with its fingerprint and a stable request
key. Retry an uncertain launch with identical parameters. These Task review
tools support both native and tracked external coding work; the existing Run
review tools remain compatible.

Plan a policy and keyed requirements through Task details or
`PUT /api/v1/tasks/{id}/completion-policy` before first work. The API requires an
expected Task revision and refuses edits after the first claim freezes the
contract. Repository CI configuration is also explicit and revision-checked.
`move_task` to Done refreshes provider evidence and applies the shared guard;
`completion_blocked` errors include actionable blocker codes. It never releases
an active owner or trusts client-supplied check results.

The in-run coding-only `submit_task_result` tool accepts a result and evidence
for the frozen requirement keys, plus an optional PR number. It accepts no Task,
Project or session identity. The server binds it to the current invocation;
native completion waits for terminal success and safe cleanup. Discovery uses
the explicit direct-completion `discovery_report` preset; delegated children use
the direct `handoff` preset. Parent continuation follows delegation outcome and
still requires the parent's final-invocation completion report.

ISQ-474 adds external claim/heartbeat/finish tools using `WorkFinishInput.Report`.
ISQ-478 adds Linear workflow status sync through guarded `MoveTask`. ISQ-476 can
document a fixed client skill/plugin after that lifecycle exists. This release
does not claim those external lifecycle tools are already present.

## Task planning tools

`list_task_planning` returns
bounded groups (up to 50 Tasks per stage by default, at most 100) with totals,
Run summaries and revision-bound cursors. Supply a status when continuing a group's
cursor, retaining Project and all filters. Default groups exclude cancelled;
request `status: "cancelled"` explicitly. A stale cursor requires a fresh first page.
`get_task_planning` returns editable-field capabilities, both revisions and the
Task detail console URL. Both tools are available read-only.

`create_task` accepts optional `priority` and `preferred_agent_id`; it does not
start a Run. `update_task` edits only supplied fields. Omission preserves a value;
explicit null clears Repository or preferred Agent when allowed:

```json
{"task_id":"<Task UUID>","expected_revision":4,"priority":"high","preferred_agent_id":null,"repository_id":null}
```

`planned_start_date` and `planned_finish_date` are optional calendar dates in
`YYYY-MM-DD` form, years 0001–9999. Finish is inclusive; equal dates mean one day.
Omitting either field preserves it, and null clears it. A partial schedule is
Unscheduled. Whenever either field is supplied (including null or an unchanged
value), also supply `expected_board_revision` from `get_task_planning`:

```json
{"task_id":"<Task UUID>","expected_revision":4,"expected_board_revision":12,"planned_start_date":"2026-10-04","planned_finish_date":"2026-10-06"}
```

Dates and other supplied fields save atomically. A stale Task or planning revision
rejects the whole edit, including a no-op; reload before expressing a new intent.
Dates remain editable on source-managed Tasks or while Runs are active, subject
to the existing locks on other fields. Dates never gate Run launch. Task creation
still starts with no dates; save dates afterward through `update_task`.

`move_task` accepts one placement and both fresh revisions:

```json
{"task_id":"<Task UUID>","expected_revision":5,"expected_board_revision":12,"status":"ready","placement":"end"}
```

Placements are `start`, `end`, `up`, `down`, `before`, and `after`; only the last
two require `anchor_task_id` (a Task in the destination stage and same Project).
Up/down stay within the current stage. Never derive saved positions from a filtered
list index. On HTTP 409, re-read and decide whether to retry; no blind replay.
Source-managed content, active-Run instructions and post-first-Run Repository locks
apply to MCP exactly as to the console. Planning changes never start, cancel or
reschedule Runs, and do not alter their immutable input snapshots or actual Agent.


`list_task_timeline` is available in full and read-only modes. Supply `project_id`,
`from` and `to` as inclusive YYYY-MM-DD dates (at most 366 days). It returns bounded
scheduled and Unscheduled sections with exact totals; the default limit is 20 per
section and maximum 100. Continue with `section` and its `next_cursor`, keeping
filters, window and limit unchanged. A revision conflict requires reloading both
sections. The existing 512 KiB output guard requests a smaller page limit rather
than truncating results. Timeline is a planning read and never starts Runs.
