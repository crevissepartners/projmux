# Configuration

Most users can configure projmux from `projmux settings`. Environment variables
are available for repeatable shell setup, managed machines, or advanced
overrides.

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

Use Settings > Project Picker for the normal interactive flow:

- `Project Root` sets, changes, or clears the saved primary root.
- `+ Add Workdir...` appends one directory to the saved workdirs list.
- `Workdirs` reviews and removes saved workdirs.

`Add Workdir > Type path manually...` skips the filesystem scan and is useful
for large mounts, WSL paths, NFS paths, or temporary project roots.

The saved workdir file is:

```text
~/.config/projmux/workdirs
```

It stores one absolute path per line. Lines beginning with `#` are comments.
The file is read only when no env root list is set.

## Project Named Snapshots

Project open exposes reusable restore choices as named snapshots. Older
checkouts may already have named snapshots in the legacy storage directory:

```text
<project>/.projmux/layouts/<name>.toml
```

The project context comes from `PROJMUX_CWD` when set, otherwise projmux walks
upward from the current directory to the nearest `.projmux` or `.git` marker.
Files outside that project tree are not discovered. This storage is treated as
legacy import data for the `Named snapshot` row in Project open. New primary
user-facing surfaces describe the restore unit as a snapshot, not as a separate
layout or preset feature.

The legacy schema is intentionally close to the session-state snapshot shape:

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

`command` records a startup recipe, matching the supported session-state replay
recipe. Panes without `command` may use `recipe = "shell"`. Supported
interpolation placeholders are limited to `${PROJMUX_CWD}` and
`${PROJMUX_SESSION}`; other `${...}` values are rejected during load.

Unknown fields and unknown sections are ignored so future schema additions do
not break older files. The built-in parser only accepts quoted strings and
integer values for the known fields above; it does not implement the full TOML
language.

## Keymap File

Settings > Keybindings is the normal in-app editor for action keys. It lists
actions with the current active keys and one state: Default, Custom, Available,
or Unbound. Open an action to see the action label, state, a flat Keys list,
Options, and a collapsed Troubleshooting row. Create flows start from
`+ Add key`, deletion lives under each key's detail, and action-level state
changes such as Unbind or Reset live under Options. Successful saves report the
keybinding as saved; failures identify the stage that failed.

Raw sequences that cannot be safely represented as a direct keybinding are not
persisted. Use Settings to save a custom key. When key delivery needs
terminal-layer remediation, first try the key in `projmux shell`, then run
`projmux setup` from the raw terminal, then use `projmux init` for supported
terminal adapters.

`~/.config/projmux/keymap.toml` can also be edited by hand. When the file is
absent, generated tmux config stays on the built-in defaults.

Supported schema:

```toml
[bindings.ProjectSidebarToggle]
keys = ["M-1", "M-a"]

[bindings.new-window]
keys = ["C-t"]

[bindings."Sidebar:PinProject"]
keys = ["M-p", "p"]
```

Each table is `[bindings.<action-id>]`. Supported keys are:

| Key | Meaning |
| --- | --- |
| `keys` | A list of no-prefix tmux plain chords such as `M-a`, `C-t`, or `M-S-Left`. |
| `plain` | Legacy single-primary replacement. Still read, but not written by Settings. |

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
[bindings.ProjectSidebarToggle]
keys = []
```

In Settings, reset removes the saved override and returns to the built-in
default. Legacy popup IDs such as
`sessionizer-sidebar` still read, but new writes use canonical toggle names
such as `ProjectSidebarToggle`, `NotifySidebarToggle`, `SessionPopupToggle`,
`AISplitPickerToggle`, `SettingsToggle`, and `ProjectSwitcherToggle`. Internal
popup commands use `Surface:Action` IDs and have surface-local conflict
domains and remain visible in Settings when catalogued.

The Settings writer is deterministic and rewrites the supported saved subset
only. If the existing file has parse errors or unknown action IDs, Settings
shows the keymap error row and refuses to overwrite it until the file is fixed.

The file currently affects generated tmux config from `projmux tmux
print-config`, `projmux tmux install`, `projmux tmux print-app-config`,
`projmux tmux install-app`, and `projmux shell`. Terminal init adapters such as
Ghostty and Windows Terminal install built-in plain-byte mappings where needed;
they do not read `keymap.toml` or copy saved keys into terminal configs.
Changing terminal-layer mappings still requires rerunning `projmux init` and
restarting the terminal where that terminal requires it.

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
prints `Next: run \`projmux tmux apply\`` to sync a running server.

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

