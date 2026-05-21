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

`doctor` checks that runtime tools such as `tmux`, `git`, and `stty` are
available.

Start the tmux app with:

```sh
projmux shell
```

Each `projmux shell` launch prints a short welcome with the current version,
detach/exit keys, core app shortcuts, and cached update status when available.
Press Enter to continue for this run, or press `s` to skip the welcome for the
current projmux version. The next projmux version shows the welcome again.

If an installer-supported update is available, the same prompt keeps update
actions separate from welcome skip: press `u` to run `projmux update apply`,
`n` to print the manual update command, or `d` to skip daily update prompts for
that release.

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
- `kubectl` for the Kubernetes status segment.
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
projmux upgrade
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
`$(go env GOPATH)/bin/projmux`, runs `projmux tmux apply`, and reconciles the
notify queue. Override the destination with `INSTALL_DIR=/usr/local/bin`.

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
asset, replaces the current executable, and reapplies the live tmux config
unless `--no-apply` is used.

## npm Packaging Details

Repository packaging and publish details are maintained in
[npm Distribution](npm-distribution.md). That document is for maintainers; end
users should not need it for installation.
