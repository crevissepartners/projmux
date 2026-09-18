# Configuration

Most users can configure projmux from `projmux settings`. Environment variables
are available for repeatable shell setup, managed machines, or advanced
overrides.

## Operational diagnostics state

Projmux keeps a private bounded operational journal at:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/logs/operations.jsonl
```

There is no configuration switch for arbitrary fields or remote delivery. The
`projmux` state and `logs` directories are created/repaired to mode `0700`, and
the JSONL file is created/repaired to `0600` on POSIX systems. At more than
5 MiB, the writer atomically retains about the newest 2 MiB of complete valid
records. Lock ownership is maintained by the OS and acquisition waits no more
than 200 ms before the best-effort write is abandoned. See
[operational-diagnostics.md](operational-diagnostics.md) for safe field and
best-effort behavior. Existing `ai-ingest.log` and
`PROJMUX_*_DEBUG` settings remain separate and unchanged.

## Project Discovery

`projmux switch` combines pinned directories, live tmux sessions, and
discovered project roots.

When no explicit roots are configured, discovery uses weak probes only when
those folders exist:

- `~/source`
- `~/work`
- `~/projects`
- `~/src`
- `~/code`

It does not assume a canonical repo root.

Use `Settings > Global > Projects` for the normal interactive flow:

- `Primary discovery root` sets, changes, or clears the saved primary root.
- `Additional discovery roots > Add path` appends one directory to the saved
  workdirs list.
- `Additional discovery roots` reviews saved workdirs; each root's
  `Remove discovery root` removes it.

`Add path > Type path manually...` skips the filesystem scan and is useful
for large mounts, WSL paths, NFS paths, or temporary project roots.

The saved workdir file is:

```text
~/.config/projmux/workdirs
```

It stores one absolute path per line. Lines beginning with `#` are comments.
The file is read only when no env root list is set.