Native picker popups launched through `projmux tmux popup-toggle` also pass a
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

## AI Resume Picker

The AI resume picker (`projmux ai split --agent resume`) lists the most recent
deduplicated Claude/Codex resume sessions. The number of rows it shows and how
far below the current directory it scans are both configurable; the defaults are
30 rows and depth 0 (the current directory only).

Preferred interactive path:

- `Settings > AI Settings > Resume picker`

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

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `PROJMUX_PROJDIR` | Explicit primary project root. Accepts an OS-native PATH-style multi-value: the first non-empty entry is the primary root and later entries are prepended to managed-root discovery. The primary value is memoized to `~/.config/projmux/projdir`. |
| `PROJMUX_MANAGED_ROOTS` | Search-root override. Uses the OS-native path-list separator and takes priority over the saved workdirs file and default weak probes. |
| `TMUX_SESSIONIZER_ROOTS` | Legacy alias still honored at runtime for managed roots. |
| `PROJMUX_LOCALE` | UI locale override. `auto` resumes detection; `en-US` and `ko-KR` pin supported locales. Unsupported tags fall back to `en-US` and surface a Settings warning. |
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in Linux/WSL sender. Separate from declarative `[hooks.send-noti]`. |
| `PROJMUX_AI_RESUME_PICKER_LIMIT` | Overrides the AI resume picker row count (`[ai] resume_picker_limit`). Clamped to 1-100; takes priority over project and global config. |
| `PROJMUX_AI_RESUME_SCAN_DEPTH` | Overrides the AI resume picker cwd-tree scan depth (`[ai] resume_scan_depth`). Clamped to 0-8; takes priority over project and global config. Depth 0 keeps the exact-cwd behavior. |
| `PROJMUX_NOTIFY_HOOK_DEPTH` | Internal recursion guard for `send-noti` hooks. Depth `>= 1` suppresses nested hook dispatch while still allowing the queue write itself. |
| `PROJMUX_NOTIFY_EXPIRE_MS` | AI desktop notification expiration in milliseconds. Defaults to `5000`; unset, zero, negative, and non-numeric values fall back to the default. |
| `PROJMUX_DESKTOP_NOTIFY_MODE` | OS desktop notification mode override. `none` / `notify` / `raise` (case insensitive). When set, this takes priority over every other resolution rung. The in-app notify queue is not affected. |
| `PROJMUX_DESKTOP_NOTIFY` | Legacy on/off override kept for backward compatibility. `on` maps to `notify`, `off` maps to `none`. Honored only when `PROJMUX_DESKTOP_NOTIFY_MODE` is unset. |
| `PROJMUX_WSL_TOAST_ICON_DIR` | Directory used when copying the WSL toast icon into a Windows-readable path. |
| `PROJMUX_USAGE_STATE_DIR` | Override directory for AI usage snapshots. Defaults to `<state>/projmux/usage`. Point this at a synced directory to share authoritative usage across machines. |
| `PROJMUX_USAGE_DEBUG` | When non-empty, prints adapter errors from `projmux status usage` to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated. Read but ignored; limits now come from upstream APIs and local Codex rollout state. |
| `PROJMUX_SESSIONSTATE_AUTOSAVE` | Session snapshot autosave override for the global fallback. Values such as `off`, `false`, or `0` disable autosave for projects that inherit the global setting; explicit project auto-save `on`/`off` still takes precedence. |
| `PROJMUX_SESSIONSTATE_DEBUG` | When non-empty, quiet autosave surfaces suppressed session-state errors to stderr. |
| `PROJMUX_FOCUS_DEBUG` | When non-empty, `projmux focus` prints one telemetry line to stderr. |
| `PROJMUX_PICKER_BACKEND` | Legacy picker backend override. Any value, including old `fzf` settings, now resolves to the native picker. |
| `PROJMUX_INSTALLER` | Installer source hint used by update flows. npm installs set this automatically; advanced release installs can set `github-release`. |
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

## tmux Project Root Option

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
  `projmux notify push` from recursively re-triggering itself.

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

