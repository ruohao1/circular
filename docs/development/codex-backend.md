---
title: "Codex backend"
description: "Enable Codex execution, configure subscription authentication, and review agent proposals."
---

Circular supports `fake` and `codex` Agents. The Codex backend is opt-in on the
worker and uses the pinned Codex CLI 0.153.4 in its own Run container. It edits the
Run worktree, streams completed messages and usage into the existing execution
page, and uses the same final diff, artifact retention, cancellation and recovery
path as the fake backend.

## Enable locally

Build the runner image:

```bash
docker build -f infra/codex-agent-workload.Dockerfile -t circular-codex-runner:dev .
```

Set these worker settings in your local environment or `.env`:

```text
CIRCULAR_CODEX_ENABLED=true
CIRCULAR_CODEX_IMAGE=circular-codex-runner:dev
CIRCULAR_CODEX_AUTH_MODE=chatgpt
```

Subscription authentication is the default. Leave `CIRCULAR_CODEX_API_KEY` unset.
Sign in once using a ChatGPT account with Codex access:

```bash
go run ./cmd/circular-codex-auth login
```

The command displays a device code and sign-in link. Complete that flow in your
browser. Circular saves this login in `.circular/codex-auth`; it never imports
credentials from your existing `~/.codex` directory. Check or clear the login with
`go run ./cmd/circular-codex-auth status` or `go run ./cmd/circular-codex-auth logout`.
These commands need the runner image and Docker, but no running database.

For Compose, build the updated worker and use its bundled command instead:

```bash
docker compose build worker api web
docker compose run --rm --no-deps --entrypoint circular-codex-auth worker login
docker compose up -d worker api web
```

Replace `login` with `status` or `logout` as needed. Use the same native or Compose
execution setup for login and Runs so the directory owner matches the runner UID.
The helper creates a private directory and never changes ownership or permissions
of an existing login. Compose stores it beneath `CIRCULAR_EXECUTION_HOST_ROOT`.

Open **Setup → Agents** in the console, enter a name, and choose **Codex**.
Choose a **Model** and **Variant** (reasoning effort); new Agents default to
**GPT-6-Astra / Low**. Instructions are optional. The Agent uses the worker's
configured connection. You can also register it through the API documentation at
`http://localhost:8000/docs` (the default API origin), using your Project UUID:

```json
{
  "project_id": "YOUR_PROJECT_UUID",
  "name": "Codex engineer",
  "backend": "codex",
  "instructions": "Implement the task and run the relevant checks.",
  "backend_config": {}
}
```

