# Install

The recommended install path is npm:

```sh
npm install -g projmux
projmux version
```

The `projmux` npm package installs a small Node.js shim plus one
platform-specific Go binary package for Linux and macOS on x64 or arm64. The
shim sets `PROJMUX_INSTALLER=npm` so Settings > About and `projmux update`
can use npm-aware update guidance.

After installing, run:

```sh
projmux doctor
```

`doctor` performs read-only diagnostics for runtime tools such as `tmux`,
`git`, and `stty`.

Provider integrations are opt-in and use the canonical installer spelling:

```sh
projmux agent integrate codex
projmux agent integrate claude
projmux agent integrate antigravity
projmux agent integrate tmux-bell
```

These installers write managed producers that call
`projmux internal agent-hook ingest ...`. Existing markerless hooks remain
user-owned and are never rewritten.

Start the tmux app with:

```sh
projmux shell
```

Each `projmux shell` launch prints a short welcome with the current version,
detach/exit keys, a bootstrap reminder, and cached release status when
available. Press Enter to continue into the shell.

If an update is available, the same shell-entry prompt uses one action
vocabulary: Enter continues, `u` upgrades by invoking `projmux update apply`,
and `s` skips that latest release tag until a newer tag appears. For `source`
or unknown installer sources, `u` prints installer guidance and then continues
shell entry.

To revisit the guide later, run `projmux welcome`, or use Settings > About >
Welcome inside the app to open it in a visible viewer. Set `PROJMUX_WELCOME=off` before
launching `projmux shell` to suppress legacy automatic attach popups without
disabling the shell prompt or manual command.

## Runtime Tools

Normal use needs:

- Node.js and npm for the npm install channel.
- tmux 3.4 or newer.

Useful optional tools:

- `git` for branch/status metadata.
- `notify-send` on Linux, or `powershell.exe` under WSL, for built-in desktop
  notifications.

## Go Install

Use this only when npm is not the right fit for your machine or workflow:

```sh
go install github.com/crevissepartners/projmux/cmd/projmux@latest
```

This requires Go 1.24 or newer. The binary is written to `$(go env GOBIN)` when
set, otherwise `$(go env GOPATH)/bin` (usually `~/go/bin`). Make sure that
directory is on `PATH`.

Go-managed installs can update through:

```sh
projmux update apply
```

See [Upgrading](upgrading.md) for version pinning and installer-specific
update behavior.

## Source Checkout

Source installs are for contributors or users who intentionally track a local
checkout:

```sh
git clone https://github.com/crevissepartners/projmux.git
cd projmux
make install
```

`make install` builds the binary, atomically replaces
`$(go env GOPATH)/bin/projmux`, runs `projmux config apply`, and reconciles the
notify queue through `projmux notification reconcile`. Override the destination
with `INSTALL_DIR=/usr/local/bin`.

Update source checkouts with the repository workflow:

```sh
git pull --ff-only
make install
```

## GitHub Release Binary

GitHub Release tarballs are supported by the updater when the install is
marked as release-managed:

```sh
export PROJMUX_INSTALLER=github-release
```

With that set, `projmux update apply` downloads the latest matching release
asset, replaces the current executable, and reapplies the live tmux config.
`--no-apply` skips the live reload only — the new binary still migrates the
keymap schema and marker-owned provider files, then writes the generated
config. It does not touch a live tmux bell hook. See
[Upgrading](upgrading.md#managed-agent-hook-producer-migration).

## npm Packaging Details

Repository packaging and publish details are maintained in
[npm Distribution](npm-distribution.md). That document is for maintainers; end
users should not need it for installation.
