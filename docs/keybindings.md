# Terminal Keybindings

projmux is keyboard-driven, but the guaranteed launch contract is small:
fresh installs bind `Alt-1` through `Alt-5` as plain Meta sequences
(`M-1`..`M-5`, bytes `\x1b1`..`\x1b5`). The AI split picker ships with an
editable `Alt-7` default. Other actions remain discoverable in
Settings > Keybindings. Transport-dependent actions keep their built-in
transport default key, and Settings can add separate safe tmux plain keys to
the same action. They are not installed as terminal-specific User-key
fallbacks. `UserN` and `CSI-u` are legacy/removal/unsupported targets, not
supported fallback guidance.

The recommended path when a key does not fire:

1. Try the key inside `projmux shell`.
2. On macOS, approve the one-time Accessibility prompt from the native
   `projmux shell` key adapter, then retry the physical key.
3. Run `projmux setup` outside tmux to see which bytes reach the process.
4. For supported terminals outside the native macOS app socket, preview
   `projmux setup terminal [terminal]`; add `--apply`
   only after reviewing the merge.
5. For unsupported terminals, configure plain Meta bytes or add a custom key in
   Settings > Keybindings.

Settings saves safe tmux plain chords for actions and automatically applies the
change to the app config and the running tmux session when Settings is opened
from inside tmux. After each save/reset it shows the three outcomes together:
saved, prepared, and running session. Successful in-tmux saves keep those labels
user-facing; failure and skipped states include diagnostic terms such as
`keymap.toml`, generated tmux config, or live tmux reload so the broken stage is
clear. If Settings is run outside tmux, the running-session stage is skipped and
the recovery/sync action is `projmux tmux apply`. Raw escape payloads, Windows
Terminal `sendInput` strings, and tmux User keys are rejected as action keys.
Settings capture diagnostics split the observed result into logical key, raw
bytes, and the tmux key name that can be saved. Diagnostic states distinguish
keys that did not arrive, ambiguous bytes such as Enter/Ctrl-M, and keys that
need a supported terminal adapter. Safe direct keys are logical tmux names such
as `M-a`, `M-1`, `C-r`, `C-Space`, function/navigation names, or printable
keys. Raw escape bytes, CSI-u, xterm modified-key payloads, and tmux
UserKey/UserSequence names stay diagnostic-only and are not promoted into
`keymap.toml`.

## Quick Start

These shortcuts are the guaranteed launch defaults. They need no tmux prefix.

| Shortcut | Action |
| --- | --- |
| `Alt-1` | Project sidebar |
| `Alt-2` | Notify sidebar |
| `Alt-3` | Recent Windows |
| `Alt-4` | AI resume session picker |
| `Alt-5` | Settings |

`RecentWindows:Open` opens the cross-project recent windows queue. It switches
to the selected live tmux window using that window's current active pane; it is
separate from `last-pane` and from the existing-session popup.

`Resources:Open` is listed in Settings > Keybindings without a default key.
Adding a safe direct chord opens the Linux/tmux Resource Inspector through the
canonical client-scoped popup-toggle path; pressing the same custom chord again
closes it without touching another client's popup. The action remains usable
when Settings > Labs > Live system resources is off—the Lab controls only
statusbar visibility.

The tmux prefix remains the upstream default `Ctrl-b`. Inside a running
session, `Ctrl-b ?` lists the live tmux bindings.

### Native macOS delivery

Darwin release binaries start a small native key adapter automatically with
`projmux shell`. It observes physical keys only while a client on that projmux
tmux socket reports itself focused. Supported `Alt`/Option and Control chords
from the same merged action catalog are sent back through tmux's client key
table. For example, a custom `M-a` remains the ordinary cross-platform
`keymap.toml` value; on macOS the adapter recognizes physical Option-A and
injects canonical `M-a`, while Linux and Windows keep their existing terminal
delivery path.

This adapter is terminal-independent, so Ghostty and iTerm2 do not need
separate projmux mappings for the app socket. It does not rewrite terminal
configuration and it does not translate Option-produced symbols such as `¡`.
The first launch shows a one-time consent hint before macOS asks for
Accessibility permission. The hint explains that the adapter captures only
modified chords from the configured keybinding catalog, never plain-text
typing; physical Option needs the event tap because terminals may convert it
before tmux can see it; and capture plus tmux injection stay on the local
machine. The broker then waits for approval and enables the event tap
automatically. While it is waiting, it replaces only its background broker
process so macOS permission changes are observed with fresh process state; the
tmux session does not need to be restarted.

Native macOS delivery remains on by default. To prevent the broker from
starting—and therefore prevent the Accessibility prompt—turn off **Native
macOS keybindings** in Settings > Keybindings before the next `projmux shell`
start, or launch with:

```sh
PROJMUX_NATIVE_KEYS=0 projmux shell
```

`PROJMUX_NATIVE_KEYS=false` is equivalent. The environment opt-out takes
precedence over the saved Settings value. Turning native delivery off leaves
the ordinary terminal key path in place; Option chords then depend on what the
terminal sends. Linux, WSL, and Windows never start this Darwin broker and are
unchanged.

