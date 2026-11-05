# Downstream build

Build and release the deployed distribution from
[`unstaticlabs/msgvault`](https://github.com/unstaticlabs/msgvault).
The deployment pins `ghcr.io/unstaticlabs/msgvault` by digest. Its historical
stack name includes the operator's name; that name does not identify a different
source repository.

## Repository roles

| Repository | Role | Local remote |
|---|---|---|
| `kenn-io/msgvault` | Native product and upstream history | `upstream` (fetch only) |
| `unstaticlabs/msgvault` | Integrated distribution, releases and container images | `usl` |
| `ValentinViennot/msgvault` | Personal contribution branches | `valentin` |

Local `main` tracks `usl/main`. An `origin` fetch alias points to the same USL
repository for documentation tools and has pushes disabled. Fetch `upstream/main` to prepare catch-ups in an
isolated branch. Do not use the personal fork as a release source or overwrite
its contribution branches when synchronizing the distribution.

## Integration and release

`main` retains both upstream and downstream ancestry. Bring upstream in with a
merge, resolve overlap in that merge, and keep new compatibility work in separate
commits. Open a pull request against `unstaticlabs/msgvault:main`; do not rebase
published distribution history or push an integration straight to `main`.

```bash
git fetch usl
git fetch upstream
git switch -c codex/upstream-catchup usl/main
git merge --no-ff upstream/main
```

Qualify the combined archive upgrade, source access, MCP contracts and browser
journeys. Tag the approved distribution `v<upstream version>-usl.<n>` after the
pull request passes and merges. The downstream publishing workflows build the
release binaries and `ghcr.io/unstaticlabs/msgvault`. Pin the resulting immutable
image digest in deployment configuration; a published image alone is not proof
of deployment.

## Compatibility ownership

The distribution retains multi-user sign-in and source visibility, calendar
coverage, provider Inbox/read state, related-email grouping and exact-message
Inbox archiving. Prefer native replacements when they preserve those outcomes.
The [October integration record](2026-10-upstream-compatibility.md) describes the
predecessor contracts; its schema numbers are historical.

The updater's release owner remains `unstaticlabs`, so a self-update cannot replace
a downstream binary with an upstream build. Pull-request workflows execute the
combined checkout's local reusable workflows. Upstream publishing removal does
not remove the downstream release and image workflows.