Settings exposes this at `Settings > Notifications > AI notification dedupe`.
The value is stored as integer seconds and applies only to AI desktop
notification dispatch. The tmux bell fallback keeps its fixed 5 second
dedupe window.

AI hook runtime actions are stored at:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/projmux/ai-hook-actions.json
```

Settings exposes this at `Settings > Notifications > Hook quiet policy`.
The file maps provider/event names to `notify`, `state`, or `quiet` and
overrides catalog `action` values during ingest, including known Codex and
Claude events. It does not change hook installation; `projmux ai integrate`
continues to use the embedded/local catalog `install` fields.

Delivery depends on the event handler. Specialized notify handlers, such as
Codex `PermissionRequest` and `Stop`, can write the in-app notify queue and use
the configured OS desktop notification path. Known Codex events without a
specialized handler can be runtime-overridden to `notify`, but that creates
only a generic in-app queue/sidebar/statusbar row; it does not fire OS toast,
`notify-send`, `PROJMUX_NOTIFY_HOOK`, or `[hooks.send-noti]`.

### Desktop notification mode

The OS-level dispatch carries three modes. The in-app notify queue, the
statusbar segment, and the attention badge stay live regardless of which
mode is active — only the toast / notify-send / auto-raise fan-out is
gated here. The same mode also gates `projmux focus` post-switch osfocus
dispatch: only `raise` asks the host terminal to come forward after a
successful tmux focus.

| Mode | On push | On click |
| --- | --- | --- |
| `off` / `none` | no toast | n/a |
| `notify` | toast / notify-send fires | no click action |
| `raise` | toast / notify-send fires AND the host terminal is auto-raised via the osfocus chain | toast click invokes `projmux focus --uri` via the `projmux://` handler |

Click activation is wired only for `raise`. The `projmux://` URI handler is
registered on the first `raise` Notify of each tmux server (gated by the
`@projmux_uri_protocol_registered_v6` marker). The
mode only controls whether a toast fires at all and whether to follow it
up with an on-push auto-raise. `off` / `none` and `notify` also suppress
the focus-triggered osfocus raise after `projmux focus`.

Resolution order (highest priority first):

1. `PROJMUX_DESKTOP_NOTIFY_MODE` env (`off` / `none` / `notify` / `raise`).
2. `PROJMUX_DESKTOP_NOTIFY` env (legacy `on` / `off`; `on` → `notify`,
   `off` → `none`).
3. Saved Settings config
   `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/desktop-notify-mode`
   (`off` / `notify` / `raise`).
4. Tmux global option `@projmux_desktop_notify_mode`.
5. Tmux global option `@projmux_desktop_notify` (legacy `1` / `0`, same
   mapping as the env above).
6. Default = `raise` when running inside WSL with `$WT_SESSION` set
   (Windows Terminal × WSL is the measured-working cell for osfocus
   raise today); otherwise `notify`.

Migration is intentionally read-time. Users with the previous legacy
toggle set keep their behavior — `@projmux_desktop_notify=0` resolves to
`none`, `@projmux_desktop_notify=1` resolves to `notify`. The first
Settings press through the new row writes `desktop-notify-mode`, mirrors the
new value into `@projmux_desktop_notify_mode` when tmux is live, and leaves the
legacy key unused. No eager rewrite of tmux state.

Toggle from Settings > Notifications > `Desktop notifications`. The
Settings info row labels the effective source as `env`, `env (legacy)`,
`setting`, `setting (legacy)`, or `default` so users see which rung of
the cascade pinned the value. `projmux tmux apply` regenerates the live tmux
option from the saved Settings file; when that file is missing, apply writes
only the default for the current host.

Hook details for new-session lifecycle hooks and project-local
`.projmux/config.toml` live in [Hooks](hooks.md).
Settings names that entry point as **Project recipe** (search still matches
`config.toml` as an alias) to avoid leaking internal file names in the primary
Settings view.

### Toast click handler (WSL + Windows Terminal)

On WSL, projmux registers a `projmux://` URL scheme on the Windows side
the first time a Toast is dispatched on each tmux server. Clicking the
toast hands control back to projmux inside WSL via the registered command:

```text
wscript.exe //B //Nologo "%LOCALAPPDATA%\projmux\projmux-uri-handler.vbs" "%1"
```

Registration writes the VBScript launcher under `%LOCALAPPDATA%\projmux`.
The launcher receives `%1` as a WScript argument, caret-escapes command-line
metacharacters such as `&`, and starts a hidden `%ComSpec% /d /s /c` command
that invokes `wsl.exe`. Its argv semantics are equivalent to:

