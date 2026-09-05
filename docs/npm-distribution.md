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

## Install residue notice

There is no `postinstall` script in this package, and there is no plan to add
one. A lifecycle script is skipped by `--ignore-scripts` and by many global and
CI installs, and npm buffers or reorders its output unless
`--foreground-scripts`. The bin shim cannot be skipped — it *is* the entrypoint
— and it writes straight to the user's terminal.

So the shim carries the install residue notice
([operational-diagnostics.md](operational-diagnostics.md#install-residue-ledger)).
`npm install -g projmux` and `npm update` delete and rewrite the package
directory, so a missing `npm/.install-residue-reported` watermark inside that
directory *is* the "this install is new" signal — no fingerprinting, no second
copy of the Go side's XDG path logic in JavaScript, and nothing written outside
the package directory. The watermark is not in the published `files` list, so
it never ships in a tarball.

On the first run after an install, if stderr is a TTY, the shim creates the
watermark and then — **after** the user's command has finished, so the notice is
the last thing on screen and never delays or interleaves with the real command
— runs `projmux internal install-residue`. A non-TTY run (a hook, CI, a pipe)
does nothing and leaves the watermark missing, so the next interactive run is
the one that reports; the notice should land on a run a human is looking at. If
the watermark cannot be written the notice is never shown, so an unwritable
package directory cannot produce it on every invocation forever. None of this
can change the shim's exit code.

The trade-off is deliberate: for npm the notice appears on the first
interactive run after the install rather than during `npm install` itself. The
ledger's `at` timestamp is the install-detection moment either way.

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
creates the release with `draft` and `force-tag-creation` set, so its release pass
creates the tag before the same action computes the next release PR. The tag starts
`release.yml`, which uploads archives to the drafted release, and only the final
`publish-release` job flips it visible. So by the time a user can see release
`vX.Y.Z`, npm `dist-tags.latest` already resolves to `X.Y.Z`; a failed npm publish
keeps the release hidden and the workflow red.
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
