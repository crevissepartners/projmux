# projmux

Project-aware tmux workspace management for people who live in terminals.

`projmux` turns project directories into durable tmux workspaces with previews,
sidebar navigation, generated keybindings, status metadata, and AI-pane
attention signals. It can run as its own tmux app (`projmux shell`) or install
the same behavior into your existing tmux server.

[![npm version](https://img.shields.io/npm/v/projmux?logo=npm)](https://www.npmjs.com/package/projmux)
[![CI](https://github.com/crevissepartners/projmux/actions/workflows/ci.yml/badge.svg)](https://github.com/crevissepartners/projmux/actions/workflows/ci.yml)

[한국어 README](README-ko.md)

## Why projmux

Most tmux project switchers stop at "pick a directory and attach a session".
`projmux` treats that as the foundation, then adds the app-level pieces needed
for a daily terminal workspace:

- **Project identity stays stable.** Directories, pins, live sessions, preview
  selection, and lifecycle commands all use the same normalized session model.
- **The UI shows context before you switch.** Popup and sidebar pickers preview
  sessions, windows, panes, git branch, Kubernetes context, and pane metadata.
- **The tmux layer is generated, not hand-spliced.** `projmux` writes the tmux
  config it needs for popup launchers, window/pane rename flows, status
  segments, pane borders, attention badges, and app mode.
- **AI panes are first-class.** Codex and Claude panes can be launched,
  labeled, tracked as thinking or waiting, surfaced in pane/window/session
  badges, and announced through desktop notifications.
- **You can choose isolation or integration.** Use `projmux shell` as a
  self-contained tmux app, or install the generated snippet into your normal
  tmux server.

## What It Does

- Creates or switches to tmux sessions from project directories.
- Shows existing sessions with window and pane previews.
- Provides popup and sidebar navigation surfaces backed by `fzf`.
- Pins important projects and scans common source roots for new ones.
- Persists preview selection for fast window and pane cycling.
- Generates tmux bindings for launchers, rename prompts, pane borders, status
  segments, and attention hooks.
- Displays git branch and Kubernetes context/namespace in the status area.
- Renders a two-line clickable status bar with click-to-switch tabs on
  row 0 and HUD-style notify (left) and AI usage (right) segments on
  row 1.
- Launches AI splits and keeps their agent name, topic, status, and
  notification state visible in tmux.

## Typical Workflow

```sh
projmux shell
```

Open the app once, then use its generated tmux bindings to:

- jump between projects from a sidebar or popup,
- inspect sessions before attaching,
- split Codex, Claude, or a plain shell into the current workspace,
- rename windows and AI pane topics without losing metadata,
- see which panes need review from badges and desktop notifications.

## Requirements

- [Node.js](https://nodejs.org/) and npm — required for the recommended npm
  install path.
- [Go 1.24+](https://go.dev/dl/) — required only when installing with
  `go install` or building from source.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) **≥ 3.4** — the workspace runtime. Earlier versions miss `display-popup -T` and other features projmux depends on.
- [fzf](https://github.com/junegunn/fzf#installation) **≥ 0.65.0** — interactive popup/sidebar pickers. The `fzf` executable on `PATH` must report at least 0.65.0 from `fzf --version`; distro packages such as Ubuntu 24 apt may be too old, so use upstream GitHub Releases, Homebrew, or another install method with a version check. `npm i fzf` installs a JavaScript fuzzy-search library, not the junegunn/fzf CLI binary that projmux executes.
- A Unix shell such as `bash`, `zsh`, or `sh` — `projmux shell` uses your
  absolute `$SHELL` for the generated app config, falling back to `/bin/sh`.
- [git](https://git-scm.com/downloads) — branch/status metadata.
- `stty` — POSIX terminal control, used by `projmux setup`. Already shipped by every macOS / Linux base system; not applicable on Windows hosts.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) — optional, only for the Kubernetes status segment.

Desktop notifications: Linux uses `notify-send`; WSL routes Windows toasts via
`powershell.exe`. Override either with `PROJMUX_NOTIFY_HOOK`.

Run `projmux doctor` any time to verify runtime dependencies are on `PATH`
and that tmux/fzf meet the minimum supported versions. Terminal key delivery
is diagnosed separately with `projmux setup`.

## Install

```sh
npm install -g projmux
```

npm installs a small Node.js shim plus the matching platform binary package
for Linux and macOS on x64 or arm64. The shim marks the install as npm-managed
so `projmux update` and the Settings About screen can use the right upgrade
path.

Verify:

```sh
projmux version
```

If npm is not a fit for your machine, install with Go:

```sh
go install github.com/crevissepartners/projmux/cmd/projmux@latest
```

This drops the binary in `$(go env GOBIN)` (when set) or `$(go env GOPATH)/bin`
(default `~/go/bin`). Make sure that directory is on your `PATH`.

### Optional: `PROJMUX_PROJDIR`

`PROJMUX_PROJDIR` is the primary project root projmux uses for picker and
discovery when you explicitly configure it. It is optional; when unset,
projmux does not assume a canonical repo root. Discovery still uses pins, live
sessions, saved workdirs, and weak common-folder probes (`~/source`, `~/work`,
`~/projects`, `~/src`, `~/code`) when they exist.

```sh
export PROJMUX_PROJDIR="/your/path"
```

Add the line to `~/.bashrc`, `~/.zshrc`, or your shell's rc file. The resolved value is
memoized to `~/.config/projmux/projdir` after first use, so later shells keep
the same root even without the env var.

`PROJMUX_PROJDIR` accepts an OS-native PATH-style multi-value (`:` on
Linux/macOS, `;` on Windows). The first non-empty entry is the primary
project root; any additional entries are prepended to the managed-roots
search list, so they participate in discovery just like
`PROJMUX_MANAGED_ROOTS`. Only the primary path is memoized to
`~/.config/projmux/projdir`.

```sh
# Linux/macOS — primary repo + secondary search root
export PROJMUX_PROJDIR="/main/repos:/srv/work/repos"
```

#### Set the project root during setup

```sh
PROJMUX_PROJDIR=/your/path projmux shell
```

The first invocation that sees the env var writes
`~/.config/projmux/projdir`, so later shells without the env var still
resolve the same root.

You can also manage the saved value interactively with
`projmux settings > Project Picker > Project Root`. That screen shows the
effective primary root and source (`PROJMUX_PROJDIR`, `@projmux_projdir`,
saved, or not configured), shows when a saved value is shadowed by env/tmux,
and lets you set a path directly, use the current project context, or clear the
saved value. When no Project Root is configured, the direct-set prompt starts
with `$HOME` as an editable fallback; it is not treated as the effective root
until you save it.

### From source

```sh
git clone https://github.com/crevissepartners/projmux.git
cd projmux
make install
```

`make install` builds, atomically replaces `$(go env GOPATH)/bin/projmux`, and
runs `projmux tmux apply` so the live `-L projmux` server picks up new bindings
without a restart. Override the destination with `INSTALL_DIR=/usr/local/bin`.

## Quick Start

Launch the isolated projmux tmux app:

```sh
projmux shell
```

projmux owns this tmux server, its generated config, status bar, and popup
bindings. Cooperative terminals get `Alt-1`..`Alt-5` immediately, with no
terminal config. The left status badge shows the current project name; the
right side shows path, kube segment, git segment, and clock.

If a key does not fire, run `projmux setup` outside tmux to see which
sequences your terminal swallows. For supported terminals, preview the
fallback with `projmux init [terminal]`, then apply it with
`projmux init [terminal] --apply` (auto-detects when no terminal is given).
Dotfiles users on multi-machine setups should pass
`--allow-symlink` or `--config <path>` to make their intent explicit. Full
flow and the manual CSI-u fallback are in
[Terminal Keybindings](docs/keybindings.md).

If anything looks off, `projmux doctor` reports which dependency is
missing or stale and how to install it. See [Requirements](#requirements)
for the supported versions.

## Upgrading

The Settings About screen is the normal interactive update surface: it shows
cached release status, installer source, Check Updates, Update Now, and
release notes. The startup update prompt uses the same cache and never reaches
the network.
To refresh the cached release status manually, run:

```sh
projmux update check
```

Use Settings > About > Update or `projmux update apply` to update through the
detected installer. See [Upgrading](docs/upgrading.md) for npm, Go, GitHub
Release, and source-checkout details.

## Usage

Day-to-day, projmux is driven by tmux keybindings inside `projmux shell` — see
[Terminal Keybindings](docs/keybindings.md). For the full CLI surface (pins,
preview state, status helpers, updates, etc.), run `projmux help` or
`<command> --help`.

## How It Finds Projects

`projmux switch` combines pinned directories, live tmux sessions, and discovered
project roots. When no explicit search roots are configured, discovery uses
weak common-folder probes such as `~/source`, `~/work`, `~/projects`, `~/src`,
and `~/code` if they exist; it does not assume a canonical `~/source/repos`
root. `projmux settings` also has `Project Picker > Add Project...`, which
scans filesystem roots up to depth 3 so projects outside the weak probes can be
added to the picker. Session names are derived from normalized directory paths,
so a project keeps the same tmux session name across launches.

For permanent search-root customization, the Project Picker section also
includes:

- `Project Root` - set, change, or clear the saved primary root. This is the
  one root used as the primary project context; env `PROJMUX_PROJDIR` and tmux
  `@projmux_projdir` override the saved value until unset. If no root is
  configured, the direct-set prompt pre-fills `$HOME` so the picker remains
  usable without inventing an implicit saved root.
- `+ Add Workdir...` - append a single directory to the saved workdirs list.
- `Workdirs` - review and remove saved workdirs. The same picker also surfaces
  any active `PROJMUX_MANAGED_ROOTS` / `TMUX_SESSIONIZER_ROOTS` env values as
  read-only rows so you can see why an env list might be overriding the saved
  file.

`Add Workdir > Type path manually...` gives you a typed entry that skips the
filesystem scan. Use it for paths you do not want crawled, e.g. WSL mounts
(`/mnt/c/Users/...`), large NFS mounts, or per-project temp roots.

The saved file lives at `~/.config/projmux/workdirs` (one absolute path per
line, `#` comments allowed). It is consulted only when the env vars are unset.

## Hooks

projmux runs an optional user script at `~/.config/projmux/hooks/post-create`
whenever it creates a new tmux session. Use it to inject per-session env via
`tmux set-environment` (e.g. picking a `GH_TOKEN` based on the project path).
Missing or non-executable hooks are skipped silently; failures never block
session creation. See [Hooks](docs/hooks.md) for the env contract, examples,
and troubleshooting.

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `PROJMUX_PROJDIR` | Explicit primary project root for the current shell. Accepts an OS-native PATH-style multi-value: the first entry is the primary repo root (memoized to `~/.config/projmux/projdir`), and any additional entries are prepended to the managed-roots search list. |
| `PROJMUX_MANAGED_ROOTS` | Colon-separated list of search roots. Overrides the saved/heuristic list. |
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in sender. |
| `PROJMUX_USAGE_STATE_DIR` | Override directory for the AI-usage snapshot cache. Defaults to `<state>/projmux/usage`. Point this at a synced location (Dropbox, iCloud Drive, etc) to share authoritative usage between machines. |
| `PROJMUX_USAGE_DEBUG` | When non-empty, surfaces adapter errors from `projmux status usage` to stderr instead of swallowing them. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated. Limits now come from the upstream APIs (Anthropic OAuth usage endpoint, Codex `rate_limits`); this variable is read but ignored. |

## AI usage tracking

`projmux usage` reports authoritative 5-hour and weekly utilisation for both
Claude Code and the Codex CLI. Both adapters read from the upstream's own
view of your account so the percentages match what `claude /usage` and
`codex` show natively:

- **Claude** — calls `GET https://api.anthropic.com/api/oauth/usage` with the
  bearer token in `~/.claude/.credentials.json`. The adapter performs a
  single refresh round-trip on 401 and rewrites the credentials file with
  the rotated tokens. Tokens are never logged.
- **Codex** — reads the most recent `rate_limits` payload from the newest
  `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`. `primary` maps to the 5h
  window, `secondary` to the weekly window.

Snapshots are persisted under `<state>/projmux/usage/snapshots.json` (or
`PROJMUX_USAGE_STATE_DIR`) and refreshed at most every 30 seconds when
`projmux status usage` runs in the tmux status bar.

## Scope

`projmux` owns the portable session-management core: naming, discovery, pins,
preview state, tmux orchestration, status segments, and generated tmux bindings.

## Development

Useful commands:

```sh
make build
make fmt
make fix
make test
make test-integration
make test-e2e
make verify
```

More documentation:

- [Architecture](docs/architecture.md)
- [CLI Reference](docs/cli.md)
- [Statusbar](docs/statusbar.md)
- [Notify queue](docs/notify-queue.md)
- [Usage tracking](docs/usage-tracking.md)
- [Upgrading](docs/upgrading.md)
- [Hooks](docs/hooks.md)
- [Migration Plan](docs/migration-plan.md)
- [Repo Layout](docs/repo-layout.md)
- [Terminal Keybindings](docs/keybindings.md)
- [Agent Workflow](docs/agent-workflow.md)

## License

MIT. See [LICENSE](LICENSE).
