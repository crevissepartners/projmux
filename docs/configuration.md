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

## Keymap File

Settings > Keybindings is the normal in-app editor for tmux key chords. It
lists each action, opens a detail screen, and lets you type `plain` or
`prefix` tmux chord strings. Saving writes `~/.config/projmux/keymap.toml`,
rewrites `~/.config/projmux/tmux.conf`, and, when Settings is running inside
tmux, sources that app config so non-terminal-layer tmux chords take effect
immediately.

Settings > Labs > Diagnose keybindings is the in-app diagnostic/remediation
surface for the same catalog. It probes one action key at a time through the
controlling TTY, reports whether the key arrived as a plain tmux chord, CSI-u
fallback, unexpected sequence, or timeout, and delegates supported terminal
fallback preview/apply operations to the `projmux init` engine.

`~/.config/projmux/keymap.toml` can also be edited by hand. When the file is
absent, generated tmux config stays on the built-in defaults.

Supported schema:

```toml
[bindings.sessionizer-sidebar]
plain = "M-a"
prefix = "A"

[bindings.new-window]
plain = "C-t"
```

Each table is `[bindings.<action-id>]`. Supported keys are:

| Key | Meaning |
| --- | --- |
| `plain` | A no-prefix tmux chord such as `M-a`, `C-t`, or `M-S-Left`. |
| `prefix` | A tmux prefix-table chord such as `A` or `r`. |

Set a value to the empty string to disable that chord for the action:

```toml
[bindings.sessionizer-sidebar]
plain = ""
```

In Settings, `Disable Plain/Prefix chord` writes the empty string override.
`Reset Plain/Prefix chord` removes that override and returns to the built-in
default. The Settings writer is deterministic and rewrites the supported
subset only: `[bindings.<action-id>]` tables with `plain` and `prefix` string
keys. If the existing file has parse errors or unknown action IDs, Settings
shows the keymap error row and refuses to overwrite it until the file is fixed.

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
| `PROJMUX_NOTIFY_HOOK` | External executable that receives AI desktop notifications instead of the built-in Linux/WSL sender. |
| `PROJMUX_DESKTOP_NOTIFY` | OS desktop notification on/off override. `on`/`off` (case insensitive). When set, this takes priority over the saved tmux option and the default. The in-app notify queue is not affected. |
| `PROJMUX_WSL_TOAST_ICON_DIR` | Directory used when copying the WSL toast icon into a Windows-readable path. |
| `PROJMUX_USAGE_STATE_DIR` | Override directory for AI usage snapshots. Defaults to `<state>/projmux/usage`. Point this at a synced directory to share authoritative usage across machines. |
| `PROJMUX_USAGE_DEBUG` | When non-empty, prints adapter errors from `projmux status usage` to stderr. |
| `PROJMUX_USAGE_LIMITS_PATH` | Deprecated. Read but ignored; limits now come from upstream APIs and local Codex rollout state. |
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

The app-name argument is `com.crevisse.projmux` (reverse-domain id used as
the Linux `--app-name`, the macOS sender label, and the Windows
`AppUserModelID`). Earlier releases used `projmux.TmuxCodex`; the new id
ships with a one-shot Windows cleanup that removes the legacy Start Menu
shortcut and registry entry the first time projmux runs.

OS desktop notifications can be silenced without touching the in-app notify
queue. The resolution order is:

1. `PROJMUX_DESKTOP_NOTIFY` env (`on` / `off`).
2. Tmux global option `@projmux_desktop_notify` (`1` / `0`).
3. Default = on.

Toggle from Settings > AI Settings > `Desktop notifications on/off`. The
Settings info row labels the effective source as `env`, `setting`, or
`default` so an env-pinned value is visible at a glance.

Hook details for new-session lifecycle hooks and project-local
`.projmux/config.toml` live in [Hooks](hooks.md).

### Toast click handler (WSL + Windows Terminal)

On WSL, projmux registers a `projmux://` URL scheme on the Windows side the
first time a Toast is dispatched on each tmux server. Clicking the Toast
hands control back to projmux inside WSL via the registered command:

```text
wsl.exe -d $WSL_DISTRO_NAME --exec <absolute-path-to-projmux> focus --uri "%1"
```

`--exec` (rather than `--`) is required so the URI bypasses the user's
default login shell — `wsl.exe -- <cmd>` routes its tail through that shell,
which then parses `&` query-string separators as background-job operators
(zsh emits `parse error near '&'`). The absolute WSL filesystem path to the
binary is captured at registration time so the launch doesn't depend on
PATH being set under `--exec`.

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
