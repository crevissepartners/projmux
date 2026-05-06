# npm Distribution

`projmux` remains a Go CLI. npm is a distribution channel that installs a
small Node.js shim plus one platform-specific Go binary package.

The public npm name `projmux` is reserved as `0.0.0-reserved`. The source
package scaffold uses this package layout:

| package | contents |
| --- | --- |
| `projmux` | `npm/projmux.js` shim and optional dependencies |
| `@projmux/linux-x64` | `linux/amd64` `bin/projmux` |
| `@projmux/linux-arm64` | `linux/arm64` `bin/projmux` |
| `@projmux/darwin-x64` | `darwin/amd64` `bin/projmux` |
| `@projmux/darwin-arm64` | `darwin/arm64` `bin/projmux` |

The shim sets `PROJMUX_INSTALLER=npm` before executing the real binary so
`projmux update status` and the Settings About screen can present
npm-specific guidance.

## Local Packaging

Build and dry-run pack all npm packages:

```bash
make npm-pack
```

or:

```bash
scripts/package-npm.sh --version 0.4.0 --out /tmp/projmux-npm --pack
```

The script stages package directories under `dist/npm` by default. It builds
the Go binary for each supported platform, copies package metadata and docs,
updates package versions in the staged copies, then runs `npm pack --dry-run`
when `--pack` is set.

## Publish Order

The platform packages must be published before the root package:

```text
@projmux/linux-x64
@projmux/linux-arm64
@projmux/darwin-x64
@projmux/darwin-arm64
projmux
```

Publishing the scoped platform packages requires control of the `@projmux`
npm scope. The release automation should fail before publishing if that scope
or `NPM_TOKEN` is unavailable.

## Non-Goals

The npm installer must not install system dependencies, edit shell startup
files, or mutate tmux config. Those actions stay behind explicit
`projmux doctor`, `projmux init`, or future opt-in install commands.
