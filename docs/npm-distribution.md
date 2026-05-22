# npm Distribution

`projmux` remains a Go CLI. npm is a distribution channel that installs a
small Node.js shim plus one platform-specific Go binary package.

The public npm package `projmux` is the root shim package. Release builds use
this package layout:

| package | contents |
| --- | --- |
| `projmux` | `npm/projmux.js` shim and optional dependencies |
| `@projmux/linux-x64` | `linux/amd64` `bin/projmux` |
| `@projmux/linux-arm64` | `linux/arm64` `bin/projmux` |
| `@projmux/darwin-x64` | `darwin/amd64` `bin/projmux` |
| `@projmux/darwin-arm64` | `darwin/arm64` `bin/projmux` |

The shim sets `PROJMUX_INSTALLER=npm` before executing the real binary so
`projmux update status` and the Settings About screen can present
npm-specific guidance. npm is only an update/install source label here; the
keybinding flow remains `projmux shell` first, then `projmux setup` and
`projmux init` only for terminals that swallow shortcuts.

## Local Packaging

Build and dry-run pack all npm packages:

```bash
make npm-pack
```

or:

```bash
scripts/package-npm.sh --version 1.2.3 --out /tmp/projmux-npm --pack
```

The script stages package directories under `dist/npm` by default. It builds
the Go binary for each supported platform, copies package metadata and docs,
updates package versions in the staged copies, generates root
`optionalDependencies` for the supported platform packages using the same
version, verifies the staged metadata is internally consistent, then runs
`npm pack --dry-run` when `--pack` is set.

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
npm scope. Configure npm Trusted Publishing for every package before merging a
release PR:

| npm package | GitHub organization/user | repository | workflow filename |
| --- | --- | --- | --- |
| `@projmux/linux-x64` | `crevissepartners` | `projmux` | `release.yml` |
| `@projmux/linux-arm64` | `crevissepartners` | `projmux` | `release.yml` |
| `@projmux/darwin-x64` | `crevissepartners` | `projmux` | `release.yml` |
| `@projmux/darwin-arm64` | `crevissepartners` | `projmux` | `release.yml` |
| `projmux` | `crevissepartners` | `projmux` | `release.yml` |

Leave the npm trusted publisher environment field empty unless the workflow is
later moved behind a GitHub deployment environment.

Tag releases publish npm packages from GitHub Actions after release archives
are uploaded. The workflow runs:

```bash
scripts/package-npm.sh --version "${GITHUB_REF_NAME#v}" --out dist/npm
```

then publishes each staged package with `npm publish --access public`.
The npm publish job uses GitHub Actions OIDC (`id-token: write`) instead of a
long-lived `NPM_TOKEN` secret. PR CI runs `make npm-pack` so package staging and
dry-run packing fail before release.

## Non-Goals

The npm installer must not install system dependencies, edit shell startup
files, or mutate tmux config. Those actions stay behind explicit
`projmux doctor`, `projmux init`, Settings About update actions, or future
opt-in install commands.
