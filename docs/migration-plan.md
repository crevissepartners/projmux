# Migration Plan

## Goal

Extract session-management product logic from dotfiles into `projmux` while leaving terminal, shell, and install policy in dotfiles.

## What moves first

### Phase 1
- session naming rules
- preview selection state
- pin state
- current-directory session jump
- tagged kill fallback logic

### Phase 2
- candidate discovery
- sessionizer row building
- session popup row building
- preview rendering data model
- session create or switch orchestration

### Phase 3
- popup toggle orchestration
- auto-attach logic
- ephemeral prune logic
- kube session helpers

## What stays in dotfiles

- `.tmux.conf`
- `bin/tmux/*` wrappers during migration
- Ghostty config
- zsh startup hooks
- machine-specific install behavior

## Compatibility strategy

- preserve keybindings in dotfiles first
- swap the called binary under those bindings from shell scripts to `projmux`
- remove shell implementation only after the Go behavior is verified

## Expected first cut

The first useful milestone is not full parity.
It is:
- `projmux current`
- `projmux switch --ui=popup`
- `projmux sessions --ui=popup`
- pin persistence
- preview state persistence
