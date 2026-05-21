# Terminal Keybindings

projmux is keyboard-driven, but the guaranteed launch contract is small:
fresh installs bind `Alt-1` through `Alt-5` as plain Meta sequences
(`M-1`..`M-5`, bytes `\x1b1`..`\x1b5`). Other actions remain discoverable in
Settings > Keybindings. Transport-dependent actions keep their built-in
transport default key, and Settings can add separate safe tmux plain aliases to
the same action. They are not installed as terminal-specific User-key
fallbacks. `UserN` and `CSI-u` are legacy/removal/unsupported targets, not
supported fallback guidance.

The recommended path when a key does not fire:

1. Try the key inside `projmux shell`.
2. Open Settings > Keybindings > Diagnostic, or run `projmux setup` outside
   tmux, to see which bytes reach the process.
3. For supported terminals, preview `projmux init [terminal]`; add `--apply`
   only after reviewing the merge.
4. For unsupported terminals, configure plain Meta bytes or add a tmux alias in
   Settings > Keybindings.

Settings writes safe tmux plain chords to
`~/.config/projmux/keymap.toml`, regenerates
`~/.config/projmux/tmux.conf`, and hot-reloads the live tmux config when it is
running inside tmux. Raw escape payloads, Windows Terminal `sendInput` strings,
and tmux User keys are rejected as aliases.

## Quick Start

These shortcuts are the guaranteed launch defaults. They need no tmux prefix.

| Shortcut | Action |
| --- | --- |
| `Alt-1` | Project sidebar |
| `Alt-2` | Notify sidebar |
| `Alt-3` | Existing session popup |
| `Alt-4` | AI split picker |
| `Alt-5` | Settings |

The tmux prefix remains the upstream default `Ctrl-b`. Inside a running
session, `Ctrl-b ?` lists the live tmux bindings.

## Discoverable Actions

Settings > Keybindings lists the full action catalogue. In particular, sidebar
keymap actions, pane switching, window switching, and rename actions remain
visible. Transport-dependent rows show the default transport key separately
from editable plain aliases.

Optional direct aliases can be added for actions such as:

| Canonical action | Meaning |
| --- | --- |
| `ProjectSwitcherToggle` | Project switcher popup |
| `new-window` | New tmux window in the current pane directory |
| `rename-window` | Rename the current tmux window |

Pane switching is catalogued as transport-dependent and the generated app tmux
config binds `M-Left`, `M-Right`, `M-Up`, and `M-Down` to `select-pane`
movement. Previous/next window remain transport-dependent and the generated app
tmux config binds `M-S-Left` / `M-S-Right` to the tmux window navigation
commands. These default transport keys are always rendered by projmux, because
delivery still depends on the terminal forwarding the modifier-arrow sequence.
Settings can add extra safe plain aliases, such as `M-[` for
`previous-window`; those aliases are saved to `keymap.toml` as `keys = [...]`
without storing or replacing the transport default. Rename actions no longer
have a built-in terminal fallback; use tmux's prefix rename flow or configure
an explicit safe alias where the action is editable.

## Roadmap Requirements

Follow-up Phase 2 keeps Settings > Keybindings as a discovery surface. It must
continue to expose launch toggles, sidebar keymap actions, picker-local actions,
pane switching, window switching, and rename actions. Transport-dependent rows
should explain the default transport key and offer only additive safe plain
aliases; diagnostic-only rows should explain why they are not editable.

Follow-up Phase 3 removes the `UserN` / `CSI-u` route from the product model.
Windows Terminal and Ghostty-centered replacements should use plain
Meta/control chords or xterm modifier sequences where possible. If a key cannot
be represented that way, leave it as a non-editable unsupported or diagnostic
row instead of preserving a User-key or CSI-u fallback.

## Picker Actions

Picker-internal commands are surface-scoped. The same physical key can be used
by different picker surfaces, while conflicts inside one surface are rejected.

| Surface action | Meaning |
| --- | --- |
| `Sidebar:KillSession` | Kill the focused existing session |
| `Sidebar:PinProject` | Pin or unpin the focused directory |
| `SessionPopup:KillSession` | Kill the focused session |
| `SessionPopup:CyclePreviewWindowPrev` / `SessionPopup:CyclePreviewWindowNext` | Preview windows |
| `SessionPopup:CyclePreviewPanePrev` / `SessionPopup:CyclePreviewPaneNext` | Preview panes |
| `NotifySidebar:Ack` / `NotifySidebar:ClearNonCritical` / `NotifySidebar:ClearAll` | Manage notifications |

Runtime picker footers avoid hardcoded key guides so custom aliases do not make
on-screen copy stale.

## Keymap File

