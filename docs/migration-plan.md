# Standalone Plan

> Historical migration record. Command spellings below describe the original
> shell-to-Go plan and are not current executable guidance; use the generated
> CLI reference for supported commands.

## Goal

Keep session-management product logic inside `projmux`.
The current target is a standalone app that can generate the tmux bindings and popup entry policy it needs.

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

### Phase 4
- standalone tmux popup toggle entrypoints
- generated tmux binding snippet
- `projmux tmux install` include-file wiring

### Phase 5
- pane attention toggle/clear/window badge commands
- generated tmux window-status and pane-focus hooks backed by `projmux attention`
- AI pane status, Codex title watcher, and desktop notification commands
- status bar git segment

## What stays outside standalone projmux

- shell startup hooks
- machine-specific install behavior
- optional OS or terminal integration scripts that are not session-management features

## Compatibility strategy

- preserve the user-facing keybindings and popup flows
- route those flows through `projmux` commands
- provide `projmux tmux print-config` and `projmux tmux install` for generated tmux bindings
- remove shell implementation only after the Go behavior is verified by the standalone path

## Expected first cut

The first useful milestone is not full parity.
It is:
- `projmux current`
- `projmux switch --ui=popup`
- `projmux sessions --ui=popup`
- pin persistence
- preview state persistence

## Standalone tmux path

The standalone tmux path is:

1. `projmux tmux print-config` prints bindings that call `projmux` directly.
2. `projmux tmux install` writes that snippet to `~/.config/tmux/projmux.conf` and includes it from `~/.tmux.conf`.
3. `projmux tmux popup-toggle <mode>` replaces `tmux-popup-toggle.sh` for sessionizer, session popup, sidebar, AI picker, and the unified settings popup.
4. `projmux attention <toggle|clear|window>` replaces tmux attention wrapper scripts for pane focus hooks and window badges.
5. `projmux ai status`, `projmux ai notify`, and `projmux ai watch-title` replace the AI pane state and notification shell scripts.
6. `projmux status git` replaces the tmux status-bar segment script for git branch rendering.

## App tmux runtime

The app runtime path is:

1. `projmux shell` writes `~/.config/projmux/tmux.conf` and launches `tmux -L projmux -f ~/.config/projmux/tmux.conf new-session -A -s main`.
2. `projmux tmux print-app-config` prints the config used by that isolated app server.
3. `projmux tmux install-app` writes the app config without touching `~/.tmux.conf`.
4. The app runtime uses a separate tmux socket and config from the user's normal tmux server, so projmux can own its status bar, bindings, badge, and popup behavior.
