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
| `set_github_publishing`, `publish_run_pull_request`                            | Opt future successful runs into automatic draft PRs, or publish an existing successful run. Publication is asynchronous; retry with the same run ID.                        |
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
`internal/agenttools` remains the private proposal MCP inside Codex workloads,
with only `list_models` and `propose_agent`. Run containers are not given this
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