The portable key vocabulary stays shared across operating systems:
`M-` (Alt/Option), `C-`, `S-`, letters, top-row digits, arrows/navigation,
and function keys. Command/Super is intentionally not part of this portable
tier. Picker-local unmodified commands continue through the picker input path;
the native adapter owns action-level no-prefix bindings only.

On Darwin, Settings > Keybindings > Add key keeps racing native modified-key
capture against the existing controlling-TTY capture. This lets Press a key
store a new physical Option, Control, Control-Option, or Shift-modified chord
before the terminal turns it into text; ordinary keys still use the portable
TTY path. The activation Enter is ignored as a recorder control, and a short
Darwin-only preference window lets the native physical chord win when the
terminal's translated bytes arrive at the same time. Linux, WSL, and Windows
keep their existing immediate terminal-capture ordering. The Darwin transport
lifecycle, Accessibility policy, and event mapping are unchanged by the
Linux/WSL recorder described below.

## Discoverable Actions

Settings > Keybindings lists the full action catalogue. In particular, sidebar
keymap actions, pane switching, window switching, and rename actions remain
visible.

The Settings flow is intentionally simple: the root has one native macOS
transport policy toggle followed by the action list with a key summary and
state. The key summary uses the first key plus `+N`, or `Not bound` when no key
is active. State vocabulary is limited to Default, Custom, Available, and
Unbound. Each action detail shows the action label, state, a flat Keys list
with `+ Add key`, Options, and a collapsed Troubleshooting row. Key rows open
key detail for Remove key and Test key. In a Linux/WSL tmux popup, `+ Add key`
enters a purpose-built recorder immediately: Recording waits for one chord,
Staged previews its normalized tmux key name without writing, Enter saves and
applies, and Esc discards it. Another chord replaces the staged candidate.
There is no Back row, search prompt, or result count in recorder mode. Plain
Enter and plain Escape are recorder controls, not candidates; modified Enter or
Escape are accepted only when the native picker can decode them to a stable
modified key name. Conflict and invalid-key feedback remains in the recorder so
the user can choose another chord before any write.

Advanced typed entry remains available from Action detail for literal
`Enter`/`Escape`, nonstandard tmux key names, the safe direct key pool,
risky/reserved key copy, and raw diagnostics. Advanced delivery is still owned
by the selected Projmux action. The native macOS app-socket adapter reads the
same safe chords directly; supported Ghostty and Windows Terminal mappings for
other paths are previewed/applied through `projmux setup terminal`, not by storing raw
sequences in the primary keymap. Options covers unbinding the action and
reset/use-default flows. Diagnostic/probe/init workflows are not first-class
Settings tabs; use `projmux setup` and, where the native adapter does not apply,
`projmux setup terminal` from the terminal when key delivery needs remediation.

Optional direct keys can be added for actions such as:

| Canonical action | Meaning |
| --- | --- |
| `RecentWindows:Open` | Recent windows queue across projects |
| `ProjectSwitcherToggle` | Project switcher popup |
| `AISplitPickerToggle` | AI split popup picker; default `Alt-7`; pressing again closes the picker popup |
| `AIResumePickerToggle` | AI resume session picker; default `Alt-4`; pressing again closes the picker popup |
| `ai-split-right` | Open a new direct AI split to the right |
| `ai-split-down` | Open a new direct AI split below |
| `new-window` | New tmux window in the current pane directory |
| `rename-window` | Rename the current tmux window |

`AISplitPickerToggle` is the `Alt-7` popup picker toggle. It opens or closes
the picker UI where the user chooses the AI split mode. It is separate from
the direct `ai-split-right` and `ai-split-down` actions.

The direct AI split actions create a new managed AI pane each time they run.
Existing AI panes are left in place; the requested direction controls where the
new pane is created.

Pane switching is catalogued as transport-dependent and the generated app tmux
config binds `M-Left`, `M-Right`, `M-Up`, and `M-Down` to `select-pane`
movement. Previous/next window remain transport-dependent and the generated app
tmux config binds `M-S-Left` / `M-S-Right` to the tmux window navigation
commands. These default transport keys are always rendered by projmux, because
delivery still depends on the terminal forwarding the modifier-arrow sequence.
Settings can add extra safe plain keys, such as `M-[` for
`previous-window`; those keys are saved to `keymap.toml` as `keys = [...]`
without storing or replacing the transport default. Rename actions no longer
have a built-in terminal fallback; use tmux's prefix rename flow or configure
an explicit safe key where the action is editable.

## Product Requirements

Settings > Keybindings stays a discovery surface. It must continue to expose
launch toggles, sidebar keymap actions, picker-local actions, pane switching,
window switching, and rename actions. The basic Settings flow is not the
terminal remediation surface: key-role replacement, disable-default, typed
fallback and terminal mapping preview/apply rows stay out of
the action detail.