An empty configuration resolves to Circular's pinned `gpt-6-astra` model with
`low` reasoning. Set `backend_config.model` and `backend_config.reasoning_effort`
to override them, for example `{"model":"gpt-6-astra","reasoning_effort":"high"}`.
Known models validate supported efforts and supply their default effort when omitted.
Custom model IDs remain supported; omit their effort to use the CLI's default.
The picker catalog (`GET /api/v1/backends/codex/models`) is checked against the
pinned Codex 0.153.4 runner, not fetched from a user's account. Account access can
differ; update the catalog when upgrading the runner. Choices and semantics follow
the [model catalog](https://learn.chatgpt.com/docs/app-server#models) and
[Codex configuration](https://learn.chatgpt.com/docs/config-file/config-sample).

Use **Edit model → Save model** on an existing Agent, including Repository discovery.
`PATCH /api/v1/agents/{agent_id}` accepts `backend_config` and preserves its other
fields. Settings are read when the worker prepares a Run; changing them does not
start a Run. Select this
Agent and a Repository in the web launcher, then start a Task. A worker with
Codex disabled fails the Run before allocating its Workspace or container.
Workers sharing a queue should use the same backend configuration; there is no
capability-based claim routing.

The selected Project also needs a registered Repository. If the console shows
"No repositories available", select **Add repository** to open its Setup form.
Alternatively, use `POST /api/v1/repositories` in the API documentation with the
same Project UUID:

```json
{
  "project_id": "YOUR_PROJECT_UUID",
  "name": "Smoke test repository",
  "clone_url": "https://YOUR_GIT_HOST/OWNER/REPOSITORY.git",
  "default_branch": "main"
}
```

Use a repository the worker can clone, with an initial commit on the specified
branch. Refresh the console, select the Project, then choose the Repository and
Codex Agent under **New task**. Enter a Task title and click **Start Run**.

## Execution and credentials

The worker passes a bounded JSON request on container stdin to
`circular-codex-workload`. In subscription mode it contains the prompt, model, reasoning effort and
authentication mode, with no tokens or API key. The runner alone reads the saved
login from a dedicated read-write bind at `/codex-auth`. The API and web app do
not receive that directory. Only trusted worker configuration can select its path;
Agent `backend_config` accepts only `model` and `reasoning_effort`. The wrapper
validates both again and passes effort as the Codex `model_reasoning_effort` setting.

`CIRCULAR_CODEX_AUTH_ROOT` is the worker-visible directory; the optional
`CIRCULAR_DOCKER_CODEX_AUTH_ROOT` gives its absolute daemon-visible equivalent.
The directory must have mode `0700`, belong to the runner UID:GID, and be separate
from Repository caches, worktrees and artifacts. Keep the configured daemon path
unchanged until outstanding Runs are released so recovery can verify their mount.

Codex writes refreshed login tokens directly into this persistent directory. A
process-shared file lock serializes Runs, login and logout using the same account,
including across workers sharing that directory. Waiting for the lock is
cancellable. Cleanup removes Run containers and worktrees while preserving login
state. Missing or invalid authentication fails the Run with instructions to log
in again; subscription mode never switches to API billing.
If the server rejects an otherwise valid saved login, Codex reports a regular
Run failure. Inspect its redacted error, then use the login command to reauthenticate.

Each Run has a non-root user, a read-only container root, one writable worktree
mount and a 128 MiB `/tmp` tmpfs. The memory limit must be at least 128 MiB.
Subscription Runs also receive the dedicated auth mount. The wrapper uses a
private temporary home, ephemeral Codex sessions, file-based authentication and
a forced ChatGPT login mode. It ignores host configuration and rules. The CLI
uses `danger-full-access` with approvals disabled inside this container; Docker
supplies filesystem and process isolation.

Codex Runs use bridge networking to reach the provider and tools invoked by the
task. Code running as the same user can access the login directory, so this local
backend is intended for trusted repositories and tasks. A credential proxy and
outbound domain restrictions are outside this slice.

Subscription tokens, including tokens refreshed during execution, are redacted
from JSONL before it leaves the wrapper. The worker also redacts its configured
API key in API mode. Unstructured execution stderr is discarded. These checks do
not scan generated files for secrets; review retained output before sharing it.

### Optional API-key authentication

To use API billing explicitly, set `CIRCULAR_CODEX_AUTH_MODE=api_key` and supply
`CIRCULAR_CODEX_API_KEY` through your worker's secret/environment mechanism. Never
put the key in Agent configuration, Task text or a committed file. API mode does
not mount the subscription directory or reuse its login. Its bounded stdin request
carries the key to the wrapper, which supplies `CODEX_API_KEY` only to the CLI
process. It is absent from Docker arguments, labels and configured environment.
A key set while subscription mode is selected is rejected as ambiguous.

The container cannot access shared Git metadata. It can edit files and run
checks, but should not commit, push or depend on in-container Git status/diff.
Circular computes the final diff from the host. Language toolchains beyond the
runner's Node.js, Git and basic system utilities require a trusted custom image
retaining the same wrapper entrypoint.

## Event contract and verification

`internal/backends` prepares invocations and decodes backend-specific records.
It does not claim Runs, allocate resources or persist lifecycle transitions.
The Supervisor frames bounded JSONL, records normalized events with source
`codex`, and owns all resource cleanup. Codex agent messages become
`agent.message.completed`; successful turn usage becomes `usage.updated`.
Tool and reasoning items are not currently presented in the timeline. A
`turn.completed` record and a zero process exit are both required for success.
Explicit errors, malformed output or a missing completion record fail the Run.

Unit tests use documented JSONL fixtures and an isolated CLI helper process;
PostgreSQL tests exercise registration, replay, completion, failure, cancellation
and cleanup without provider credentials. Real Docker tests verify writable temporary storage and the dedicated auth mount,
including cleanup through a fresh runtime adapter without deleting login state.
Run the full verification commands from the README before a live smoke test.

For a live smoke test, use a disposable Repository and a task such as “Add
`hello.txt` containing `Hello from Circular`.” Confirm the message and usage,
download the diff, and verify the Workspace becomes `released`. Start a second
Run and cancel it while active to verify cleanup with your actual provider.
This live test requires your own subscription login (or explicitly configured API
key) and consumes the corresponding account usage. Automated tests use synthetic
authentication and do not contact a model provider.

CLI behavior follows the official [non-interactive mode documentation](https://learn.chatgpt.com/docs/non-interactive-mode),
[authentication documentation](https://learn.chatgpt.com/docs/auth),
and [CLI reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli).

## Agent proposals through MCP

Each Codex Run includes a local `circular` MCP server, implemented with the official
[Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk). No separate MCP URL,
provider token, or console setup is required. The trusted runner registers two tools:

- `list_models` returns Circular's model catalog and Astra default.
- `propose_agent` accepts `name`, `purpose`, complete `instructions`, and optional
  `model`, `reasoning_effort`, and `model_reason`. The orchestrating agent calls
  `list_models`, selects settings separately for each role, and explains both
  choices in `model_reason` (at most 1000 characters). User-specified choices take
  precedence; Astra and the model's default effort apply when no choice is supplied.
  It saves a recommendation for user review and
  explicitly reports `pending_review`. It does not create an Agent or start a Run.

The server runs over stdio inside the Run container. Drafts go into a private,
bounded temporary directory, with a maximum of 12 unique proposals per Run.
Identical calls reuse a proposal ID. When the CLI exits, the wrapper publishes
`circular.agent.proposed` records through the normal credential redactor. The
worker validates them again and atomically saves each proposal with an
`agent.proposed` timeline event. Project ownership comes from the Run's Task;
model output cannot choose another project. Cancelled or interrupted execution
may end before drafts are published.

On the Run page, **Suggested agents → Review & create** opens a dialog for the
name, instructions, model, and reasoning variant. The dialog prefills the
orchestrator's settings and shows its explanation. The user can override them or
select **Use recommendation** to restore the suggestion. Creation uses the
reviewed settings and leaves the Agent ready for selection in New task. Dismissals and creation
status persist across reloads. Creation locks the Run and proposal, creates one
Agent, and marks the proposal accepted in the same transaction. Retrying after a
lost response returns that Agent; name conflicts remain editable. The original
recommendation stays unchanged so it can still be matched to older reports.

The HTTP contract exposes:

- `GET /api/v1/runs/{run_id}/agent-proposals` — saved recommendations and status.
- `POST /api/v1/runs/{run_id}/agent-proposals` — save a reviewable draft, including
  a recommendation from an older Markdown report.
- `POST /api/v1/runs/{run_id}/agent-proposals/{proposal_id}/create` — create the
  reviewed Agent with `name`, `purpose`, `instructions`, and optional model settings.
- `POST /api/v1/runs/{run_id}/agent-proposals/{proposal_id}/dismiss` — dismiss it.

Apply migrations through `0007` and rebuild the API, worker, web app, and Codex runner when
upgrading. Revision `0006` adds proposal storage; `0007` adds the model explanation
with an empty default for existing proposals. These migrations preserve existing Agents,
Tasks, Runs, or provider connections. Previously completed discovery reports work
without rerunning: the console recognizes named roles with fenced instructions
inside a **Recommended agents** section and offers the same review flow.
Explanations are metadata on the original proposal; they are not added to the
Agent's instructions or backend configuration. Rephrasing an explanation does not
create another proposal with the same role and settings.
