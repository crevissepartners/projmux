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
- `projmux attach auto`

### Pins and config
- `projmux pin add <dir>`
- `projmux pin remove <dir>`
- `projmux pin toggle <dir>`
- `projmux pin list`
- `projmux pin clear`
- `projmux config path`

### Tmux-facing helper entrypoints
- `projmux tmux popup-toggle <mode>`
- `projmux tmux rows <mode>`
- `projmux tmux preview <target>`

## Suggested mode mapping

- `projmux switch --ui=popup`
- `projmux switch --ui=sidebar`
- `projmux sessions --ui=popup`

## Compatibility target

The first implementation should preserve these user-facing flows from dotfiles:
- project directory chooser
- existing-versus-new session labeling
- preview window/pane cycling
- tagged kill
- settings/pin management
- current directory jump
- auto attach and ephemeral pruning
