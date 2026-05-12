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

## Project Layout Presets

Projects may keep reusable tmux layout seeds in:

```text
<project>/.projmux/layouts/<name>.toml
```

The project context comes from `PROJMUX_CWD` when set, otherwise projmux walks
upward from the current directory to the nearest `.projmux` or `.git` marker.
Files outside that project tree are not discovered.

Use `projmux layout save [--description <text>] [--fresh] <name>` from inside a
tmux session to capture the current session into this directory. The save path
uses the same tmux capture logic as `projmux session-state save`; paths inside
the project root are rendered as `${PROJMUX_CWD}` or `${PROJMUX_CWD}/rel` so
the preset stays portable across checkouts. Use `projmux layout remove --force <name>`
to delete a preset non-interactively.

The Phase 1 schema is intentionally close to the session-state snapshot shape:

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
not break older `layout list` and `layout show` flows. The built-in parser only
accepts quoted strings and integer values for the known fields above; it does
not implement the full TOML language.

## Keymap File

Settings > Keybindings is the normal in-app editor for action keys. It lists
each action, opens a detail screen, and uses `Press new key` to capture one
keypress through the same controlling-TTY probe path as `projmux setup`.
Saving writes safe tmux plain chords to `~/.config/projmux/keymap.toml`,
rewrites `~/.config/projmux/tmux.conf`, and, when Settings is running inside
tmux, sources that app config so tmux-level chords take effect immediately.

Settings reports CSI-u/User-key captures as terminal fallback delivery and
does not write a keymap entry for them. Raw sequences that cannot be safely
represented as a tmux plain chord are not persisted; configure terminal
fallback with `projmux init` instead.

`~/.config/projmux/keymap.toml` can also be edited by hand. When the file is
absent, generated tmux config stays on the built-in defaults.

Supported schema:

```toml
[bindings.sessionizer-sidebar]
plain = "M-a"

[bindings.new-window]
plain = "C-t"
```

Each table is `[bindings.<action-id>]`. Supported keys are:

| Key | Meaning |
| --- | --- |
| `plain` | A no-prefix tmux chord such as `M-a`, `C-t`, or `M-S-Left`. |

Legacy `prefix = ...` entries still parse during migration so existing files
do not break, but Settings no longer writes prefix keys and generated tmux
config no longer binds the old action prefix chords.

Set a value to the empty string to disable that chord for the action:

```toml
[bindings.sessionizer-sidebar]
plain = ""
```

In Settings, `Disable` writes the empty plain override. `Reset default`
removes that override and returns to the built-in default. The Settings writer
is deterministic and rewrites the supported saved subset only:
`[bindings.<action-id>]` tables with `plain` string keys. If the existing file
has parse errors or unknown action IDs, Settings shows the keymap error row and
refuses to overwrite it until the file is fixed.

The file currently affects generated tmux config from `projmux tmux
print-config`, `projmux tmux install`, `projmux tmux print-app-config`,
`projmux tmux install-app`, and `projmux shell`. Terminal init adapters such as
Ghostty and Windows Terminal still install the built-in CSI-u fallback map.
Changing those terminal-layer mappings still requires rerunning `projmux init`
and restarting the terminal where that terminal requires it.

When a chord is overridden, projmux emits unbinds for both the stale default
chord and the replacement before binding the merged action. Popup and floating
UI actions still route through `tmux popup-toggle`, so pressing the same
configured key opens and closes the popup.

## Environment Variables

