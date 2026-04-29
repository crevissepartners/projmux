# projmux

Project-aware tmux workspace management for people who live in terminals.

`projmux` turns project directories into durable tmux workspaces with previews,
sidebar navigation, generated keybindings, status metadata, and AI-pane
attention signals. It can run as its own tmux app (`projmux shell`) or install
the same behavior into your existing tmux server.

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

- [Go 1.24+](https://go.dev/dl/) — required to install or build the binary.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) — the workspace runtime.
- [fzf](https://github.com/junegunn/fzf#installation) — interactive popup/sidebar pickers.
- [zsh](https://zsh.sourceforge.io/) — default shell of the generated app config (`projmux shell`).
- [git](https://git-scm.com/downloads) — branch/status metadata.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) — optional, only for the Kubernetes status segment.

Desktop notifications: Linux uses `notify-send`; WSL routes Windows toasts via
`powershell.exe`. Override either with `PROJMUX_NOTIFY_HOOK`.

## Install

```sh
go install github.com/crevissepartners/projmux/cmd/projmux@latest
```

This drops the binary in `$(go env GOBIN)` (when set) or `$(go env GOPATH)/bin`
(default `~/go/bin`). Make sure that directory is on your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

Verify:

```sh
projmux version
```

### Optional: `PROJDIR`

`PROJDIR` is the default project root projmux uses for picker and discovery.
It is optional — when unset, projmux falls back to its built-in source-root
discovery (`~/source`, `~/work`, `~/projects`, `~/src`, `~/code`,
`~/source/repos`).

```sh
export PROJDIR="$HOME/source/repos"
```

Add the line to `~/.zshrc` (or your shell's rc file). The resolved value is
memoized to `~/.config/projmux/projdir` after first use, so later shells keep
the same root even without the env var.

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
bindings. The left status badge shows the current project name; the right side
shows path, kube segment, git segment, and clock.

If your terminal emulator needs explicit key forwarding, see
[Terminal Keybindings](docs/keybindings.md).

## Upgrading

`projmux upgrade` reinstalls the binary via `go install`, atomically replaces
the active file, and reapplies the live tmux config so a running `-L projmux`
server picks up new bindings without a restart.

```sh
projmux upgrade                                  # @latest, replace + apply
projmux upgrade --ref @v0.2.0                    # pin a specific tag
projmux upgrade --ref @main                      # track a branch
projmux upgrade --target /usr/local/bin/projmux  # replace another path
projmux upgrade --no-apply                       # skip 'projmux tmux apply'
projmux upgrade --dry-run                        # print the steps only
```

The upgrade reads `PROJDIR`/`RP` from the calling shell and memoizes the
resolved value to `~/.config/projmux/projdir`, so the new binary keeps the
same project root context as the one it replaces.

## Usage

Day-to-day, projmux is driven by tmux keybindings inside `projmux shell` — see
[Terminal Keybindings](docs/keybindings.md). For the full CLI surface (pins,
preview state, status helpers, `upgrade`, etc.), run `projmux help` or
`<command> --help`.

## How It Finds Projects

`projmux switch` combines pinned directories, live tmux sessions, and discovered
project roots. The default discovery logic favors common source locations such
as `~/source`, `~/work`, `~/projects`, `~/src`, `~/code`, and `~/source/repos`
when they exist. `projmux settings` also has `Project Picker > Add Project...`,
which scans those filesystem roots up to depth 3 so projects outside `~` and
`~rp` can be added to the picker. Session names are derived from normalized
directory paths, so a project keeps the same tmux session name across launches.

For permanent search-root customization, the Project Picker section also
includes:

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

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `PROJDIR` | Default project root for the current shell. Memoized to `~/.config/projmux/projdir` after first use. |
| `PROJMUX_MANAGED_ROOTS` | Colon-separated list of search roots. Overrides the saved/default list. |
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in sender. |

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
- [CLI Shape](docs/cli.md)
- [Migration Plan](docs/migration-plan.md)
- [Repo Layout](docs/repo-layout.md)
- [Terminal Keybindings](docs/keybindings.md)
- [Agent Workflow](docs/agent-workflow.md)

## License

MIT. See [LICENSE](LICENSE).
