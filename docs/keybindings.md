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

Settings saves safe tmux plain chords and two-to-four-stroke sequences for
actions, then automatically applies the change to the app config and the
running tmux session when Settings is opened from inside tmux. After each
save/reset it shows the three outcomes together: saved, prepared, and running
session. Successful in-tmux saves keep those labels user-facing; failure and
skipped states include diagnostic terms such as `keymap.toml`, generated tmux
config, or live tmux reload so the broken stage is clear. If Settings is run
outside tmux, the running-session stage is skipped and the recovery/sync action
is `projmux config apply`. Raw escape payloads, Windows Terminal `sendInput`
strings, and tmux User keys are rejected as action keys or sequence strokes.
Settings capture diagnostics split the observed result into logical key, raw
bytes, and the tmux key name that can be saved. Diagnostic states distinguish
keys that did not arrive, ambiguous bytes such as Enter/Ctrl-M, and keys that
need a supported terminal adapter. Safe direct keys are logical tmux names such
as `M-a`, `M-1`, `C-r`, `C-Space`, function-key names, or printable
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

On Darwin, Linux, and WSL, Settings > Keybindings > Action > `+ Add binding`
uses the same native-picker logical recorder. Native macOS transport state and
terminal adapter differences remain available through an explicit `Test
delivery` result and `projmux setup`; they do not occupy normal binding detail
rows or change the logical strokes the recorder accepts.

## Discoverable Actions

Settings > Keybindings lists the full action catalogue. In particular, sidebar
keymap actions, pane switching, window switching, and rename actions remain
visible.

The Settings flow is intentionally simple: the root has one native macOS
transport policy toggle followed by the action list with a key summary and
state. The key summary uses the first key plus `+N`, or `Not bound` when no key
is active. State vocabulary is limited to Default, Custom, Available, and
Unbound. Each action detail leads with the action label and state, then the
current **Single Keys** and **Sequences** collections. Their key and sequence
rows open the existing manage flows; `+ Add binding`, `Enter binding manually`,
unbind, and reset/use-default actions follow the current bindings. Picker-local
actions keep their sequence limitation beside the unavailable sequence state
and point back to the usable single-key actions. There is no `Advanced`,
`Details`, `Delivery`, or `Troubleshooting` teaching container. Internal action
semantics, handler-manifest metadata, canonical storage, and transport paths are
not rendered as passive rows. Key detail instead shows the current action and
key, then Replace binding, manual replacement, Remove key, and Test delivery;
sequence detail likewise shows the sequence and its test/replace/remove actions.
`+ Add binding` enters a
purpose-built recorder immediately and continuously accumulates one to four
logical strokes without closing. Enter saves and applies once, Esc cancels with
no write, and Backspace removes only the last stroke (or does nothing when the
draft is empty). Plain Enter and Escape are recorder controls, not candidates;
reserved control/navigation keys and their modifier or alias spellings are
never added to the draft. A first plain printable stroke is rejected, while a
safe plain printable is allowed after a safe modified first stroke. One recorded
stroke saves as a single key and two to four save as a sequence.

The recorder, `Enter binding manually`, and the retained physical-capture
adapter all use the same classifier, pre-write conflict validation, and final
save boundary. Typed entry accepts comma display form such as `C-o,o` or legacy
space form `C-o o`; visible sequence boundaries use commas, while
`keymap.toml`, generated configuration, routes, and runtime values retain the
schema's space separator. Conflicts remain in the recorder for retry without a
write. Replace binding uses the same pipeline with the old binding as explicit
context, so single-to-sequence and sequence-to-single replacement is one atomic
save. Navigation, cancellation, Backspace editing, and delivery tests do not
write the keymap or generated config and do not reload tmux; successful
mutations still report saved, prepared, and running-session stages. Delivery remediation remains in
`projmux setup` and `projmux setup terminal`, not a separate authoring view.

`Test delivery` in key detail is an observable action, not a diagnostic dump. It
reports the logical key, the raw observation, the key tmux received, and one of
`delivered`, `key-did-not-arrive`, `ambiguous-key`, or `adapter-needed`. It
writes nothing and reloads nothing, so a raw observation can never be promoted
into the stored logical-key model. Exactly one reader is live at a time: inside
a tmux popup the picker's own recorder reads the key (the popup client owns the
tty, so the controlling-TTY probe is never started and cannot hang on its
timeout), and outside tmux the probe reads it with a bounded timeout that
reports `key-did-not-arrive` rather than blocking. Where no reader can be owned,
or where the chord is a plain `Enter`/`Escape` that the recorder consumes as its
own control, the row is disabled and states the reason plus the next step:
`projmux setup`, then `projmux setup terminal <terminal> --apply`.