Settings writes the action-centered multi-alias schema:

```toml
[bindings.ProjectSidebarToggle]
keys = ["M-1", "M-a"]

[bindings.new-window]
keys = ["C-t"]

[bindings.previous-window]
keys = ["M-["] # additive alias; default M-S-Left is not stored here

[bindings."Sidebar:PinProject"]
keys = ["M-p", "p"]
```

Legacy files using `plain = "M-a"` still read as a single-primary replacement
override. New writes use `keys = [...]`. `keys = []` disables the direct plain
aliases for that action. Legacy `prefix = ...` entries still parse during
migration and are preserved when Settings rewrites the file, but Settings does
not create new prefix entries.

Legacy popup IDs such as `sessionizer-sidebar`, `notify-sidebar`,
`session-popup`, `ai-split-picker-right`, `ai-split-settings`, and
`sessionizer` still read. Settings and new docs show the canonical toggle
names: `ProjectSidebarToggle`, `NotifySidebarToggle`, `SessionPopupToggle`,
`AISplitPickerToggle`, `SettingsToggle`, and `ProjectSwitcherToggle`.

## Diagnose: `projmux setup`

Run `projmux setup` outside tmux to find out which projmux keys reach the raw
terminal. The same diagnostic is available in-app at Settings > Keybindings >
Diagnostic.

| Status | Meaning |
| --- | --- |
| `OK plain` | The terminal forwarded the expected bytes, such as `\x1b1` for `Alt-1`; tmux can bind this directly. |
| `MISS timeout` | No bytes arrived because the terminal swallowed the key. |
| `MISS unknown` | Bytes arrived, but they do not match the expected plain sequence. |

Useful flags:

```sh
projmux setup
projmux setup --timeout 10s
projmux setup --non-interactive
```

## Auto-Config: `projmux init`

`projmux init` previews and optionally applies supported terminal mappings.
Default mode is dry-run; pass `--apply` to write changes with a timestamped
backup.

```sh
projmux init
projmux init ghostty
projmux init ghostty --apply
projmux init windows-terminal --apply
projmux init --config /path/to/file
projmux init --allow-symlink
```

The merge is idempotent: matching bindings are no-ops, missing bindings are
added, and keys already mapped to a different user action are skipped with a
warning. `projmux init` does not read `keymap.toml`; direct tmux aliases still
belong in Settings > Keybindings or the keymap file.

### Ghostty

`projmux init ghostty` emits plain Meta bytes for `Alt-1` through `Alt-5` in
the managed block:

```text
# >>> projmux managed keybindings (do not edit between markers)
keybind = alt+1=text:\x1b1
keybind = alt+2=text:\x1b2
keybind = alt+3=text:\x1b3
keybind = alt+4=text:\x1b4
keybind = alt+5=text:\x1b5
# <<< projmux managed keybindings
```

Config candidates are `<config-dir>/ghostty/config` and
`<config-dir>/ghostty/config.ghostty`, with `$XDG_CONFIG_HOME` preferred over
`$HOME/.config`. If both exist, pass `--config <path>`. Symlinked configs are
refused by default; pass `--allow-symlink` to write through the link.

### Windows Terminal

`projmux init windows-terminal` merges `sendInput` actions identified by the
`User.projmux*` ID prefix. The generated inputs use plain Meta bytes, tmux
prefix sequences for split actions, and xterm modifier-arrow sequences for
previous/next window:

```json
{
  "actions": [
    { "command": { "action": "sendInput", "input": "\u001b1" }, "id": "User.projmuxSidebar" },
    { "command": { "action": "sendInput", "input": "\u001b2" }, "id": "User.projmuxNotifySidebar" },
    { "command": { "action": "sendInput", "input": "\u001b3" }, "id": "User.projmuxSessions" },
    { "command": { "action": "sendInput", "input": "\u001b4" }, "id": "User.projmuxAIPicker" },
    { "command": { "action": "sendInput", "input": "\u001b5" }, "id": "User.projmuxSettings" },
    { "command": { "action": "sendInput", "input": "\u0002r" }, "id": "User.projmuxAISplitRight" },
    { "command": { "action": "sendInput", "input": "\u0002l" }, "id": "User.projmuxAISplitDown" },
    { "command": { "action": "sendInput", "input": "\u000e" }, "id": "User.projmuxNewWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[1;4D" }, "id": "User.projmuxPrevWindow" },
    { "command": { "action": "sendInput", "input": "\u001b[1;4C" }, "id": "User.projmuxNextWindow" }
  ]
}
```

Windows Terminal config is JSONC; comments and unrelated user entries are
preserved where possible. Conflicting user-owned keys are skipped.