| Variable | Purpose |
| --- | --- |
| `PROJMUX_PROJDIR` | Explicit primary project root. Accepts an OS-native PATH-style multi-value: the first non-empty entry is the primary root and later entries are prepended to managed-root discovery. The primary value is memoized to `~/.config/projmux/projdir`. |
| `PROJMUX_MANAGED_ROOTS` | Search-root override. Uses the OS-native path-list separator and takes priority over the saved workdirs file and default weak probes. |
| `TMUX_SESSIONIZER_ROOTS` | Legacy alias still honored at runtime for managed roots. |
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in Linux/WSL sender. Separate from declarative `[hooks.send-noti]`. |
| `PROJMUX_NOTIFY_HOOK_DEPTH` | Internal recursion guard for `send-noti` hooks. Depth `>= 1` suppresses nested hook dispatch while still allowing the queue write itself. |
| `PROJMUX_DESKTOP_NOTIFY_MODE` | OS desktop notification mode override. `none` / `notify` / `raise` (case insensitive). When set, this takes priority over every other resolution rung. The in-app notify queue is not affected. |
| `PROJMUX_DESKTOP_NOTIFY` | Legacy on/off override kept for backward compatibility. `on` maps to `notify`, `off` maps to `none`. Honored only when `PROJMUX_DESKTOP_NOTIFY_MODE` is unset. |
| `PROJMUX_WSL_TOAST_ICON_DIR` | Directory used when copying the WSL toast icon into a Windows-readable path. |
| `PROJMUX_USAGE_STATE_DIR` | Override directory for AI usage snapshots. Defaults to `<state>/projmux/usage`. Point this at a synced directory to share authoritative usage across machines. |
| `PROJMUX_USAGE_DEBUG` | When non-empty, prints adapter errors from `projmux status usage` to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated. Read but ignored; limits now come from upstream APIs and local Codex rollout state. |
| `PROJMUX_SESSIONSTATE_AUTOSAVE` | Session snapshot autosave override. Values such as `off`, `false`, or `0` disable autosave regardless of the saved Settings value. |
| `PROJMUX_SESSIONSTATE_AUTORESTORE` | Session snapshot auto-restore override. Values such as `off`, `false`, or `0` disable auto-restore regardless of the saved Settings value. |
| `PROJMUX_SESSIONSTATE_DEBUG` | When non-empty, quiet autosave surfaces suppressed session-state errors to stderr. |
| `PROJMUX_FOCUS_DEBUG` | When non-empty, `projmux focus` prints one telemetry line to stderr. |
| `PROJMUX_PICKER_BACKEND` | Legacy picker backend override. Any value, including old `fzf` settings, now resolves to the native picker. |
| `PROJMUX_INSTALLER` | Installer source hint used by update flows. npm installs set this automatically; advanced release installs can set `github-release`. |

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
`config.toml`.

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

### Desktop notification mode

The OS-level dispatch carries three modes. The in-app notify queue, the
statusbar segment, and the attention badge stay live regardless of which
mode is active — only the toast / notify-send / auto-raise fan-out is
gated here.

| Mode | On push | On click |
| --- | --- | --- |
| `none` | no toast | n/a |
| `notify` | toast / notify-send fires | toast click invokes `projmux focus --uri` via the `projmux://` handler |
| `raise` | toast / notify-send fires AND the host terminal is auto-raised via the osfocus chain | same as `notify` — click is always available |

Click activation is always wired. The `projmux://` URI handler is
registered on the first Notify of each tmux server (gated by the
`@projmux_uri_protocol_registered_v2` marker) regardless of mode. The
mode only controls whether a toast fires at all and whether to follow it
up with an on-push auto-raise.

Resolution order (highest priority first):

1. `PROJMUX_DESKTOP_NOTIFY_MODE` env (`none` / `notify` / `raise`).
2. `PROJMUX_DESKTOP_NOTIFY` env (legacy `on` / `off`; `on` → `notify`,
   `off` → `none`).
3. Tmux global option `@projmux_desktop_notify_mode`.
4. Tmux global option `@projmux_desktop_notify` (legacy `1` / `0`, same
   mapping as the env above).
5. Default = `raise` when running inside WSL with `$WT_SESSION` set
   (Windows Terminal × WSL is the measured-working cell for osfocus
   raise today); otherwise `notify`.

Migration is intentionally read-time. Users with the previous legacy
toggle set keep their behavior — `@projmux_desktop_notify=0` resolves to
`none`, `@projmux_desktop_notify=1` resolves to `notify`. The first
Settings press through the new row writes the new key and the legacy key
goes unused. No eager rewrite of tmux state.

Toggle from Settings > AI Settings > `Desktop notifications`. The
Settings info row labels the effective source as `env`, `env (legacy)`,
`setting`, `setting (legacy)`, or `default` so users see which rung of
the cascade pinned the value.

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
wsl.exe -d $WSL_DISTRO_NAME --exec <absolute-path-to-projmux> focus --uri "%1"
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
4. The WSL handler command uses `--exec`, not `--`. `wsl.exe -- <cmd>`
   routes its tail through the user's login shell, which parses `&`
   query-string separators as background-job operators (zsh emits
   `parse error near '&'`). `--exec` skips the shell and invokes the
   binary directly. The absolute WSL filesystem path to the binary is
   captured at registration so PATH does not need to be populated under
   `--exec`.