Saved workdirs are a **scan source and nothing else**. Adding a root, and scanning one,
never registers a Registry Project: a discovered child is an unregistered
candidate until `projmux create project --root <path>` or opening it once from the
Projects sidebar registers that exact path. See
[upgrading.md](upgrading.md#discovery-no-longer-registers-projects).

## Project pins

Pins are presentation preferences, stored typed:

```text
~/.config/projmux/pins
```

```text
projmux-pins v2
project proj-kwo4qozry2sr2ycij2g45zyvam
candidate /home/dev/src/scratch
```

A `project` pin references a Registry Project uid, so its displayed root and name
come from the Registry and the pin survives a rebind, a rename, and a missing
root. A `candidate` pin references a path no Project claims. Neither kind is a
discovery source and neither is managed identity.

`projmux pin project list [--kind project|candidate]` prints both collections with
their kind. `pin project add|remove|toggle <dir>` accepts the same directory
argument it always has and resolves it to a typed pin — exactly one Project with
that root makes the pin managed, none makes it a candidate, more than one is
refused — with `uid:<uid>` available to name a Project directly.

A pre-v2 file of bare paths is read without being rewritten and migrated only on
request with `projmux pin project migrate`; see
[upgrading.md](upgrading.md#pins-are-typed-and-migrate-on-request) for the
per-line outcomes and the ambiguity refusal.

## Legacy Project Layout Files

Older checkouts may already have named layout files in the project-local
storage directory:

```text
<project>/.projmux/layouts/<name>.toml
```

The project context comes from `PROJMUX_CWD` when set, otherwise projmux walks
upward from the current directory to the nearest `.projmux` or `.git` marker.
Files outside that project tree are not discovered. No current projmux surface
opens or writes these files: closed-Project startup does not offer layout
choices, and Settings no longer saves a named layout from a live session. The
format stays documented so existing files can be recognized, and a declared
startup `command` stays behind project automation trust (see
[hooks.md](hooks.md#trust-model)).

The schema describes Windows and Panes:

```toml
schema_version = 1
description = "Daily dev"
mode = "inherit-autosave" # default; or "fresh-each-time"
default_cwd = "${PROJMUX_CWD}"

[[windows]]
index = 0
name = "main"
layout = "..."
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
command = "make watch"
```

`command` records a startup recipe. Panes without `command` may use `recipe = "shell"`. Supported
interpolation placeholders are limited to `${PROJMUX_CWD}` and
`${PROJMUX_SESSION}`; other `${...}` values are rejected during load.

Unknown fields and unknown sections are ignored so future schema additions do
not break older files. The built-in parser only accepts quoted strings and
integer values for the known fields above; it does not implement the full TOML
language.

## Keymap File

Settings > Keybindings is the normal in-app editor for action keys. It lists
actions with the current active keys and one state: Default, Custom, Available,
or Unbound. Open an action to see the action label, state, the action's
target/result/placement/anchor and shipped handler, a flat Keys list, and
Options. Create flows start from `+ Add key` or `Enter key name manually`,
deletion and `Test delivery` live under each key's detail, and action-level
state changes such as Unbind or Reset live under Options. Successful saves report the
keybinding as saved; failures identify the stage that failed.

Raw sequences that cannot be safely represented as a direct keybinding are not
persisted. Use Settings to save a custom key. When key delivery needs
terminal-layer remediation, first try the key in `projmux shell`, then run
`projmux setup` from the raw terminal, then use `projmux setup terminal` for
supported terminal adapters.

`~/.config/projmux/keymap.toml` can also be edited by hand. When the file is
absent, generated tmux config stays on the built-in defaults.

Supported schema:

```toml
schema_version = 2

[bindings."project-sidebar.toggle"]
keys = ["M-1", "M-a"]
sequences = ["C-k C-p"]

[bindings."window.create"]
keys = ["C-t"]

[bindings."project-sidebar.project.pin-toggle"]
keys = ["M-p", "p"]

[bindings."resource-inspector.open"]
keys = ["M-u"]
```

Each table is `[bindings.<action-id>]`. Supported keys are:

| Key | Meaning |
| --- | --- |
| `keys` | A list of no-prefix tmux plain chords such as `M-a`, `C-t`, or `M-S-Left`. |
| `plain` | Legacy single-primary replacement. Still read, but not written by Settings. |
| `sequences` | Additive two-to-four-stroke triggers such as `C-k C-p`; manual-file/runtime support only in Phase 0. |

The root `schema_version` marker is the file's own version. A file without one
is v0 and uses the runtime action ids (`ProjectSidebarToggle`, `new-window`,
`Sidebar:PinProject`); v1 uses canonical dotted ids, and
`schema_version = 2` adds `sequences`. All versions read — the v0 spelling of
every action is a permanent read alias — and migration to v2 runs at
`projmux config apply` or at the first Settings key save. A v1 migration is a
marker-only edit with a digest-named pre-v2 backup. [docs/keybindings.md](keybindings.md#schema-versions) has the full
table, the upgrade ordering and the downgrade procedure.

This marker is a separate version domain from the CLI resource registry's
`apiVersion: projmux.io/v1alpha1` / camelCase `schemaVersion: 3` envelope. The
two have separate markers, separate backups and separate rollbacks; neither one
failing affects the other. A successful Registry v1/v2 → v3 migration keeps its
private repair/loss evidence at `<exact-versioned-backup>.migration-report.json`,
including that backup's absolute path and SHA-256; failed or repeated passes
publish no report.

Legacy `prefix = ...` entries still parse during migration so existing files
do not break. Settings preserves existing prefix entries when rewriting the
file, but does not create new prefix keys, and generated tmux config no longer
binds the old action prefix chords.

Settings > Keybindings names `AISplitPickerToggle` as the AI split popup
picker toggle. That action opens or closes the picker UI and is separate from
the direct AI pane actions, `ai-split-right` and `ai-split-down`. The direct
actions create a new managed AI pane each time they run, leaving existing AI
panes in place. `right` and `down` choose where the new pane is created.

Use an empty `keys` list to disable direct plain keys for the action when
editing the file by hand:

```toml
[bindings."project-sidebar.toggle"]
keys = []
```

In Settings, reset removes the saved override and returns to the built-in
default. Popup *mode* names such as `sessionizer-sidebar` are not action ids and
resolve to nothing; a migration preserves such a table and reports it rather
than remapping it. Internal picker commands keep surface-local conflict domains
under their canonical ids (`session-picker.*`, `notification-sidebar.*`,
`project-sidebar.*`, `settings.*`) and remain visible in Settings when
catalogued.

`resource-inspector.open` (v0 `Resources:Open`) is a user-configurable direct
popup action with no built-in shortcut. Every configured alias renders the
canonical client-scoped body
`projmux internal tmux popup-toggle --client #{client_tty} resource-inspector`; pressing
the same alias again closes only that client's popup. It remains available on
Linux/tmux even when the `Status Bar > Resources` live-resource segment is off.

The Settings writer is deterministic and rewrites the supported saved subset
only. If the existing file has parse errors or unknown action IDs, Settings
shows the keymap error row and refuses to overwrite it until the file is fixed.

The file currently affects generated tmux config from `projmux config render
standalone`, `projmux config render app`, `projmux config apply`, the hidden
`projmux internal tmux install` / `install-app` installer plumbing, and `projmux shell`.
The render routes never write the keymap; `projmux config apply` migrates it
first and only applies once that succeeds. Terminal remediation adapters such as
Ghostty and Windows Terminal install built-in plain-byte mappings where needed;
they do not read `keymap.toml` or copy saved keys into terminal configs.
Changing terminal-layer mappings still requires rerunning `projmux setup
terminal` and restarting the terminal where that terminal requires it.

When a chord is overridden, projmux emits unbinds for both the stale default
chord and the replacement before binding the merged action. Popup and floating
UI actions still route through `tmux popup-toggle`, so pressing the same
configured key opens and closes the popup.

## Theme Resolver Foundation

Theme is a global user preference. The effective theme resolves from the global
user theme plus a built-in fallback only:

```text
~/.config/projmux/config.toml
built-in fallback preset
```

Settings edits the global `[theme]` in `~/.config/projmux/config.toml`. The
Effective theme view shows the final global > built-in fallback value for each
field with source labels: `global` or `fallback`. Saving or resetting a theme
value live-applies it: projmux regenerates the generated tmux config and, when
Settings runs inside tmux, `tmux source-file`-reloads it so a running server
repaints immediately. Outside tmux the save still succeeds and the report
prints `Next: run \`projmux config apply\`` to sync a running server.

The `background`, `surface`, `status_background`, `surface_active`, and
`pane_active_bg` tokens additionally accept the value `default` ("Terminal
default" in Settings) to keep that role at the terminal background. Priority is
**explicit `default` > preset fill > unset (fallback)**, so picking a preset and
then setting (for example) `background = "default"` keeps every other token
preset-filled while the pane body stays at the terminal default (`window-style
"bg=default"`). Set `surface = "default"` separately when popup/native frame
backgrounds should inherit the terminal background, set
`status_background = "default"` when the bottom status line should do the same,
and set `pane_active_bg = "default"` when the active pane should not be tinted.
`default` is only valid on these background-like tokens. See
`docs/theme-palette.md` for the full sentinel contract.

Project `.projmux/config.toml` `[theme]` is **deprecated and ignored**: it is no
longer an effective theme source and does not influence the native picker,
statusbar, popup, or any tmux chrome. Settings has no Project theme tab, project
override editor, project theme reset action, or `project` source label — both
the global theme editor and the effective theme view live under the Global tab.
See `docs/upgrading.md` for the migration note for existing project `[theme]`
users.

Renderer adapters can apply an already resolved `EffectiveTheme` to native
picker frame `surface` / `chrome_foreground` SGR, tmux window background tokens,
and the bottom status bar `status_background` token. Settings and native project
picker surfaces load global `[theme]` values through the shared effective-theme
source. Native picker frames also apply the built-in fallback `surface` /
`chrome_foreground` tokens so picker-owned padding, empty rows, footer rows, and
preview gaps do not inherit the terminal default background.

Active pane focus is part of the theme app chrome. The active pane is marked by
an active border (`pane-active-border-style`, fallback cyan `colour51`, the
public `focus` token) and a subtle dark background tint (`window-active-style`,
fallback `colour234`, the public `pane_active_bg` token); inactive panes follow
the public `background` token (tmux `window-style`, unset keeps `bg=default`).
tmux draws a single shared border between adjacent panes, so a full active-pane
rectangle is not guaranteed; focus is reinforced by the tint plus the
`pane-border-status top` topic line. This chrome preserves `pane-border-status
top`, pane topics, AI badges, and visible pane labels.

Native picker popups launched through `projmux internal tmux popup-toggle` also pass a
per-popup tmux 3.4 `display-popup -s` body style using the effective theme
`surface` / `chrome_foreground` tmux tokens (popup/native backgrounds follow
`surface`, while the bottom status bar follows `status_background`). This styles
only the tmux popup body
before the native renderer draws. It does not set global `popup-style` or
`popup-border-style`, and it does not change shell pane backgrounds,
`default-style`, `window-style`, OSC terminal backgrounds, or the general
status/window palette.

Resolver schema shape:

```toml
[theme]
preset = "projmux"
background = "default"
surface = "default"
status_background = "#182226"
surface_active = "#2c383d"
chrome_foreground = "#d8e0e4"
text_primary = "#d8e0e4"
muted = "#75848c"
accent = "#7ac7ad"
critical = "#ff6b6b"
warning = "#ffcc66"
progress = "#ffcc66"
success = "#5faf87"
action_required = "#ffaf00"
pane_active_bg = "default"
focus = "#00ffff"
provenance = "#ff8700"
```

`text_primary` controls primary content text in native terminal-rendered UI.
`chrome_foreground` controls frame, title, search, status, border-adjacent, and
other app chrome foreground roles. The older `foreground` key is still accepted
as a legacy alias/fill value: when present, it fills `text_primary` and
`chrome_foreground` unless either new key is explicitly set. New configs should
prefer the split keys, and Settings presents the split names rather than
encouraging writes to `foreground`.

`progress`, `success`, and `action_required` are the AI/status colors (progress
yellow, success green, action-required amber-orange). `action_required` is the
AI "needs input/approval" badge color and is intentionally independent of
`critical` — repainting `critical` never changes it. `pane_active_bg` is the
active-pane background tint, and `focus` is the active-pane border color. Each
of these is a public token: leave it unset to keep the historical built-in
color, or set it to repaint the matching chrome.

`provenance` is the compact usage label color for a provider row whose numbers
came from a fallback data source rather than its authoritative one (today: a
Codex row outside a healthy app-server). It is independent of `warning` and
`critical`, which stay reserved for usage thresholds, and of the ordinary AI
label color; the fallback value is orange. See `docs/usage-tracking.md` for what
the label looks like and `docs/theme-palette.md` for the preset contract.

Supported presets are `projmux`, `high-contrast`, `blue-hour`, `carbon-violet`,
`daylight`, `ember`, `forest`, and `rose`. `daylight` is the fully-light
preset; the others are dark. Preset colors paint projmux chrome only (status
bar, popups, pane borders/tint) — pane contents follow the terminal theme, so
`daylight` pairs best with a light terminal theme. A preset fills
missing color tokens, and explicit color tokens override preset values. Tokens
the global theme leaves unset fall through to the built-in fallback preset.
Terminal-default backgrounds are configured per token with the `default`
sentinel rather than through separate terminal preset variants.

Unknown presets and invalid color values invalidate only the global theme
source and produce resolver warnings; the built-in fallback still resolves
normally.
Colors are `#RRGGBB`. Settings edits colors through a preset selector, swatch
rows, and a hex input page. Native truecolor renderers and tmux style roles use
the exact hex value; 256-color mappings are retained only for renderer paths
that explicitly require xterm `colourN`/ANSI-256 colors. Generated tmux config
adds `xterm*:RGB` to `terminal-features` so capable terminals render exact
theme hex values instead of tmux downsampling them to the nearest 256-color
entry. The theme has no font keys: `font_family` and `font_size` were removed in
Phase 1b because tmux/ANSI rendering cannot force a terminal font. Leftover font
keys in an existing config are accepted but ignored. See `docs/upgrading.md`.

## UI Locale

The UI locale can be pinned globally or left on automatic detection.

Preferred interactive path:

- `Settings > Appearance > Language / Locale`

Global config path:

```text
~/.config/projmux/config.toml
```

Schema:

```toml
[ui]
locale = "auto" # auto | en-US | ko-KR
```

Resolution priority is:

1. `PROJMUX_LOCALE`
2. global/user `[ui] locale`
3. auto-detected environment: `LC_ALL`, then `LC_MESSAGES`, then `LANG`
4. built-in fallback `en-US`

`auto` means detect from the environment. In Settings, the `auto` detail shows
the currently detected locale and source. Supported UI locales are `en-US` and
`ko-KR`; unsupported tags fall back to `en-US` and the Settings detail shows a
warning with the unsupported value and source.

Project-local locale override is not part of the runtime policy. Locale is a
user/global preference in this release.

## Codex app-server health

Settings > AI includes a read-only `Codex control plane` row. It reports one of
`App Server`, `Hook fallback`, or `Unavailable`, together with the existing
effective reason. Endpoint readiness, running executable/version, official
daemon-manager ownership, and remote-control capability are separate closed
axes. Separate `probe_reason` and `install_capability` fields preserve the
app-server root cause and bounded PATH/managed-payload topology. Lifecycle
outcome/reason and sanitized CLI, managed, and running versions remain
independent. Read-only surfaces report `not-attempted/read-only`.
`projmux doctor --section integrations` and explicit support reports expose the
same bounded fields without executable/socket paths, prompts, tokens, process
output, or response payloads.

There is no app-server source setting or environment override. Authority is a
capability result, not a preference: Projmux probes the existing local control
socket through `codex app-server proxy`, reads official manager evidence through
`codex app-server daemon version`, and reads remote-control state through
`remoteControl/status/read`, all with short timeouts. An older endpoint that
does not expose the last method reports `unsupported` on that axis without
hiding endpoint readiness. Doctor, Settings, and support reports never start or
otherwise mutate the daemon, configuration, login state, or control socket.

An `external-cli-only` install capability acknowledges that the ordinary CLI
exists while the canonical managed daemon payload was not observed. It does
not identify a package manager. Install topology is not manager ownership:
only the official daemon response's backend field proves a managed process.

The Codex integration has a lifecycle seam for later native features. Only an
actual native user action may use it. A ready unmanaged, version-skewed, or
ownership/version-unknown endpoint is refused without mutation and reports the
shared-client interruption risk plus bounded operator recovery. Only an exact
missing or connection-refused default control socket is start-eligible. That path invokes the
official idempotent `codex app-server daemon start` command at most once per
in-flight process decision, then retries proxy initialization with a bounded
backoff. Projmux never automatically stops, kills, restarts, adopts, or enables
remote control on the shared app server.

The `Codex` row in the provider picker launches immediately through the
canonical create route. It does not start or probe the app-server, call
`model/list`, or add `--model` or `model_reasoning_effort`; the Codex process
therefore keeps its own configured defaults. The picker has no per-launch model
or effort row.

`projmux agent review` starts `review/start` only for a Running Codex Agent whose
Registry Agent, owned Pane, activation generation, stored thread, and live Pane
thread all match exactly. Review availability is based on the negotiated
app-server version for that connection and is confirmed by the method call;
Codex 0.149 does not advertise a separate review capability bit. Older versions
or a method-not-found response are reported explicitly as unavailable. The
initial `review/start` response is projected into Agent interaction lifecycle;
the lifecycle observer described below owns later app-server terminal events.

A natively created or resumed Codex Agent keeps a content-free lifecycle
observer on its exact Agent UID, Pane UID/runtime handle, activation generation,
and thread ID. While its initialized proxy connection and snapshot are current,
the app server is the only attention authority: active, idle, waiting for input,
and exact unresolved approval requests project the Agent interaction and badge.
Only an exact successful `turn/completed` projects response-complete and queues a
completion notification; failed and interrupted turns become idle. Disconnect
and thread unload first invalidate the epoch and clear stale attention, then
enable hook fallback. A reconnect starts a new epoch from `thread/read`; events
from older epochs or other identities are ignored.

`describe agent` and the Codex Agent event Settings page report the effective
lifecycle source, a closed reason, and active/pending/inactive epoch status.
These diagnostics never retain prompts, reasoning, output, approval reasons, or
diff content. Settings aggregates multiple live Codex Panes as counts and says
`mixed` when native and fallback authority coexist.

Projmux does not install, bootstrap, restart, stop, or reconfigure the Codex
daemon, does not stop the shared daemon when Projmux exits, and does not manage
Codex authentication. There is no custom socket or remote-control setting.
The semantic approval/completion policy below drives the same badge, queue, and
desktop intent for both app-server and hook-fallback authority. Existing
`ai-hook-actions.json` values remain byte-for-byte unchanged and, when an exact
runtime override exists, take precedence only while hook fallback is current;
they are never inferred or copied into the semantic policy store.

## Split Start Directory

New splits start in the owner Project root. `[ai] split_cwd_from = "pane"`
starts UI splits in the active Pane's live directory instead, while that
directory is inside the owner Project root. Settings exposes the same value as
`AI > New splits start in` (`Project root` / `Current Pane directory`); choosing
one writes only the global `[ai] split_cwd_from`.

Config paths (global and project both honored):

```text
~/.config/projmux/config.toml      # global
<project>/.projmux/config.toml     # project
```

Schema:

```toml
[ai]
split_cwd_from = "project" # project|pane; where a new shell or Agent split starts
```

Resolution depends on who starts the split.

**CLI** (`create pane`, `create agent`, and the provider shortcuts) — the flag
only:

1. `--cwd-from project|pane`
2. otherwise `project`

The CLI opens no config file for this decision. A CLI result is determined by
its arguments, so scripts and automation keep starting in the Project root when
someone changes this setting.

**UI** (keybinding splits, the launcher, the resume picker's new row, and the
Pane menu) — the config tiers:

1. an explicit per-call value (flag)
2. project `[ai] split_cwd_from`, read from the owner Project root
3. global/user `[ai] split_cwd_from`
4. built-in default (`project`)

There is no environment override, and an unknown value skips its own tier. The
project tier is always read from the owner Project root, so where a Pane sits
never changes which config decides. The Settings row shows the UI result and the
tier that decided it (`project`, `global`, or `default`).

With `pane` selected, the active Pane is the origin of the split: the Pane a
keybinding ran in, the Pane a right-click menu was opened on, or the anchor Pane
a CLI create resolved. Its live directory is used only when it is inside the
owner Project root. `$HOME`, another registered Project tree, a directory that no
longer exists, and an unreadable directory all start in the Project root and
report one line naming the reason and that root — on stderr for a CLI create
with `--cwd-from pane`, and as one client message for a split started from the
UI. A split is never refused for this reason. `--cwd` on `create agent` still
names the Agent working directory outright and ignores this setting.

## Enabled AI Providers

Disabled providers are a central policy, stored in
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-enabled-agents`, that the CLI
honors as well as the UI: `create agent`, `create window --provider`, and
`agent resume` refuse a disabled provider, and the refusal names the command
that re-enables it.
`projmux config providers` lists every provider as `<id> enabled` or
`<id> disabled`; `projmux config providers --enable <id>` and
`--disable <id>` change one through the same writer as
`Settings > Global > AI > Enabled providers`. A missing file means every
provider is enabled; disabling every provider persists as none enabled.

## AI Resume Picker

The Agent resume picker lists the most recent
deduplicated Claude/Codex/Antigravity resume sessions. The number of rows it
shows and how far below the current directory it scans are both configurable;
the defaults are 30 rows and depth 0 (the current directory only).

Preferred interactive path:

- `Settings > Global > AI > Agent Resume Picker` (`Picker limit`, `Scan depth`)

Config paths (global and project both honored):

```text
~/.config/projmux/config.toml      # global
<project>/.projmux/config.toml     # project
```

Schema:

```toml
[ai]
resume_picker_limit = 30 # 1-100; how many recent sessions the picker lists
resume_scan_depth = 0    # 0-8; include sessions started in cwd child dirs
```

Resolution priority (each key resolves independently) is:

1. `PROJMUX_AI_RESUME_PICKER_LIMIT` / `PROJMUX_AI_RESUME_SCAN_DEPTH`
2. project `[ai]` key
3. global/user `[ai]` key
4. built-in default (`30` rows, depth `0`)

`resume_picker_limit` is clamped to `1`-`100`; a missing or non-positive value
falls back to the default. `resume_scan_depth` is clamped to `0`-`8`: depth `0`
lists only sessions whose recorded working directory matches the current one
(the historical behavior), while depth `N` also lists sessions started up to `N`
levels below it — useful from a monorepo or parent directory. The match is a
path-tree filter on each session's recorded cwd, so parent and sibling
directories are never included. At depth `>0` the picker adds a relative-cwd
column (`./`, `./web`, `./api`) so child-directory sessions are easy to tell
apart. A missing or zero depth is identical to the historical behavior. Settings
edits write the global config.

Codex uses the local app-server conversation catalog as its primary source.
Each picker invocation follows opaque `thread/list` cursors to completion with
explicit `cli`, `vscode`, and `appServer` source kinds, non-archived filtering,
provider recency ordering, and exact-cwd filtering at depth 0. Wider depth is a
bounded client-side path-tree filter. Native rows use the exact thread id, the
provider-owned name (or only a short id when unnamed), git branch, and runtime
status; prompt preview and transcript turns are never read for titles. If the
native catalog is unavailable, unsupported, malformed, or returns invalid
pagination, that invocation discards all native rows and performs one rollout
fallback. The picker annotates Codex rows with source, confidence, runtime
status, and a closed fallback reason, so native and rollout rows are never
silently merged.

Antigravity uses the upstream v1.1.12 current-storage boundary before its
legacy history fallback. `cache/last_conversations.json` contributes the latest
UUID mapped to a matching workspace; `cache/conversation_metadata.json`
contributes only rows that carry a valid UUID, workspace URI/path, and summary.
Both require an exact regular `conversations/<uuid>.db` and use only its
existence/mtime. They do not open SQLite content and do not treat `.db-wal`,
`.db-shm`, symlinks, or arbitrary paths as conversations. The cache provides a
latest-session floor rather than complete history. Missing/malformed cache,
workspace-less metadata, and stale mappings degrade to legacy `history.jsonl`
without changing the shared exact/depth/sort/cap behavior.
Live hook resume metadata is a separate high-confidence lane and is not a
disk-picker candidate.

## Release channel

Which release line this install is offered updates from. The axis is orthogonal
to the install path: every installer can be judged on either channel.

Files:

```text
~/.config/projmux/config.toml      # global/user
```

Schema:

```toml
[update]
release_channel = "rc"   # "stable" (default) or "rc"
```

Set it from Settings > About > Updates > Release channel. The default is
`stable`, and it is the default in the strong sense: an install that has never
touched the toggle, an empty value, and a channel this binary does not
recognise all resolve to `stable`, so no configuration mistake can put an
install on prereleases it never asked for.

Resolution priority is:

1. `[update] release_channel`, once the key exists
2. `PROJMUX_RELEASE_CHANNEL`
3. built-in default (`stable`)

The stored setting takes the axis away from the environment only once it
exists, which is why merely opening the Settings row does not write it. An
install driven by `PROJMUX_RELEASE_CHANNEL` therefore keeps its opt-in until
somebody actually uses the toggle — and after that, turning the toggle off puts
the install back on the stable line even while the environment variable is
still set. Turning it off does not downgrade a prerelease that is already
installed; that install stays put until its stable line ships.

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `PROJMUX_PROJDIR` | Explicit primary project root. Accepts an OS-native PATH-style multi-value: the first non-empty entry is the primary root and later entries are prepended to managed-root discovery. The primary value is memoized to `~/.config/projmux/projdir`. The legacy `PROJDIR` and `RP` env vars are no longer honored. |
| `PROJMUX_MANAGED_ROOTS` | Search-root override. Uses the OS-native path-list separator and takes priority over the saved workdirs file and default weak probes. |
| `TMUX_SESSIONIZER_ROOTS` | Legacy alias still honored at runtime for managed roots. |
| `PROJMUX_LOCALE` | UI locale override. `auto` resumes detection; `en-US` and `ko-KR` pin supported locales. Unsupported tags fall back to `en-US` and surface a Settings warning. |
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in Linux/WSL sender. Separate from declarative `[hooks.send-noti]`. |
| `PROJMUX_AI_RESUME_PICKER_LIMIT` | Overrides the AI resume picker row count (`[ai] resume_picker_limit`). Clamped to 1-100; takes priority over project and global config. |
| `PROJMUX_AI_RESUME_SCAN_DEPTH` | Overrides the AI resume picker cwd-tree scan depth (`[ai] resume_scan_depth`). Clamped to 0-8; takes priority over project and global config. Depth 0 keeps the exact-cwd behavior. |
| `PROJMUX_NOTIFY_HOOK_DEPTH` | Internal recursion guard for `send-noti` hooks. Depth `>= 1` suppresses nested hook dispatch while still allowing the queue write itself. |
| `PROJMUX_NOTIFY_EXPIRE_MS` | AI desktop notification expiration in milliseconds. Defaults to `5000`; unset, zero, negative, and non-numeric values fall back to the default. |
| `PROJMUX_DESKTOP_NOTIFY_MODE` | OS desktop notification mode override. `off` / `none` / `notify` (case insensitive). When set, this takes priority over every other resolution rung. The in-app notify queue is not affected. The retired `raise` / `auto-raise` / `autoraise` literals are still accepted and read as `notify`. |
| `PROJMUX_DESKTOP_NOTIFY` | Legacy on/off override kept for backward compatibility. `on` maps to `notify`, `off` maps to `none`. Honored only when `PROJMUX_DESKTOP_NOTIFY_MODE` is unset. |
| `PROJMUX_WSL_TOAST_ICON_DIR` | Directory used when copying the WSL toast icon into a Windows-readable path. |
| `PROJMUX_USAGE_STATE_DIR` | Override directory for AI usage snapshots. Defaults to `<state>/projmux/usage`. Point this at a synced directory to share authoritative usage across machines. The value is used as given, with no `~` expansion. |
| `PROJMUX_USAGE_DEBUG` | When non-empty, prints adapter errors from the `projmux internal status usage` renderer to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated. Read but ignored; limits now come from upstream APIs and local Codex rollout state. |
| `PROJMUX_SESSIONSTATE_AUTOSAVE` | Ignored. Project snapshots and their auto-save were removed. |
| `PROJMUX_SESSIONSTATE_DEBUG` | Ignored. It only gated stderr for the removed quiet autosave. |
| `PROJMUX_FOCUS_DEBUG` | When non-empty, `projmux focus` prints one telemetry line to stderr with the target, session, window, pane, socket, client, source, and kind. |
| `PROJMUX_INSTALLER` | Installer source hint used by update flows. npm installs set this automatically; advanced release installs can set `github-release`. |
| `PROJMUX_RELEASE_CHANNEL` | Release channel the update judgment is made against, orthogonal to `PROJMUX_INSTALLER`. Only an exact `rc` opts in; unset, empty, and unrecognised values all mean the default `stable` channel, which never sees a prerelease. An rc install is answered with whichever of the stable and rc lines is newer, so it returns to stable as soon as that line ships. Read only until `[update] release_channel` exists; see [Release channel](#release-channel). |
| `PROJMUX_SHELL_UPDATE_CHECK_TIMEOUT_MS` | Timeout in milliseconds for the best-effort release check attempted by `projmux shell` when the update cache is missing or stale. Invalid, zero, or negative values use the default. |

## Welcome State

`projmux shell` still reads and rewrites legacy per-version welcome state under
the projmux state directory for attach-popup compatibility, normally:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/welcomed-v<version>.json
```

The current schema is:

```json
{
  "version": 1,
  "last_welcomed_version": "0.6.3",
  "welcomed_at": "2026-05-21T00:00:00Z",
  "skip_version": "0.6.3",
  "skipped_at": "2026-05-21T00:00:00Z"
}
```

This file no longer suppresses the shell-entry welcome. `skip_version` and
`last_welcomed_version` remain readable for legacy state and pending attach
popup compatibility, but the automatic shell prompt now uses release skip state
instead.

Update prompt skips are stored by latest release tag under the update cache
directory:

```text
${XDG_CACHE_HOME:-$HOME/.cache}/projmux/update-skip.json
```

When `tag_name` matches the fresh cached latest release tag, `projmux shell`
continues without offering the update actions. A newer latest tag makes the
prompt eligible again.

Example:

```sh
export PROJMUX_PROJDIR="/main/repos:/srv/work/repos"
```

On Linux and macOS the separator is `:`. On Windows-style paths the separator
is `;`.

## tmux Primary Discovery Root Option

The switch command also reads this tmux option:

```tmux
set-option -g @projmux_projdir /path/to/repos
```

An env `PROJMUX_PROJDIR` value takes priority over the tmux option. The tmux
option takes priority over the saved projdir file.

## Notifications

When `PROJMUX_NOTIFY_HOOK` is unset, projmux uses:

- `notify-send` on Linux.
- PowerShell toasts on WSL.

When the hook is set, projmux invokes it with positional arguments:

```text
summary body urgency app-name tag group icon-path
```

This environment variable remains the imperative "replace desktop notification
sender" escape hatch. It does not run through the declarative hook trust/config
system and it does not receive the notify-queue JSON payload. For additive
forwarding after a successful queue write, prefer `[hooks.send-noti]` in
`config.toml`. The `urgency` argument is the OS notification urgency, not the
internal notify-queue severity; AI desktop notifications default to OS urgency
`normal` even when the statusbar, Alt-2, and persistent queue row remain
critical.

AI desktop notifications are transient by default. Linux `notify-send` receives
`--urgency=normal` and `--expire-time=${PROJMUX_NOTIFY_EXPIRE_MS:-5000}`. WSL
PowerShell Toasts use `duration="short"` and set `ExpirationTime` explicitly.
If WSL falls back to `wsl-notify-send.exe`, urgency and expiration are
best-effort because that adapter does not expose a stable equivalent for every
host setup.

`[hooks.send-noti]` and `PROJMUX_NOTIFY_HOOK` can coexist:

- `PROJMUX_NOTIFY_HOOK` replaces the built-in Linux/WSL desktop sender.
- `[hooks.send-noti]` fires after the queue write and does not replace desktop
  notifications.
- `PROJMUX_NOTIFY_HOOK_DEPTH` prevents a `send-noti` hook that calls
  `projmux create notification` from recursively re-triggering itself.

The app-name argument is `com.crevisse.projmux` (reverse-domain id used as
the Linux `--app-name`, the macOS sender label, and the Windows
`AppUserModelID`). Earlier releases used `projmux.TmuxCodex`; the new id
ships with a one-shot Windows cleanup that removes the legacy Start Menu
shortcut and registry entry the first time projmux runs.

AI desktop notifications collapse repeated pane-local notifications using a
seconds window. Resolution priority is:

1. `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS`
2. Settings saved value at
   `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-notify-dedupe-seconds`
3. default `120`

Settings exposes this at
`Settings > Global > Notifications > Desktop delivery > Dedupe window`.
The value is stored as integer seconds and applies only to AI desktop
notification dispatch. The tmux bell fallback keeps its fixed 5 second
dedupe window.

AI hook runtime actions are stored at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json
```

Settings exposes this at `Settings > Notifications > Agent event behavior`.
The file maps provider/event names to `notify`, `state`, or `quiet` and
overrides catalog `action` values during ingest, including known Codex and
Claude events. It does not change hook installation; `projmux agent integrate`
continues to use the embedded/local catalog `install` fields.

Codex native lifecycle semantics are stored separately at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-semantic-policies.json
```

The two closed events are `approval_required` and `response_complete`. Each can
be `notify` (badge + queue + desktop), `state` (badge only), or `quiet` (no
badge, queue, or desktop). Settings labels these choices `Notify`, `State only`,
and `Quiet`. The selected intent applies to both the app-server epoch and hook
fallback. An explicit raw `PermissionRequest` or `Stop` runtime override takes
precedence only during hook fallback; catalog defaults do not override this
semantic store. Saving this file does not infer from, rewrite, or normalize the
raw hook override file.

Delivery depends on the event handler. Specialized notify handlers, such as
Codex `PermissionRequest` and `Stop`, can write the in-app notify queue and use
the configured OS desktop notification path. Known Codex events without a
specialized handler can be runtime-overridden to `notify`, but that creates
only a generic in-app queue/sidebar/statusbar row; it does not fire OS toast,
`notify-send`, `PROJMUX_NOTIFY_HOOK`, or `[hooks.send-noti]`.

### Desktop notification mode

The OS-level dispatch carries two modes. The in-app notify queue, the
statusbar segment, and the attention badge stay live regardless of which
mode is active — only the toast / notify-send fan-out is gated here.

| Mode | On push | On click |
| --- | --- | --- |
| `off` / `none` | no toast | n/a |
| `notify` | toast / notify-send fires | no click action |

Desktop notification delivery never takes your host terminal window focus.
The Toast carries no `launch` payload, projmux registers no `projmux://`
protocol handler, and there is no auto-raise on push. `projmux focus` also
stops at the tmux layer in every mode: it switches the client, window, and
pane but never asks the host terminal window to come forward.

Resolution order (highest priority first):

1. `PROJMUX_DESKTOP_NOTIFY_MODE` env (`off` / `none` / `notify`).
2. `PROJMUX_DESKTOP_NOTIFY` env (legacy `on` / `off`; `on` → `notify`,
   `off` → `none`).
3. Saved Settings config
   `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/desktop-notify-mode`
   (`off` / `notify`).
4. Tmux global option `@projmux_desktop_notify_mode`.
5. Tmux global option `@projmux_desktop_notify` (legacy `1` / `0`, same
   mapping as the env above).
6. Default = `notify` on every platform, WSL + Windows Terminal included.

The retired `raise` mode was removed in 0.11.0. Wherever a mode literal is
read — env, saved file, or tmux option — `raise`, `auto-raise`, and
`autoraise` are still accepted and resolve to `notify` **in that same
source**, so an old value keeps pinning the resolution instead of falling
through to a lower-precedence rung. The alias never re-enables host-window
focus, a clickable Toast, or URI handler registration; it is not offered in
Settings and is never written back — the first Settings press or
`projmux config apply` replaces it with `notify`.

Migration is intentionally read-time. Users with the previous legacy
toggle set keep their behavior — `@projmux_desktop_notify=0` resolves to
`none`, `@projmux_desktop_notify=1` resolves to `notify`. The first
Settings press through the new row writes `desktop-notify-mode`, mirrors the
new value into `@projmux_desktop_notify_mode` when tmux is live, and leaves the
legacy key unused. No eager rewrite of tmux state.

Choose it from `Settings > Global > Notifications > Desktop delivery > Delivery mode`. The
Settings info row labels the effective source as `env`, `env (legacy)`,
`setting`, `setting (legacy)`, or `default` so users see which rung of
the cascade pinned the value. `projmux config apply` regenerates the live tmux
option from the saved Settings file; when that file is missing, apply writes
only the default for the current host.

Hook details for new-session lifecycle hooks and project-local
`.projmux/config.toml` live in [Hooks](hooks.md).
Settings exposes project-local executable automation under **Project >
Automation**. `[startup]` and generic `[env]` remain config-file compatibility
inputs and are not Settings authoring destinations.

### Toast click handler (WSL + Windows Terminal) — retired in 0.11.0

projmux used to register a `projmux://` URL scheme on the Windows side and
emit clickable Toasts whose click routed through the hidden `projmux internal focus` ingress.
That path is **removed**: desktop notifications are passive, carry no
`launch` payload, and projmux registers no protocol handler. Clicking a
projmux Toast does nothing.

Consequences for existing installs:

- A `HKCU\Software\Classes\projmux` key written by an earlier version is
  **not** removed automatically. Delete it manually if you want the scheme
  gone; leaving it in place is harmless because nothing produces the URI.
- The old public `focus --uri` compatibility input is removed. Machine-owned
  focus payloads use `projmux internal focus --uri`. No
  projmux code path produces such a URI any more.
- The `@projmux_uri_protocol_registered*` tmux markers are no longer read or
  written. Stale markers are inert.

The measurement trail for the retired design is kept for history in
[notify-os-focus-poc.md](notify-os-focus-poc.md).

## Usage Tracking

`projmux agent usage` and the status-bar usage segment store snapshots under:

```text
${PROJMUX_USAGE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/projmux/usage}/snapshots.json
```

See [Usage tracking](usage-tracking.md) for adapter behavior, throttling, and
failure handling.

## Project Startup

The Registry (`registry.json`) is the only saved Project state; see
[session-restore.md](session-restore.md). projmux keeps no separate Project
snapshot store. Nothing reads the former
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/sessions/*.json` snapshot
files; they are removed by `config apply`. The app tmux status tick saves
nothing: generated status lines carry no auto-save job, and the hidden
`internal tmux autosave-session-state` route that older generated configs
still call is a silent no-op that writes nothing and records no diagnostics.
The `sessionstate-autosave` and `sessionstate-autosave-interval` files and the
per-Project `sessionstate-projects/<session>/autosave` files are removed by
`config apply`, together with the `sessions/` and `sessionstate-projects/`
directories once they are empty. Any other entry there is kept, and listed
by the apply that removes the files.
`PROJMUX_SESSIONSTATE_AUTOSAVE` is still ignored without a warning.

Opening a registered closed Project from the Alt-1 sidebar shows a native
`Start project` step with exactly `Continue project` and
`Clear layout and open`. A root that is not a registered Project skips that
step and opens fresh, which registers it. There is no setting that skips the
step for a registered Project.
An explicit `Continue project` on a root that is not a registered Project
refuses with zero writes and points to `Clear layout and open`.
`Clear layout and open` clears only the saved Window and Agent layout: the
folder, its files, `.projmux/config.toml`, and trust stay. It confirms first,
then atomically replaces the old Project graph
with a new Project UID and a new canonical Window/shell UID pair. Exactly one
same-root Project claimant remains. The root directory,
Git/worktree data, and the trust decision remain unchanged. Esc returns to
Projects with zero writes. After
the startup mode is selected, project automation trust is evaluated if needed.
The Alt-1 sidebar opens trust as the shared client-scoped `Trust project
automation` popup instead of inline sidebar rows. The selected open
continuation runs in a detached tmux job that can close the sidebar before trust
without depending on the self-closing sidebar process to keep running.
Deny/cancel refreshes the original sidebar query/selection context with a
visible status message. Existing sessions switch directly without a startup
picker.

Default `projmux shell` does not open a startup picker before attach. It
derives the default app session identity and startup directory from the
current project context when available; otherwise it uses the `home` target
and home directory.

The retired closed-Project startup setting's file under
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/` is not read, and `config apply`
removes it; see [Upgrading](upgrading.md#closed-project-startup-setting-removed).

Interactive `projmux quit` offers only the quit row and `Cancel`. Neither it
nor `quit --yes` / `quit --force` saves Project state.

## Decoration Mode

Settings > Appearance controls optional icon decoration per surface:

- `off` is the default and avoids icon-font assumptions.
- `symbol` restores Nerd Font-style folder, git-provider, and bell icons.
- `emoji` uses emoji decorators. GitHub remotes use a cat-style mark, GitLab
  remotes use a fox-style mark, and other remotes use a generic git branch mark.

The per-surface saved values live at:

```text
~/.config/projmux/statusbar-decoration-cwd
~/.config/projmux/statusbar-decoration-git
~/.config/projmux/statusbar-decoration-notify
```

The legacy `~/.config/projmux/statusbar-decoration` value is still read as the
fallback default when a per-surface file is absent.

## Row 0 HUD Visibility

`Settings > Appearance > Status Bar` controls the two upper HUD components as
independent global presentation preferences:

- `Notifications HUD > Visible`
- `Agent Usage HUD > Visible`
- `Agent Usage HUD > Claude|Codex > Visible`
- each provider's supported HUD windows (`5h`, `Weekly`)

The saved values are `on` or `off` in these files:

```text
~/.config/projmux/statusbar-visibility-notifications-hud
~/.config/projmux/statusbar-visibility-agent-usage-hud
~/.config/projmux/statusbar-visibility-agent-usage-provider-claude
~/.config/projmux/statusbar-visibility-agent-usage-provider-codex
~/.config/projmux/statusbar-visibility-agent-usage-window-claude-5h
~/.config/projmux/statusbar-visibility-agent-usage-window-claude-weekly
~/.config/projmux/statusbar-visibility-agent-usage-window-codex-5h
~/.config/projmux/statusbar-visibility-agent-usage-window-codex-weekly
```

Missing, empty, and invalid values resolve to `on` except the Codex `5h`
window, whose ambient HUD default is `off`; an explicit saved `on` restores it.
Settings shows whether the effective value came from `saved` or `default` and
marks an invalid saved value as ignored. Saving a toggle regenerates the app
and standalone tmux output, and Settings source-reloads the generated app config
when it is running inside tmux.

Parent visibility gates only the effective projection. Turning the overall HUD
or a provider off does not rewrite its provider/window leaf files; turning the
parent back on restores the saved child selection. Provider rows follow the
usage-supported provider catalog. Window rows come only from the explicit HUD
capability map, so opaque quota buckets never create settings. Antigravity has
no usage source; `statusbar-visibility-agent-usage-*-antigravity*` files left by
older releases are ignored and never deleted.

Visibility does not enable or disable either producer. Hiding Notifications HUD
does not change the persistent queue, desktop delivery, or Notification
Sidebar. Hiding Agent Usage HUD does not change the `agent usage` command,
Provider snapshot cache, upstream API collection, quota, or refresh policy.
Provider/window leaves likewise filter only `projmux internal status usage`; explicit
`agent usage` table/JSON and the cached statusbar popup remain lossless.

## Row 1 Segment Visibility

`Settings > Appearance > Status Bar` controls these retained lower-row
components independently:

- `Project`
- `Working directory > Visible`
- `Git > Visible`
- `Clock`
- `Settings launcher`

Their global saved values are `on` or `off` in:

```text
~/.config/projmux/statusbar-visibility-project
~/.config/projmux/statusbar-visibility-working-directory
~/.config/projmux/statusbar-visibility-git
~/.config/projmux/statusbar-visibility-clock
~/.config/projmux/statusbar-visibility-settings-launcher
```

Missing, empty, and invalid values resolve to `on`; Settings reports the
effective value and whether it came from `saved` or `default`. Each save is an
atomic 0600 replacement, regenerates both app and standalone output, and
source-loads the generated app config when Settings is inside tmux. Off removes
the complete segment, including its mouse range and owned spacing. Working
directory/Git icon mode remains a separate setting, so icon `off` leaves the
text visible. Settings launcher `off` removes only its mouse chip; CLI and
keybinding entry remain.

Resources intentionally does not use one of these visibility files. Its
existing `~/.config/projmux/live-resources` value is the only enabled source
and controls both the segment and sampler/cache mutation.

## Live System Resources

`Settings > Appearance > Status Bar > Resources` controls the compact
CPU/memory segment on the lower status row. The saved global value is:

```text
~/.config/projmux/live-resources
```

Accepted values are `off` (default) and `on`. A missing, empty, or invalid
saved value resolves to `off` with source `default`; valid saved values report
source `saved`. The feature is available on
macOS, native Linux, and WSL. Linux reads procfs directly; macOS reads Mach host
statistics and `hw.memsize`. Neither path launches `top`, `vm_stat`, `free`,
PowerShell, or another metrics process. On macOS, available memory is free plus
inactive pages, matching the reclaimable-memory intent of Linux
`MemAvailable`. In WSL the values describe the Linux guest/VM view, not total
Windows host utilization. These are host-scoped values and do not attribute
usage to a pane, window, project, or session.

When enabled, the compact segment is also the clickable `resources` statusbar
range and opens the Resource Inspector. Turning Resources off hides the segment
and stops its host sampler/cache writes; it does not disable the explicit
`projmux resources` inspector or a custom-bound
`Resources:Open` action. Inspector samples are memory-only for the popup
lifetime and are unrelated to the status segment's host CPU reference cache.

The display policy is fixed rather than configurable: CPU is normal below 70%,
warning at 70–89%, and critical at 90% or above; memory is normal below 75%,
warning at 75–89%, and critical at 90% or above. The two values are classified
and styled independently. Normal and unavailable (`--`) values use the
secondary status-text theme role, warnings use the warning role, and critical
values use the bold critical role. Visible severity words are omitted and each
percent uses a fixed four-column slot, including `%`, so metric transitions do
not resize the segment. No threshold values are stored in config.

The CPU delta cache is internal state at
`${XDG_STATE_HOME:-~/.local/state}/projmux/live-resources-sample.json`.
CPU reference samples older than 30 seconds are ignored and replaced on the
next refresh.

## Web Settings (web.toml)

The web UI keeps its own settings layer in one file:

```text
~/.config/projmux/web.toml     # $XDG_CONFIG_HOME/projmux/web.toml when set
```

A *front* setting changed in the web UI is written to `web.toml` only; no TUI
or central file is touched and no tmux config is regenerated. The web reads each
front setting as `web.toml value ?? the layer it belongs to ?? built-in
default`, so a key the web never changed keeps following the TUI or central
value. The TUI, the keybinding splits and the CLI never read `web.toml`. The
file does not exist until the first web change; there is nothing to migrate.

Settings live in four layers:

| Layer | Where | What |
| --- | --- | --- |
| central | `config.toml` central keys (`[ai] split_cwd_from`, `[ui] locale`, hooks, env, startup), `ai-enabled-agents`, `live-resources`, `projdir`, `workdirs`, `pins`, `tags`, `project-hooks`, `desktop-notify-mode`, `ai-notify-dedupe-seconds`, `ai-hook-actions.json`, `ai-semantic-policies.json`, `ai-hooks.d/`, `personas/` | product behavior every surface shares |
| TUI | `statusbar-visibility-*`, `statusbar-decoration*`, `ai-badge-style`, `runtime-diagnostics-visibility`, `keymap.toml`, `tmux-ai-split-mode`, `config.toml` `[theme]`, `[ui] native_keys`, `[ai] resume_*` | how the terminal looks and launches |
| WEB | `web.toml` | the web's own values for front settings |
| browser | `localStorage` `projmux.web.*` | per-browser conveniences, never read by the server |

`[ai] split_cwd_from` in `config.toml` is the central default the TUI reads; the
web reads it when `web.toml` has no `[ai] split_cwd_from`.

Front settings, by web API key (`PATCH /api/v1/web/settings`), with their place
in `web.toml` and the layer they fall back to:

| API key | `web.toml` | Falls back to |
| --- | --- | --- |
| `ai.splitCwdFrom` | `[ai] split_cwd_from = "project"\|"pane"` | `config.toml` `[ai] split_cwd_from` |
| `statusbar.notifications` | `[statusbar] notifications = true\|false` | `statusbar-visibility-notifications-hud` |
| `statusbar.project` | `[statusbar] project` | `statusbar-visibility-project` |
| `statusbar.working-directory` | `[statusbar] working_directory` | `statusbar-visibility-working-directory` |
| `statusbar.git` | `[statusbar] git` | `statusbar-visibility-git` |
| `statusbar.clock` | `[statusbar] clock` | `statusbar-visibility-clock` |
| `statusbar.usage` | `[statusbar.usage] visible` | `statusbar-visibility-agent-usage-hud` |
| `statusbar.usage.<provider>` | `[statusbar.usage.<provider>] visible` | `statusbar-visibility-agent-usage-provider-<provider>` |
| `statusbar.usage.<provider>.<window>` | `[statusbar.usage.<provider>] <window>` | `statusbar-visibility-agent-usage-window-<provider>-<window>` |

Every usage node that has children is a table naming itself `visible`, so the
file stays valid TOML. `<provider>` and `<window>` are the usage HUD
capabilities (`claude`, `codex`; `5h`, `weekly`), spelled in lower case. Usage
leaves overlay one by one and keep the TUI's effective rules: a provider that
is off hides its windows, and a window with no value anywhere uses its own
default (Codex `5h` is off).

Example:

```toml
[ai]
split_cwd_from = "pane"

[statusbar]
git = false
clock = false

[statusbar.usage]
visible = true

[statusbar.usage.codex]
5h = true
```

A web split resolves its start directory as: the request's `cwdFrom`, then the
owner Project's `.projmux/config.toml`, then `web.toml`, then the global
`config.toml`, then `project`. A Project's own config still beats a web change.

The file is a strict TOML subset: `#` comments, `[table]` headers of bare keys,
and `key = "string"` or `key = true|false` with bare keys. An unknown key, a
provider or window the HUD does not have, a value of the wrong kind, a repeated
key or table, arrays, and dotted or quoted keys are errors that name the file
and line, for example `~/.config/projmux/web.toml:3: unknown key
"statusbar.gti"`. While `web.toml` has such an error, the web settings and
status bar reads answer `409 refused` with that message, a web change is
refused without rewriting the file, and the web usage HUD reports it instead of
drawing cells. Nothing in the file is ignored silently.

The web writes the whole file on every change: a 0600 temp file in the same
directory, fsynced, then renamed over `web.toml`, in a fixed key order. A
reader sees the previous file or the new one, never a partial write. Comments
you add by hand are not kept by the next web change.

Enabled providers (`ai.provider.<id>`), `locale` and `statusbar.resources` are
central: the web saves them to their central files, as the TUI Settings does,
and never to `web.toml`.

## Rare Tunables

These are intended for debugging or local policy, not routine setup:

| Variable | Purpose |
| --- | --- |
| `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` | Override the Settings/default collapse window for duplicate AI desktop notifications keyed on the pane-local AI notification key. The Settings value and default apply only when this env is unset or not a positive integer. |
| `PROJMUX_CODEX_TITLE_WATCH_INTERVAL` | Title-watch loop pacing for Codex panes. |
| `PROJMUX_CODEX_REPLY_SETTLE_LOOPS` | Reply-detection settle-loop pacing for Codex panes. |
