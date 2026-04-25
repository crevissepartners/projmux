# CLI Shape

## Principles

- top-level commands should map to user intentions, not internal implementation details
- popup versus sidebar is UI mode, not product identity
- commands should work both inside and outside tmux where that makes sense

## Proposed commands

### Session navigation
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

### Tmux-facing helper entrypoints
- `projmux tmux popup-toggle <mode>`
- `projmux tmux print-config [--bin <path>]`
- `projmux tmux install [--bin <path>] [--config <path>] [--include <path>]`
- `projmux tmux popup-switch`
- `projmux tmux popup-sessions`
- `projmux tmux popup-preview <session>`

## Suggested mode mapping

- `projmux tmux popup-toggle sessionizer-sidebar` -> `projmux switch --ui=sidebar`
- `projmux tmux popup-toggle sessionizer` -> `projmux switch --ui=popup`
- `projmux tmux popup-toggle session-popup` -> `projmux sessions --ui=popup`
- `projmux tmux popup-toggle ai-split-picker-right` -> `projmux ai picker --inside right`
- `projmux tmux popup-toggle ai-split-settings` -> `projmux ai settings`
- `projmux switch --ui=popup`
- `projmux switch --ui=sidebar`
- `projmux sessions --ui=popup`
- `projmux session-popup preview <session>`
- `projmux session-popup open <session>`

## Compatibility target

The first implementation should preserve these user-facing flows from dotfiles:
- project directory chooser
- existing-versus-new session labeling
- preview window/pane cycling
- tagged kill
- settings/pin management
- current directory jump
- auto attach and ephemeral pruning
