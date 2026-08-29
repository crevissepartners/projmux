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
`projmux setup terminal` only for terminals that swallow shortcuts.

## Local Packaging

Build and dry-run pack all npm packages:

```bash
make npm-pack
```

or:

```bash
scripts/package-npm.sh --version 1.2.3 --out /tmp/projmux-npm --pack
```

The script stages package directories under `dist/npm` by default. Local
all-platform packaging runs on macOS so both Darwin packages include the CGO
native key adapter; attempting to build them on another host fails instead of
silently producing a no-CGO Darwin package. The script builds the Go binary for
each supported platform, copies package metadata and docs, updates package
versions in the staged copies, generates root
`optionalDependencies` for the supported platform packages using the same
version, verifies the staged metadata is internally consistent, checks both
Darwin binaries for the native adapter, then runs `npm pack --dry-run` when
`--pack` is set.

Release packaging reuses the already-tested platform archives rather than
cross-compiling a second set of npm-only binaries:

```bash
scripts/package-npm.sh \
  --version 1.2.3 \
  --release-dir /path/to/release-archives \
  --out /tmp/projmux-npm
```

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
are uploaded. The npm job downloads the same linux/darwin × amd64/arm64
archives built by the release matrix and runs:

```bash
scripts/package-npm.sh \
  --version "${GITHUB_REF_NAME#v}" \
  --out dist/npm \
  --release-dir dist/release
```

This keeps the npm platform binaries byte-for-byte aligned with the GitHub
Release binaries, including the `darwin && cgo` native key adapter, then
publishes each staged package with `npm publish --access public`.

The GitHub release itself stays a draft until that npm job succeeds. release-please
creates the release with `draft` set, `release.yml` uploads archives to the drafted
release, and only the final `publish-release` job flips it visible. So by the time a
user can see release `vX.Y.Z`, npm `dist-tags.latest` already resolves to `X.Y.Z`;
a failed npm publish keeps the release hidden and the workflow red.
The npm publish job uses GitHub Actions OIDC (`id-token: write`) instead of a
long-lived `NPM_TOKEN` secret. PR CI runs `make npm-pack` so package staging and
dry-run packing fail before release.

## Executable Path Canonicalization

`npm update -g` retires the existing package directory by renaming it to
`node_modules/.<pkg>-<hash>` before installing the replacement and deleting
the retired directory. `os.Executable()` follows that rename, so a projmux
process running across the update window can resolve its own path into the
doomed staging directory. projmux canonicalizes any `node_modules/.<pkg>-<hash>`
(or scoped `node_modules/@scope/.<pkg>-<hash>`) segment back to `<pkg>` before
writing the resolved path into generated tmux config or live hooks, so a
completed update never leaves stale-path `run-shell` commands failing with
`... returned 127`.

## Non-Goals

The npm installer must not install system dependencies, edit shell startup
files, or mutate tmux config. Those actions stay behind explicit
`projmux doctor`, `projmux setup terminal`, Settings About update actions, or future
opt-in install commands.
