# projmux

Project-aware tmux session management for people who live in terminals.

`projmux` turns project directories into stable tmux sessions, gives you fast
fzf-driven switching and previews, and can install the tmux bindings it needs
without depending on a private dotfiles layout.

[한국어 README](README-ko.md)

## Features

- Project picker that creates or switches to tmux sessions from directories.
- Existing-session picker with popup previews for windows and panes.
- Sidebar and popup surfaces backed by `fzf`.
- Pinned project directories and tagged session actions.
- Persisted preview selection for window and pane cycling.
- Generated tmux bindings for popup launchers, attention badges, and status bar
  segments.
- Isolated `projmux shell` mode that runs its own tmux server and config.
- Status bar helpers for the active git branch and Kubernetes context/namespace.
- AI pane helpers for split launchers, status markers, and desktop notifications.

## Requirements

- Go 1.24 or newer.
- tmux.
- fzf for interactive pickers.
- zsh for the generated isolated app config.
- git for branch/status metadata.
- kubectl is optional and only needed for Kubernetes status segments.

## Install

From source:

```sh
git clone https://github.com/es5h/projmux.git
cd projmux
make build
install -m 0755 .bin/projmux ~/.local/bin/projmux
```

Make sure `~/.local/bin` is on your `PATH`:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Check the binary:

```sh
projmux version
projmux help
```

For development, use:

```sh
make fmt
make fix
make test
make test-integration
make test-e2e
```

## Quick Start

Launch the isolated projmux tmux app:

```sh
projmux shell
```

This writes `~/.config/projmux/tmux.conf` and starts:

```sh
tmux -L projmux -f ~/.config/projmux/tmux.conf new-session -A -s main
```

Inside that app session, the left status badge shows the current project name,
and the right status area shows the current path, kube segment, git segment, and
clock.

Common app bindings:

| Key | Action |
| --- | --- |
| `Alt-1` | Open project sidebar |
| `Alt-2` | Open existing session popup |
| `Alt-3` | Open project switcher popup |
| `Alt-4` | Open AI split picker |
| `Alt-5` | Open AI split settings |
| `Ctrl-n` | New tmux window in the current pane directory |
| `Alt-Left/Right/Up/Down` | Move between panes |
| `Alt-Shift-Left/Right` | Previous/next window |
| `Prefix b` | Existing session popup |
| `Prefix f` | Project switcher popup |
| `Prefix F` | Project sidebar |
| `Prefix g` | Jump to the current pane project session |
| `Prefix r` | Open AI split to the right |
| `Prefix l` | Open AI split below |

## Apply To Your Existing tmux

If you want projmux inside your normal tmux server instead of the isolated app
server, install the generated tmux snippet:

```sh
projmux tmux install --bin "$(command -v projmux)"
tmux source-file ~/.tmux.conf
```

That writes `~/.config/tmux/projmux.conf`, adds a source line to
`~/.tmux.conf`, and installs bindings that call `projmux` directly.

To inspect the snippet before installing:

```sh
projmux tmux print-config --bin "$(command -v projmux)"
```

To only regenerate the isolated app config:

```sh
projmux tmux install-app --bin "$(command -v projmux)"
```

## zsh Integration

The lowest-friction setup is an alias:

```sh
alias pmx='projmux shell'
```

If you want new interactive zsh shells to enter the projmux app automatically,
add a guarded hook to `~/.zshrc`:

```sh
if [[ -o interactive && -z "${TMUX:-}" ]] && command -v projmux >/dev/null 2>&1; then
  exec projmux shell
fi
```

Keep machine-specific shell policy in your own zsh config. `projmux` provides
the stable app entrypoint and generated tmux config; it does not own terminal
emulator or login-shell policy.

## Commands

High-level navigation:

```sh
projmux shell
projmux switch [--ui=popup|sidebar]
projmux sessions [--ui=popup|sidebar]
projmux current
```

Session lifecycle:

```sh
projmux attach auto [--keep=N] [--fallback=home|ephemeral]
projmux kill tagged
projmux kill tagged <session>...
projmux prune ephemeral [--keep=N]
```

Pins, tags, and preview state:

```sh
projmux pin add <dir>
projmux pin remove <dir>
projmux pin toggle <dir>
projmux pin list
projmux tag toggle <name>
projmux tag list
projmux preview select <session> <window> <pane>
projmux preview cycle-window <session> <next|prev>
projmux preview cycle-pane <session> <next|prev>
```

Tmux integration helpers:

```sh
projmux tmux install
projmux tmux install-app
projmux tmux popup-toggle <mode>
projmux attention toggle [pane]
projmux status git [path]
projmux status kube [session]
```

Run `projmux help` or `<command> --help` for the full command surface.

## How It Finds Projects

`projmux switch` combines pinned directories, live tmux sessions, and discovered
project roots. The default discovery logic favors common source locations such
as `~/source/repos` when they exist. Session names are derived from normalized
directory paths, so a project keeps the same tmux session name across launches.

## Configuration And State

Default paths follow XDG conventions:

- Config: `~/.config/projmux`
- State: `~/.local/state/projmux`
- Cache: `~/.cache/projmux` and tmux-specific cache files under `~/.cache/tmux`
- Runtime kube session files: `$XDG_RUNTIME_DIR/kube-sessions` when available

The generated app tmux config lives at:

```text
~/.config/projmux/tmux.conf
```

The generated normal tmux snippet lives at:

```text
~/.config/tmux/projmux.conf
```

## Project Boundary

`projmux` owns the portable session-management core: naming, discovery, pins,
preview state, tmux orchestration, status segments, and generated tmux bindings.

Your dotfiles should keep owning local policy: terminal emulator key dispatch,
zsh startup decisions, machine-specific packages, and symlinks.

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
- [Agent Workflow](docs/agent-workflow.md)

## License

MIT. See [LICENSE](LICENSE).
