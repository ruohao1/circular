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