An action whose shipped/default plain alias, prefix trigger, or sequence
contains a reserved logical base key is protected as a whole. Its Action detail
shows the shipped trigger that caused the read-only lock and keeps navigation
and delivery tests, but renders no add, replace, remove, unbind, or
reset action. Protection is derived from the shipped catalog rather than the
effective/custom binding, so an override cannot unlock a protected action or
lock an otherwise safe default. Existing protected defaults continue to parse,
render, and execute unchanged.

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
possible. Use `projmux config apply` as a CLI recovery/sync command after editing the keymap file by
hand, after an outside-tmux Settings save, or after resolving a reported
generated-config or live-reload failure. Generated config first unbinds the
known retired `C-t` pane-label chord, then installs the current keymap; an
explicit current `C-t` assignment therefore wins without retaining the retired
command body.

Apply rewrites `keymap.toml` only to migrate it to the current schema version,
and only before it writes any generated config. An already-current file is left
byte-identical. See [Schema versions](#schema-versions).

## Keymap File

Settings writes the action-centered multi-alias schema, versioned by a root
`schema_version` marker:

```toml
schema_version = 2

[bindings."project-sidebar.toggle"]
keys = ["M-1", "M-a"]
sequences = ["C-k C-p"]

[bindings."window.create"]
keys = ["C-t"]
sequences = ["C-k C-w"]

[bindings."window.focus-previous"]
keys = ["M-["] # additive alias; default M-S-Left is not stored here

[bindings."project-sidebar.project.pin-toggle"]
keys = ["M-p", "p"]
```

Legacy files using `plain = "M-a"` still read as a single-primary replacement
override. New writes use `keys = [...]`. `keys = []` disables the direct plain
aliases for that action. Legacy `prefix = ...` entries still parse during
migration and are preserved when Settings rewrites the file, but Settings does
not create new prefix entries.

`sequences = [...]` adds action triggers made of two to four logical tmux
strokes separated by exactly one space. It does not replace `keys`, and projmux
ships no built-in sequences. Settings can add, replace, remove, and test these
triggers through the sequence editor described above; hand editing remains
supported through the same grammar and preflight.

The first stroke must be a modified chord (`C-k`, `M-x`, `C-M-k`), navigation
key, or function key. Later strokes may also be safe named keys or one printable
ASCII key in existing files. Settings refuses to newly author Enter, Escape,
Tab, Backspace, Delete, arrows, Home, End, PageUp, or PageDown at any stroke,
including modifier and alias spellings. The loader and runtime continue to
accept previously saved values and shipped defaults; the authoring policy is
not a parser-level rejection. Escape is also reserved for cancellation, and raw escape/sendInput,
CSI-u/xterm payloads, `User*` fallbacks, unsafe tmux config characters, and
undeliverable names are rejected. `C-m`, `C-i`, and `C-[` are rejected with the
spelling to use instead: a terminal reports those bytes as `Enter`, `Tab`, and
`Escape`, so a leaf bound to the control spelling could never be reached.
A sequence is also rejected when it duplicates
another sequence, is a strict prefix of one, or starts with a root no-prefix
single chord. Validation completes before any keymap, generated config, or live
tmux write.

Shared prefixes compile into deterministic `projmux-sequence-*` tmux key
tables. Escape or an unknown continuation consumes the partial sequence and
returns the client to `root` without dispatching an action or replaying input to
the pane. The generated config records the exact roots and tables it installs in
`@projmux_sequence_roots` / `@projmux_sequence_tables`; the *next* apply unbinds
precisely those before it sources the new config, so removing a sequence leaves
no ghost table. Removal is owned by the apply command rather than the config
file, because a `run-shell` loop inside a sourced file is not ordered against
the binds around it. The macOS native broker only adds representable modified
sequence strokes to its transport allowlist and still forwards each stroke via
tmux's client key table—it does not turn those strokes into independent action
triggers.

Direct AI split actions stay distinct from the `Alt-7` popup picker toggle:
`agent-pane.launch-default.right` and `agent-pane.launch-default.down` create a
pane, `agent-pane-launcher.toggle` opens the picker.

### Schema versions

| Version | Marker | Table ids |
| --- | --- | --- |
| v0 | none | the runtime ids projmux has always used — `ProjectSidebarToggle`, `new-window`, `ai-split-right`, `Sidebar:KillSession` |
| v1 | `schema_version = 1` | canonical dotted ids — `project-sidebar.toggle`, `window.create`, `agent-pane.launch-default.right`, `project-sidebar.runtime.stop` |
| v2 | `schema_version = 2` | v1 canonical ids plus additive `sequences = [...]` action triggers |

A file with no marker is v0. All versions read: the v0 spelling of every action
stays a permanent read alias, so a hand-written or hand-restored v0 file keeps
working. Only writes are versioned. A v0 migration writes canonical ids and the
v2 marker; a v1 migration changes only the marker, preserving comments, table
ordering, unknown tables, and hand formatting byte for byte.

A canonical id containing a dot must be quoted. `[bindings."window.create"]` is
a binding named `window.create`; `[bindings.window.create]` would be a nested
table and is rejected.

### Upgrading a keymap

The migration is not a separate command. It runs at the two moments a keymap is
already being written or applied:

- `projmux config apply`, which every installer path calls with the newly
  installed binary.
- The first Settings key save, which is what converges an install that never ran
  an updater — a tarball unpacked over the old binary, for instance.

`projmux config render standalone` and `projmux config render app` report a
pending migration on **stderr** and write nothing. Their stdout stays the
generated artifact, byte-identical to the `internal tmux print-config` and
`internal tmux print-app-config` spellings a running server still invokes, so a
sourced config is never corrupted by a diagnostic. `projmux config render app`
is the read-only preview that pairs with `projmux config apply`, because
`config apply` writes the app config and reloads the live server.

The migration order is fixed:

1. Read and validate the whole v0/v1 file, including chord and sequence conflicts.
2. Write a digest-named backup. v0 keeps the established
   `keymap.toml.pre-v1-<digest>.bak` name; v1 uses
   `keymap.toml.pre-v2-<digest>.bak`. A retry reuses the same backup and a
   different original can never overwrite it.
3. Produce v2 in a temp file, re-parse it, re-merge it and prove the effective
   single-chord and sequence tables are unchanged. v1 takes the marker-only path.
4. Only then replace the original atomically.
5. Only then write the generated tmux config and reload the live server.

If any step fails, the keymap, the generated config and the running server are
all left alone, and the report says which of the three did not move. Running the
migration again on an already-migrated file writes no bytes at all.

`projmux update apply --dry-run` names the
migration stage but do not print a rename table. The candidate binary is not
installed yet, and only it knows its own canonical ids.

`--no-apply` suppresses the live tmux reload, not the migration. Installer paths
still invoke the new binary as `config apply --no-reload` so the schema does not
fall behind the binary that writes it.

### Downgrading or rolling back

To roll v2 back to the immediately previous v1 file, restore its pre-v2 backup:

```sh
cp ~/.config/projmux/keymap.toml.pre-v2-<digest>.bak ~/.config/projmux/keymap.toml
```

For a keymap that entered migration as v0, restore the established pre-v1
backup **before** installing a projmux that predates versioned keymaps:

```sh
cp ~/.config/projmux/keymap.toml.pre-v1-<digest>.bak ~/.config/projmux/keymap.toml
```

A projmux that predates this schema reads `schema_version` as an unsupported
root key and refuses the file. Restoring first avoids that. The backup is
validated on restore, so a truncated or corrupted one fails loudly rather than
becoming the live keymap.

### Retired ids

These table names were removed from the action catalog and resolve to nothing.
A migration preserves the table and reports it rather than remapping it onto a
surviving action:

| Retired id | What to do |
| --- | --- |
| `rename-pane-topic` | replace with `[bindings."pane.rename"]`; this one fails the parse outright because the replacement has different semantics |
| `sessionizer-sidebar`, `notify-sidebar`, `session-popup`, `ai-split-picker-right`, `ai-split-settings`, `sessionizer` | popup *mode* names, never action ids; use the canonical toggle id for the surface |

A table this projmux does not recognise at all — from a newer release, or
hand-written — is preserved verbatim and reported as unmapped.

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
