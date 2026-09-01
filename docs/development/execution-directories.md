# Managed execution directories

The worker owns three filesystem roots. Repository and Run names, remote URLs, branch
names, and other user-controlled strings never become path segments. A repository cache
path is `<repository root>/<repository UUID>`; worktree and artifact paths are
`<corresponding root>/<Run UUID>`.

The worker also needs two views of each worktree when it eventually asks the host Docker
daemon to launch a Run container:

1. the worker-visible path used to provision and inspect the worktree;
2. the Docker-host-visible path used as the bind-mount source.

`ExecutionDirectories` performs that translation and rejects relative roots, `..`
traversal, existing symlinks that resolve outside the worker root, and filesystem-root
configuration. It does not create directories or grant the worker Docker access.

## Local worker defaults

Run `circular-worker` from the repository root with the settings unset:

| Purpose | Worker path | Docker host path |
| --- | --- | --- |
| Repository cache | `<repo>/.circular/repositories/<repository UUID>` | Not mounted |
| Run worktree | `<repo>/.circular/worktrees/<Run UUID>` | Same as worker path |
| Run artifacts | `<repo>/.circular/artifacts/<Run UUID>` | Not mounted |

Relative worker-root overrides are resolved once against the worker's current working
directory. `CIRCULAR_DOCKER_WORKTREE_ROOT`, when explicitly set, must be absolute because
the Docker daemon interprets it in the host filesystem namespace.

## Docker Compose defaults

Compose bind-mounts `${CIRCULAR_EXECUTION_HOST_ROOT:-$PWD/.circular}` at
`/var/lib/circular` in the trusted worker container. The resulting mapping is:

| Purpose | Docker host | Worker container |
| --- | --- | --- |
| Repository cache | `$PWD/.circular/repositories` | `/var/lib/circular/repositories` |
| Run worktrees | `$PWD/.circular/worktrees` | `/var/lib/circular/worktrees` |
| Run artifacts | `$PWD/.circular/artifacts` | `/var/lib/circular/artifacts` |

Compose therefore sets `CIRCULAR_DOCKER_WORKTREE_ROOT` to the absolute host-side
`$PWD/.circular/worktrees` by default. Set `CIRCULAR_EXECUTION_HOST_ROOT` to another
absolute directory when the data should live elsewhere. If the worker and Docker daemon
do not share a host filesystem, bind-path translation is insufficient; a remote-volume
adapter would be a separate execution model.

The later Docker adapter will decide the worktree's destination inside a Run container.
This mapping supplies only the validated host source and does not widen the intended
rule that an agent container receives its own worktree and explicitly scoped secrets.

## Local Repository cache

`LocalRepositoryCache` accepts a Repository UUID and its registered clone URL, then
returns the validated checkout at the path derived by `ExecutionDirectories`. Clone
URLs, Repository names, and remote ref names never become path fragments. A first
checkout is cloned with no working-tree files into a same-root staging directory,
validated, and atomically renamed into the UUID target. The result can be used as the
source for linked Git worktrees.

Reuse updates `origin` to the current registered URL, runs `fetch --prune`, and advances
the checkout's local default branch so `HEAD` resolves to the fetched commit. If that
fetch fails after the URL change, the cache attempts to restore the prior origin before
raising a credential-free `RepositoryFetchError`. Git runs as argv without a shell.
The MVP permits only local-file and HTTPS transports; other Git protocol helpers,
including `ext`, are disabled.

Updates are serialized per Repository with an advisory `fcntl.flock` and a bounded
30-second default acquisition timeout. Separate Repository UUIDs use separate lock
files and can proceed independently. This is deliberately a Linux/POSIX, worker-local
filesystem implementation: every worker sharing a cache root must cooperate with the
same locks, and network filesystems whose advisory-lock or atomic-rename semantics are
unreliable are unsupported. Cache filesystem metadata operations are short synchronous
operations against this trusted local root; Git processes and lock waits remain
asynchronous. Each Git command runs in its own process group; caller cancellation stops
and awaits that group before the cache lock is released. Agent containers do not
receive the Repository cache root.

## Worker settings

| Environment variable | Local default |
| --- | --- |
| `CIRCULAR_REPOSITORY_CACHE_ROOT` | `.circular/repositories` |
| `CIRCULAR_WORKTREE_ROOT` | `.circular/worktrees` |
| `CIRCULAR_ARTIFACT_ROOT` | `.circular/artifacts` |
| `CIRCULAR_DOCKER_WORKTREE_ROOT` | Resolved worker worktree root |
| `CIRCULAR_RUNNER_IMAGE` | `circular-runner:dev` |
| `CIRCULAR_RUNNER_CPU_LIMIT` | `1` CPU |
| `CIRCULAR_RUNNER_MEMORY_LIMIT_MB` | `2048` MiB |

The image and resource values are configuration defaults only. Until the Docker runtime
adapter is implemented, the in-process fake backend does not consume them.