```text
wsl.exe -d $WSL_DISTRO_NAME --exec <absolute-path-to-projmux> focus --uri <uri>
```

For click activation to work in our unpackaged Win32 setup, four
conditions must hold simultaneously — all of them are arranged by the
notify path so users do not configure anything:

1. No COM Toast Activator is registered. The shortcut writes only
   `PKEY_AppUserModel_ID` (pid=5) and intentionally omits
   `PKEY_AppUserModel_ToastActivatorCLSID` (pid=26). When a COM activator
   is registered alongside the AppID, Windows tries COM first, silently
   fails for unpackaged exes, and does *not* fall through to the launch
   URI. Stripping the COM side makes Windows ShellExecute the launch URI
   on click.
2. The Start Menu shortcut exists with the AppID set. The shortcut is
   never launched — it is a property bag so the toast can route under
   the right DisplayName + icon.
3. The shortcut target is `cmd.exe /c exit`. Earlier code used
   `powershell.exe -WindowStyle Hidden -Command exit`; Windows Defender
   silently quarantines such shortcuts moments after creation, which
   leaves no AppID-tagged shortcut and breaks both the routing and the
   click path. `cmd.exe /c exit` is treated as benign and survives.
4. The WSL handler uses a WScript launcher instead of launching `wsl.exe` or
   `powershell.exe` directly, avoiding the transient Windows console flash on
   Toast click. Inside that launcher it still uses `--exec`, not `--`.
   `wsl.exe -- <cmd>` routes its tail through the user's login shell, which
   parses `&` query-string separators as background-job operators (zsh emits
   `parse error near '&'`). `--exec` skips the shell and invokes the binary
   directly. The `%1` URI is passed as a WScript argument and then forwarded
   through a hidden cmd.exe command line with `&` escaped as `^&`, so query
   separators are data instead of PowerShell, cmd, or WSL login-shell syntax.
   The absolute WSL filesystem path to the binary is captured at registration
   so PATH does not need to be populated under `--exec`.

The URI carries the originating pane id and tmux socket so the click
round-trips back to the exact pane that fired the notification, which
the `projmux focus` path then redirects via `tmux switch-client`.

Registration markers and the writes involved:

- Registry keys (HKCU): `SOFTWARE\Classes\projmux\(Default)`,
  `SOFTWARE\Classes\projmux\URL Protocol`, and
  `SOFTWARE\Classes\projmux\shell\open\command\(Default)`.
- Launcher file: `%LOCALAPPDATA%\projmux\projmux-uri-handler.vbs`.
- tmux user-option marker `@projmux_uri_protocol_registered_v6` records that
  registration has been attempted on this server so the script runs at most
  once per server boot. (The v5 marker
  `@projmux_uri_protocol_registered_v5` was bumped when the WScript launcher
  added the hidden cmd.exe parser hop; existing v5 users re-register
  transparently on the next Notify after upgrade. After successful v6
  registration, projmux removes legacy URI marker keys from v1 through v5 so
  tmux state reflects only the active handler generation.)

Limitations:

- The handler captures the user's current `WSL_DISTRO_NAME` at registration
  time. Users running multiple WSL distros get one handler bound to the
  first distro that fired a toast — clicks from a different distro's toast
  will route back through the first distro. Tier-2 follow-up.
- WSL2 cold-start latency: the first click after a long idle goes through
  WSL boot and typically takes 2-3s to surface the focus on the pane.

## Usage Tracking

`projmux usage` and the status-bar usage segment store snapshots under:

```text
${PROJMUX_USAGE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/projmux/usage}/snapshots.json
```

See [Usage tracking](usage-tracking.md) for adapter behavior, throttling, and
failure handling.

## Session State