The product model does not support `UserN` or `CSI-u` as fallback guidance.
Windows Terminal and Ghostty adapters use built-in plain Meta/control bytes or
xterm modifier sequences where possible. If a key cannot be represented that
way, leave it as a non-editable unsupported or diagnostic row instead of
preserving a User-key or CSI-u fallback.

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
| `NotifySidebar:Ack` / `NotifySidebar:AckGroup` / `NotifySidebar:ClearNonCritical` / `NotifySidebar:ClearAll` | Manage notifications |

Notify sidebar Right/Left child-row show/hide behavior is picker-local and is
not part of the Settings action catalog. `NotifySidebar:AckGroup` defaults to
uppercase `A`, distinct from `NotifySidebar:Ack` on lowercase `a`.

Runtime picker footers render key guides from the merged keymap, using the
first active key as the representative key.

In Settings > Keybindings, **Add key** stages one normalized chord and appends
it to the selected action's `keys = [...]` list only after Enter confirmation.
The recorder reuses the native picker's single input reader; it does not open a
second controlling-TTY reader or install a platform input hook. For catalog
popup toggle actions, confirmed keys are used both
by generated tmux open bindings and by popup-internal close bindings, so the
same key can close the corresponding already-open popup. This same-key close
behavior is only for actions cataloged as popup toggles, such as
`ProjectSidebarToggle`, `NotifySidebarToggle`, `RecentWindows:Open`,
`AISplitPickerToggle`, `SettingsToggle`, `ProjectSwitcherToggle`, and
`SessionPopupToggle`. Direct command actions such as `new-window`, pane/window
navigation, and direct AI split actions remain command bindings and are not
treated as popup close keys.

Settings is the default apply path for key edits: it writes the key list,
refreshes the generated config, and reloads the running tmux session when
possible. Use `projmux tmux apply` as a CLI recovery/sync command after editing
the keymap file by hand, after an outside-tmux Settings save, or after resolving
a reported generated-config or live-reload failure. Generated config first
unbinds the known retired `C-t` pane-label chord, then installs the current
keymap; an explicit current `C-t` assignment therefore wins without retaining
the retired command body. Apply does not rewrite `keymap.toml`.

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
`AISplitPickerToggle`, `SettingsToggle`, and `ProjectSwitcherToggle`. Direct
AI split actions keep their command IDs, `ai-split-right` and `ai-split-down`,
so Settings can distinguish them from the `Alt-7` popup picker toggle.

## Diagnose: `projmux setup`

Run `projmux setup` outside tmux to find out which projmux keys reach the raw
terminal. Settings > Keybindings remains the action-key editor; setup is the
terminal delivery diagnostic.

| Status | Meaning |
| --- | --- |
| `OK plain` | The terminal forwarded the expected bytes, such as `\x1b1` for `Alt-1`; tmux can bind this directly. |
| `MISS timeout` | No bytes arrived because the terminal swallowed the key. |
| `MISS unknown` | Bytes arrived, but they do not match the expected plain sequence. |

Settings capture uses the same underlying probe but reports a read model with
separate fields:

| Field | Meaning |
| --- | --- |
| Logical key | The key the user intended to press, such as `Alt-1`. |
| Raw bytes | The bytes captured from the terminal, shown escaped. |
| tmux received key | The logical tmux key name that can be saved, or a diagnostic placeholder when none is safe. |
| Delivery status | `delivered`, `key-did-not-arrive`, `ambiguous-key`, or `adapter-needed`. |

Useful flags:

```sh
projmux setup
projmux setup --timeout 10s
projmux setup --non-interactive
```

## Terminal remediation: `projmux setup terminal`

`projmux setup terminal` previews and optionally applies supported terminal
mappings. Default mode is a read-only preview; pass `--apply` to write changes with a timestamped
backup.

```sh
projmux setup terminal
projmux setup terminal ghostty
projmux setup terminal ghostty --apply
projmux setup terminal windows-terminal --apply
projmux setup terminal --config /path/to/file
projmux setup terminal --allow-symlink
```

The merge is idempotent: matching bindings are no-ops, missing bindings are
added, and keys already mapped to a different user action are skipped with a
warning. `projmux setup terminal` does not read `keymap.toml`; direct tmux keys still
belong in Settings > Keybindings or the keymap file.

### Ghostty

`projmux setup terminal ghostty` emits plain Meta bytes for `Alt-1` through `Alt-5` in
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

`projmux setup terminal windows-terminal` merges `sendInput` actions identified by the
`User.projmux*` ID prefix. The generated inputs use plain Meta bytes, tmux
prefix sequences for split actions, and xterm modifier-arrow sequences for
previous/next window:

```json
{
  "actions": [
    { "command": { "action": "sendInput", "input": "\u001b1" }, "id": "User.projmuxSidebar" },
    { "command": { "action": "sendInput", "input": "\u001b2" }, "id": "User.projmuxNotifySidebar" },
    { "command": { "action": "sendInput", "input": "\u001b3" }, "id": "User.projmuxRecentWindows" },
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
