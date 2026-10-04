# Circular vitrine

Public product presentation, separate from the private working console in
`apps/web`. This page contains no application data, API client, forms, tracking,
or sign-in link. Its links navigate the product overview.

Follow the [Circular brand guide](../../docs/brand.md) for the approved Orbit
mark, palette, typography, and voice.

Run `node apps/vitrine/build.mjs` from the repository root. The build copies only
the listed public files and the already-installed Geist font with its license
into `apps/vitrine/dist/`. No additional dependencies are required. The existing
`dist/` ignore rules exclude the output from git.

Deploy that output as `/opt/circular/vitrine/`. Caddy serves it at
`https://circular.ruohao.dev`. The working console is available only through the
loopback app listener and an SSH tunnel; see `infra/production/README.md`.