`projmux shell` autosaves session snapshots from the app tmux status tick. The
autosave command is quiet and debounced per session, and stores snapshots under
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/sessions`.

Global auto-save defaults to `off` on a fresh install. Project auto-save is an
override with `inherit`, `on`, and `off`; `inherit` follows the global value,
while `on` and `off` take precedence. Auto-save only updates the latest
snapshot. Named snapshots are manual and are never updated by auto-save.

Project open from the Alt-1 sidebar defaults to opening a closed project as an
`Empty session`. The optional `Settings > Session State > Sidebar startup
picker` toggle enables the native sidebar `Start project` step. Rows appear as `Latest
snapshot`, named snapshot rows, `Empty session`, and `Back`. `Latest snapshot`
is the snapshot auto-save that changes as auto-save runs; named snapshots are
fixed snapshots. Rows include saved-at date/time metadata when projmux can
determine it. `Back` returns to the project list without creating, replaying, or
opening a session. After the startup mode is selected, project automation trust
is evaluated if needed. A named snapshot containing a startup `command` must
authorize the SHA-256 of the exact layout bytes used for parse/restore, even
when project hooks are disabled or `.projmux/config.toml` is absent. Approval
continues the selected path and deny/cancel aborts without session create,
snapshot replay, or startup command. The Alt-1 sidebar opens trust as the shared
client-scoped `Trust project automation` popup
instead of inline sidebar rows. The selected open continuation runs in a
detached tmux job that can close the sidebar before trust without depending on
the self-closing sidebar process to keep running. Deny/cancel refreshes the
original sidebar query/selection context with a visible status message. Existing
sessions switch directly without a startup picker.

Default `projmux shell` no longer opens a startup picker or replays session-state
snapshots before attach. It still derives the default app session identity and
startup directory from the current project context when available; otherwise it
uses the `home` target and home directory. Session-state restore selection is
limited to the Session State sidebar startup picker.

Settings > Session State is global settings only: global auto-save, auto-save
interval, and storage/retention policy. Settings > Project > Session State
is override/effective-focused: project identity, project auto-save
`inherit`/`on`/`off`, effective auto-save value/source, and snapshot save
actions. Snapshot inspection lives under `Projects > Sessions > State`, whose
overview shows latest/named snapshot status and the window -> pane read model
without immediate mutation.

The saved global toggles live under
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sessionstate-autosave`,
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sessionstate-autosave-interval`, and
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sidebar-startup-picker`. Project
auto-save overrides live under
`${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sessionstate-projects/<session>/autosave`.
The environment variables above override the global files.

Manual snapshot actions are available from the CLI:

```sh
projmux session-state status [--session <name>]
projmux session-state save
projmux session-state delete [--session <name>]
projmux session-state restore --dry-run [--session <name>]
```

`status` prints the source label (`autosave`, `layout(<name>)`, or `fresh`), the
effective auto-save state, and a compact snapshot preview for the
target session. Older snapshots without a source field display as `autosave`.
`save` captures the current tmux session immediately and intentionally bypasses
the autosave debounce and disabled-autosave gate; it still requires a current
tmux session. `delete` removes the target snapshot without an interactive
confirmation. `restore --dry-run` is preview-only in this release and does not
create sessions or send tmux commands.

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

## Live System Resources Lab

`Settings > Labs > Live system resources` controls the experimental compact
CPU/memory segment on the lower status row. The saved global value is:

```text
~/.config/projmux/live-resources
```

Accepted values are `off` (default) and `on`. The feature is available on
macOS, native Linux, and WSL. Linux reads procfs directly; macOS reads Mach host
statistics and `hw.memsize`. Neither path launches `top`, `vm_stat`, `free`,
PowerShell, or another metrics process. On macOS, available memory is free plus
inactive pages, matching the reclaimable-memory intent of Linux
`MemAvailable`. In WSL the values describe the Linux guest/VM view, not total
Windows host utilization. The CPU delta cache is internal state at
`${XDG_STATE_HOME:-~/.local/state}/projmux/live-resources-sample.json`.
CPU reference samples older than 30 seconds are ignored and replaced on the
next refresh.

## Rare Tunables

These are intended for debugging or local policy, not routine setup:

| Variable | Purpose |
| --- | --- |
| `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` | Override the Settings/default collapse window for duplicate AI desktop notifications keyed on the pane-local AI notification key. |
| `PROJMUX_CODEX_TITLE_WATCH_INTERVAL` | Title-watch loop pacing for Codex panes. |
| `PROJMUX_CODEX_REPLY_SETTLE_LOOPS` | Reply-detection settle-loop pacing for Codex panes. |
| `TMUX_KUBE_CACHE_TTL` | Kubernetes status segment cache TTL. |
| `TMUX_KUBE_TIMEOUT` | kubectl invocation budget for the Kubernetes status segment. |
