# CLI Shape

## Principles

- top-level commands should map to user intentions, not internal implementation details
- popup versus sidebar is UI mode, not product identity
- commands should work both inside and outside tmux where that makes sense

## Proposed commands

### Session navigation
- `projmux shell`
- `projmux switch`
- `projmux current`
- `projmux sessions`
- `projmux preview`

### Session lifecycle
- `projmux create <dir>`
- `projmux kill <session>`
- `projmux kill tagged`
- `projmux prune ephemeral`
- `projmux attach auto [--keep=N] [--fallback=home|ephemeral]`

### Pins and config
- `projmux pin add <dir>`
- `projmux pin remove <dir>`
- `projmux pin toggle <dir>`
- `projmux pin list`
- `projmux pin clear`
- `projmux config path`

### Tmux attention
- `projmux attention toggle [pane]`
- `projmux attention clear [pane]`
- `projmux attention window [window]`

### Tmux AI status
- `projmux ai status set <thinking|waiting|idle> [pane]`
- `projmux ai notify [notify|reset] [pane]`
- `projmux ai watch-title [pane]`

Set `PROJMUX_NOTIFY_HOOK` to route AI desktop notifications through a custom
executable. The hook receives summary, body, urgency, app name, tag, group, and
icon path as positional arguments and replaces the built-in sender while
configured.

Without a hook, `projmux` uses `notify-send` on Linux. On WSL it targets
Windows toast notifications through `powershell.exe` and attempts to register
the `projmux.TmuxCodex` AppUserModelID automatically on first use.

### Tmux status bar
- `projmux status git [path]`
- `projmux status kube [session]`
- `projmux status usage [--max-width N]`
- `projmux status notify [--max-width N]`
- `projmux statusbar click <range-id> [--socket <s>] [--mouse-x N] [--mouse-y N]`

`projmux statusbar click` is the click/keyboard dispatcher for the three-line
status bar. Range ids are `session pwd kube git usage notify`. See
[architecture.md](architecture.md#three-line-clickable-status-bar) for the
full mapping.

### Tmux-facing helper entrypoints
- `projmux tmux popup-toggle <mode>`
- `projmux tmux rebalance-panes`
- `projmux tmux rename-pane <pane> <title>`
- `projmux tmux print-config [--bin <path>]`
- `projmux tmux print-app-config [--bin <path>]`
- `projmux tmux install [--bin <path>] [--config <path>] [--include <path>]`
- `projmux tmux install-app [--bin <path>] [--config <path>]`
- `projmux tmux popup-switch`
- `projmux tmux popup-sessions`
- `projmux tmux popup-preview <session>`

## Suggested mode mapping

- `projmux tmux popup-toggle sessionizer-sidebar` -> `projmux switch --ui=sidebar`
- `projmux tmux popup-toggle sessionizer` -> `projmux switch --ui=popup`
- `projmux tmux popup-toggle session-popup` -> `projmux sessions --ui=popup`
- `projmux tmux popup-toggle ai-split-picker-right` -> `projmux ai picker --inside right`
- `projmux tmux popup-toggle ai-split-settings` -> `projmux settings`
- `projmux switch --ui=popup`
- `projmux switch --ui=sidebar`
- `projmux sessions --ui=popup`
- `projmux session-popup preview <session>`
- `projmux session-popup open <session>`

## Compatibility target

The first implementation should preserve these user-facing flows:
- project directory chooser
- existing-versus-new session labeling
- preview window/pane cycling
- tagged kill
- settings/pin management
- current directory jump
- auto attach and ephemeral pruning