The URI carries the originating pane id and tmux socket so the click
round-trips back to the exact pane that fired the notification, which
the `projmux focus` path then redirects via `tmux switch-client`.

Registration markers and the writes involved:

- Registry keys (HKCU): `SOFTWARE\Classes\projmux\(Default)`,
  `SOFTWARE\Classes\projmux\URL Protocol`, and
  `SOFTWARE\Classes\projmux\shell\open\command\(Default)`.
- tmux user-option marker `@projmux_uri_protocol_registered_v2` records that
  registration has been attempted on this server so the script runs at most
  once per server boot. (The v1 marker `@projmux_uri_protocol_registered`
  was bumped when the registry command switched to `--exec`; existing v1
  users re-register transparently on the next Notify after upgrade and the
  orphaned v1 key requires no cleanup.)

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

## Shell Welcome State

`projmux shell` stores its once-per-version welcome marker under:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/projmux/welcomed-v<version>.json
```

If the marker is missing, the next shell launch shows the welcome again. If the
marker is corrupt or cannot be written, shell startup continues without the
welcome.

The same guide is also available on demand through `projmux welcome`, and it is
linked from Settings > About as `Welcome`.

When `pending_attach_welcome` is true, the generated projmux shell tmux config
runs `projmux welcome --popup` asynchronously from the `client-attached` hook.
That helper atomically claims the pending marker, flips it off, and shows the
welcome guide in a tmux popup once for that version. Missing, corrupt, or
already-consumed state is a quiet no-op.

Set `PROJMUX_WELCOME=off` before launching or attaching to `projmux shell` to
suppress the automatic attach popup. The manual `projmux welcome` command still
prints the guide.

## Session State

`projmux shell` autosaves session snapshots from the app tmux status tick. The
autosave command is quiet and debounced per session, and stores snapshots under
`${XDG_STATE_HOME:-$HOME/.local/state}/projmux/sessions`.

When auto-restore is enabled, `projmux shell` checks for a saved snapshot before
attaching. Restore only runs when the target app session is absent; an existing
live session is left untouched and the normal attach path continues. Missing
snapshots are quiet. Invalid snapshots or replay failures are reported to
stderr, then `projmux shell` falls back to the normal `tmux new-session -A`
attach behavior.

Settings > Session State shows the effective auto-save / auto-restore state,
the current session snapshot summary, and a delete action. The saved toggles
live under `${XDG_CONFIG_HOME:-$HOME/.config}/projmux/sessionstate-autosave`
and `sessionstate-autorestore`; the environment variables above override those
files.

Manual snapshot actions are available from the CLI:

```sh
projmux session-state status [--session <name>]
projmux session-state save
projmux session-state delete [--session <name>]
projmux session-state restore --dry-run [--session <name>]
```

`status` prints the effective auto-save / auto-restore state and a compact
snapshot preview for the target session. `save` captures the current tmux
session immediately and intentionally bypasses the autosave debounce and
disabled-autosave gate; it still requires a current tmux session. `delete`
removes the target snapshot without an interactive confirmation. `restore
--dry-run` is preview-only in this release and does not create sessions or send
tmux commands.

## Decoration Mode

Settings > Appearance controls optional status and picker decoration:

- `off` is the default and avoids icon-font assumptions.
- `symbol` restores the Nerd Font-style folder, GitHub, and bell icons.
- `emoji` uses emoji decorators, including the notify sidebar header bell.

The saved value lives at:

```text
~/.config/projmux/statusbar-decoration
```

## Rare Tunables

These are intended for debugging or local policy, not routine setup:

| Variable | Purpose |
| --- | --- |
| `PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS` | Collapse window for duplicate AI notifications keyed on `(summary, tag)`. |
| `PROJMUX_CODEX_TITLE_WATCH_INTERVAL` | Title-watch loop pacing for Codex panes. |
| `PROJMUX_CODEX_REPLY_SETTLE_LOOPS` | Reply-detection settle-loop pacing for Codex panes. |
| `TMUX_KUBE_CACHE_TTL` | Kubernetes status segment cache TTL. |
| `TMUX_KUBE_TIMEOUT` | kubectl invocation budget for the Kubernetes status segment. |
